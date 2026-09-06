package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ubgo/lath/kit/github"
)

var repo = github.Repo{Owner: "acme", Name: "app"}

// stub answers as GitHub does, with both hosts pointed at one test server:
// the API and the upload endpoint are different hosts in production, which is
// exactly the distinction a client can get wrong invisibly.
func stub(t *testing.T, handler http.HandlerFunc) github.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return github.Client{Token: "t", BaseURL: srv.URL, UploadURL: srv.URL}
}

func TestRepoRendersTheFormEveryURLUses(t *testing.T) {
	t.Parallel()
	if got := repo.String(); got != "acme/app" {
		t.Errorf("String() = %q, want owner/name", got)
	}
}

// TestATagWithNoReleaseIsAnAnswer. Every version's first publish passes
// through this, so it must be a value a caller can test rather than a failure
// that aborts the release.
func TestATagWithNoReleaseIsAnAnswer(t *testing.T) {
	t.Parallel()
	c := stub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})

	_, err := c.Release(context.Background(), repo, "v1.0.0")
	if !errors.Is(err, github.ErrNoRelease) {
		t.Fatalf("err = %v; want ErrNoRelease", err)
	}
	if !strings.Contains(err.Error(), "v1.0.0") {
		t.Errorf("err = %v; want the tag named", err)
	}
}

// TestEnsureReleaseCreatesThenIsIdempotent is the property a re-run depends
// on. Everything that can fail after a tag exists leaves a release a retry has
// to walk back into; a create-only call would fail on the one thing that
// already worked.
func TestEnsureReleaseCreatesThenIsIdempotent(t *testing.T) {
	t.Parallel()
	var stored atomic.Pointer[github.Release]
	var posts, patches atomic.Int32

	c := stub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			if cur := stored.Load(); cur != nil {
				_ = json.NewEncoder(w).Encode(cur)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		case r.Method == http.MethodPost:
			posts.Add(1)
			var got github.Release
			_ = json.NewDecoder(r.Body).Decode(&got)
			got.ID = 42
			stored.Store(&got)
			_ = json.NewEncoder(w).Encode(got)
		case r.Method == http.MethodPatch:
			patches.Add(1)
			var got github.Release
			_ = json.NewDecoder(r.Body).Decode(&got)
			got.ID = 42
			stored.Store(&got)
			_ = json.NewEncoder(w).Encode(got)
		}
	})

	want := github.Release{TagName: "v1.0.0", Name: "v1.0.0", Body: "first"}

	got, outcome, err := c.EnsureRelease(context.Background(), repo, want)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != github.OutcomeCreated {
		t.Errorf("outcome = %q, want created", outcome)
	}
	if got.ID != 42 {
		t.Errorf("ID = %d; want the id GitHub assigned, which assets attach to", got.ID)
	}

	// Same notes again: a re-publish must be visibly a no-op, not a silent
	// PATCH that churns the release's edit history.
	_, outcome, err = c.EnsureRelease(context.Background(), repo, want)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != github.OutcomeUnchanged {
		t.Errorf("outcome = %q, want unchanged", outcome)
	}
	if patches.Load() != 0 {
		t.Errorf("an identical release was PATCHed %d time(s)", patches.Load())
	}

	// Changed notes: updated, and still no second create.
	changed := want
	changed.Body = "corrected"
	_, outcome, err = c.EnsureRelease(context.Background(), repo, changed)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != github.OutcomeUpdated {
		t.Errorf("outcome = %q, want updated", outcome)
	}
	if posts.Load() != 1 {
		t.Errorf("the release was created %d times", posts.Load())
	}
}

// TestUploadGoesToTheUploadHost. GitHub serves uploads from a different host
// than the rest of the API. Sending an asset to the API host fails in a way
// that reads like a permission problem, so the split is pinned here.
func TestUploadGoesToTheUploadHost(t *testing.T) {
	t.Parallel()
	var uploadHit atomic.Bool
	var name atomic.Value

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the upload went to the API host, where it does not belong")
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(api.Close)
	uploads := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploadHit.Store(true)
		name.Store(r.URL.Query().Get("name"))
		body, _ := io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(github.Asset{ID: 7, Name: r.URL.Query().Get("name"), Size: int64(len(body))})
	}))
	t.Cleanup(uploads.Close)

	c := github.Client{Token: "t", BaseURL: api.URL, UploadURL: uploads.URL}
	const content = "an artifact"

	asset, err := c.UploadAsset(context.Background(), repo, 42,
		"app_darwin_arm64.tar.gz", strings.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	if !uploadHit.Load() {
		t.Fatal("the upload host was never called")
	}
	if got := name.Load(); got != "app_darwin_arm64.tar.gz" {
		t.Errorf("uploaded name = %v", got)
	}
	if asset.Size != int64(len(content)) {
		t.Errorf("Size = %d, want %d", asset.Size, len(content))
	}
}

// TestUploadRefusesToReplaceSilently. An artifact someone may already have
// downloaded is not something to overwrite on a caller's behalf.
func TestUploadRefusesToReplaceSilently(t *testing.T) {
	t.Parallel()
	c := stub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"ReleaseAsset","code":"already_exists","field":"name"}]}`))
	})

	_, err := c.UploadAsset(context.Background(), repo, 42, "app.tar.gz", strings.NewReader("x"), 1)
	if !errors.Is(err, github.ErrAssetExists) {
		t.Fatalf("err = %v; want ErrAssetExists", err)
	}
	if !strings.Contains(err.Error(), "app.tar.gz") {
		t.Errorf("err = %v; want the asset named", err)
	}
}

// TestAssetsAreReadBack is the discipline the package exists for: a publish
// that trusts its own 201s reports success for a release whose artifacts are
// missing or truncated, and the first person to find out is installing it.
func TestAssetsAreReadBack(t *testing.T) {
	t.Parallel()
	c := stub(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/assets") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]github.Asset{
			{ID: 1, Name: "app_darwin_arm64.tar.gz", Size: 1024},
			{ID: 2, Name: "checksums.txt", Size: 96},
		})
	})

	assets, err := c.Assets(context.Background(), repo, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 2 {
		t.Fatalf("got %d assets, want 2", len(assets))
	}
	// The stored SIZE is the point: an upload cut short still produces an
	// asset, and this is the only evidence at this layer that it is partial.
	if assets[0].Size != 1024 {
		t.Errorf("Size = %d; want what GitHub stored", assets[0].Size)
	}
}

// TestGitHubsOwnExplanationSurvives. "already_exists" on an asset and
// "not_found" on a repository both arrive as 422; the status line alone sends
// the reader to the wrong problem.
func TestGitHubsOwnExplanationSurvives(t *testing.T) {
	t.Parallel()
	c := stub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"message":"tag_name is not a valid ref"}]}`))
	})

	_, _, err := c.EnsureRelease(context.Background(), repo, github.Release{TagName: "nonsense"})
	if err == nil {
		t.Fatal("a refusal was accepted")
	}
	for _, want := range []string{"Validation Failed", "not a valid ref"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v; want it to keep %q", err, want)
		}
	}
}

// TestAnHTMLErrorPageIsReportedAsOne. A proxy or a captive portal answers with
// HTML, and "invalid character '<'" sends the reader hunting for a bug in this
// package.
func TestAnHTMLErrorPageIsReportedAsOne(t *testing.T) {
	t.Parallel()
	c := stub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	})

	_, err := c.Release(context.Background(), repo, "v1.0.0")
	if err == nil {
		t.Fatal("an HTML error page was accepted")
	}
	if !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "Bad Gateway") {
		t.Errorf("err = %v; want the status and a glimpse of the body", err)
	}
}

// TestEveryRequestIsAuthenticatedAndVersionPinned. The API version header is
// what stops a server-side change altering what this package means, and a
// missing token turns every write into a confusing permission error partway
// through a publish.
func TestEveryRequestIsAuthenticatedAndVersionPinned(t *testing.T) {
	t.Parallel()
	var auth, version atomic.Value
	c := stub(t, func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		version.Store(r.Header.Get("X-GitHub-Api-Version"))
		_ = json.NewEncoder(w).Encode(github.Release{ID: 1, TagName: "v1.0.0"})
	})

	if _, err := c.Release(context.Background(), repo, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if got := auth.Load(); got != "Bearer t" {
		t.Errorf("Authorization = %v", got)
	}
	if got, _ := version.Load().(string); got == "" {
		t.Error("no API version was pinned; a server-side change could alter what this means")
	}
}

// TestDeleteAssetIsHowAReplacementIsStated. The package never overwrites
// implicitly, so this is the explicit path a caller takes when replacing is
// genuinely the intent.
func TestDeleteAssetIsHowAReplacementIsStated(t *testing.T) {
	t.Parallel()
	var method, path atomic.Value
	c := stub(t, func(w http.ResponseWriter, r *http.Request) {
		method.Store(r.Method)
		path.Store(r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.DeleteAsset(context.Background(), repo, 7); err != nil {
		t.Fatal(err)
	}
	if got := method.Load(); got != http.MethodDelete {
		t.Errorf("method = %v", got)
	}
	if got, _ := path.Load().(string); !strings.HasSuffix(got, "/releases/assets/7") {
		t.Errorf("path = %q", got)
	}
}

// TestACancelledContextStopsTheRequest. A release interrupted with ctrl-c must
// not sit in a network call, and the reason must survive.
func TestACancelledContextStopsTheRequest(t *testing.T) {
	t.Parallel()
	c := stub(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(github.Release{})
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Release(ctx, repo, "v1.0.0")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want the cancellation to stay reachable", err)
	}
}
