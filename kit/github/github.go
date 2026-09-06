// Package github publishes releases and their artifacts.
//
// Plain HTTP against GitHub's REST API, not the `gh` CLI. The CLI is a fine
// tool and a poor dependency: it must be installed, it must be logged in as
// somebody, and its output is meant for a person. A token and a round trip
// work identically on a laptop, in CI, and inside a container with nothing
// installed but this binary.
//
// The operations here are the ones a release needs and no more: create or
// update a release, attach files to it, and READ BACK what was attached. That
// last one is the reason the package exists in this shape. A publish that
// trusts its own exit codes reports success for a release whose assets never
// uploaded, and the first person to discover it is someone trying to install
// the thing.
//
// What belongs to the caller, deliberately: which tag to publish, what the
// release notes say, whether a version is a prerelease, and what the artifacts
// are called. Those are decisions about a project. This package moves bytes.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// API is the default REST endpoint. Overridable per Client for GitHub
// Enterprise, and for a test server.
const API = "https://api.github.com"

// Uploads is the default asset-upload endpoint. GitHub serves uploads from a
// DIFFERENT host than the rest of the API, which is not a detail a caller
// should have to know, and is why it is a separate field rather than a path.
const Uploads = "https://uploads.github.com"

// DefaultTimeout bounds one request. Generous because an asset upload is a
// file transfer, not a JSON call: a release archive on a slow connection
// legitimately takes minutes, and a timeout that kills it produces a
// half-published release.
const DefaultTimeout = 10 * time.Minute

// apiVersion pins the REST API's dated contract. WIRE FORMAT: GitHub uses this
// header to keep old clients working across breaking changes, so pinning it is
// what stops a server-side change altering what this package means.
const apiVersion = "2022-11-28"

// acceptJSON is the media type that selects the versioned JSON API.
const acceptJSON = "application/vnd.github+json"

// ErrNoRelease reports a tag with no release. Not an error condition in
// itself: the first publish of any version passes through it, which is why it
// is a value a caller can test rather than a failure.
var ErrNoRelease = errors.New("github: no release for that tag")

// ErrAssetExists reports an asset name already attached to the release.
// GitHub refuses the upload rather than replacing it, and this package does
// not force: replacing an artifact someone may already have downloaded is a
// decision for the caller, who can DeleteAsset first.
var ErrAssetExists = errors.New("github: an asset with that name is already attached")

// Repo identifies a repository.
type Repo struct {
	Owner string
	Name  string
}

// String renders "owner/name", the form every GitHub URL and error message
// uses.
func (r Repo) String() string { return r.Owner + "/" + r.Name }

// Release is a published version.
//
// ID is assigned by GitHub and is what assets attach to; a caller creating a
// release leaves it zero and reads it back from the result.
type Release struct {
	ID         int64  `json:"id,omitempty"`
	TagName    string `json:"tag_name"`
	Name       string `json:"name,omitempty"`
	Body       string `json:"body,omitempty"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	// URL is the page a human visits. Read-only.
	URL string `json:"html_url,omitempty"`
}

// Asset is one file attached to a release.
type Asset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Size is what GitHub STORED, which is the number worth checking: an
	// upload that was cut short still produces an asset, and its size is the
	// only evidence at this layer that it is incomplete.
	Size int64 `json:"size"`
	// DownloadURL is where the artifact can be fetched from.
	DownloadURL string `json:"browser_download_url"`
}

// Outcome reports what EnsureRelease did.
//
// Defined here rather than shared with the other idempotent packages in this
// kit: a three-value enum is not worth a dependency between two libraries that
// otherwise have nothing to do with each other, and the alternative, a
// "common types" package, is where unrelated things go to become coupled.
type Outcome string

const (
	// OutcomeCreated means no release existed for that tag.
	OutcomeCreated Outcome = "created"
	// OutcomeUpdated means one existed with different notes or flags.
	OutcomeUpdated Outcome = "updated"
	// OutcomeUnchanged means it already said exactly this.
	OutcomeUnchanged Outcome = "unchanged"
)

// OutcomeValues is the canonical list, so a caller switching on an outcome can
// check it covers every case from one place.
var OutcomeValues = []Outcome{OutcomeCreated, OutcomeUpdated, OutcomeUnchanged}

// Client talks to one GitHub instance.
//
// The zero value is unusable: a Token is required. That is deliberate, an
// unauthenticated client can read public releases and fail on everything else,
// which turns a missing credential into a confusing permission error halfway
// through a publish.
type Client struct {
	// Token authenticates every request. A fine-grained token needs
	// "Contents: read and write" on the repository.
	Token string
	// BaseURL overrides API, for GitHub Enterprise or a test server.
	BaseURL string
	// UploadURL overrides Uploads. Separate from BaseURL because GitHub
	// serves uploads from another host; a test server sets both to itself.
	UploadURL string
	// HTTP is the client used for every request. Nil means one with
	// DefaultTimeout.
	HTTP *http.Client
}

// Release returns the release published for a tag.
//
// Returns ErrNoRelease when the tag has none, which is the ordinary state
// before a version's first publish rather than a failure.
func (c Client) Release(ctx context.Context, repo Repo, tag string) (Release, error) {
	var out Release
	path := fmt.Sprintf("/repos/%s/releases/tags/%s", repo, url.PathEscape(tag))
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		if errors.Is(err, errNotFound) {
			return Release{}, fmt.Errorf("%s %s: %w", repo, tag, ErrNoRelease)
		}
		return Release{}, fmt.Errorf("reading the release for %s: %w", tag, err)
	}
	return out, nil
}

// EnsureRelease creates the release for r.TagName, or updates it to match.
//
// Idempotent on purpose: publishing is the step most likely to be re-run,
// because everything that can fail after a tag exists, a network drop, an
// expired credential, a half-uploaded asset, leaves a release that a retry
// must be able to walk back into. A create-only call would make the retry
// fail on the one thing that already worked.
//
// Unchanged is reported by comparing the fields this package sets, so a
// re-publish of identical notes is visibly a no-op rather than a silent PATCH.
func (c Client) EnsureRelease(ctx context.Context, repo Repo, r Release) (Release, Outcome, error) {
	existing, err := c.Release(ctx, repo, r.TagName)
	switch {
	case errors.Is(err, ErrNoRelease):
		var created Release
		path := fmt.Sprintf("/repos/%s/releases", repo)
		if err := c.do(ctx, http.MethodPost, path, r, &created); err != nil {
			return Release{}, "", fmt.Errorf("creating the release for %s: %w", r.TagName, err)
		}
		return created, OutcomeCreated, nil
	case err != nil:
		return Release{}, "", err
	}

	if existing.Name == r.Name && existing.Body == r.Body &&
		existing.Draft == r.Draft && existing.Prerelease == r.Prerelease {
		return existing, OutcomeUnchanged, nil
	}
	var updated Release
	path := fmt.Sprintf("/repos/%s/releases/%d", repo, existing.ID)
	if err := c.do(ctx, http.MethodPatch, path, r, &updated); err != nil {
		return Release{}, "", fmt.Errorf("updating the release for %s: %w", r.TagName, err)
	}
	return updated, OutcomeUpdated, nil
}

// Assets lists what is attached to a release.
//
// The read half, and the one a publish must not skip: this is how a caller
// proves the artifacts it uploaded are actually there, with the sizes GitHub
// stored, rather than trusting that its own uploads returned 201.
func (c Client) Assets(ctx context.Context, repo Repo, releaseID int64) ([]Asset, error) {
	var out []Asset
	path := fmt.Sprintf("/repos/%s/releases/%d/assets?per_page=%d", repo, releaseID, assetPageSize)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, fmt.Errorf("listing assets of release %d: %w", releaseID, err)
	}
	return out, nil
}

// assetPageSize asks for GitHub's maximum page. A release with more artifacts
// than this exists, but not in any project this serves; asking for the maximum
// means the pagination that would be needed is one flag away rather than a
// silent truncation at the default of thirty.
const assetPageSize = 100

// UploadAsset attaches a file to a release.
//
// size is required and is not a convenience: GitHub rejects a chunked upload,
// so the length must be known before the body is sent. Pass the file's size
// from its FileInfo; an io.Reader that cannot report one has to be buffered by
// the caller, which is a decision about memory that belongs to them.
//
// Returns ErrAssetExists when the name is taken. This package never replaces
// silently: an artifact someone may have downloaded is not something to
// overwrite on a caller's behalf. Delete it first if that is the intent.
func (c Client) UploadAsset(ctx context.Context, repo Repo, releaseID int64, name string, body io.Reader, size int64) (Asset, error) {
	endpoint := c.uploadURL() +
		fmt.Sprintf("/repos/%s/releases/%d/assets?name=%s", repo, releaseID, url.QueryEscape(name))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return Asset{}, err
	}
	c.authenticate(req)
	// Deliberately generic: GitHub stores this and serves it back, and
	// guessing a specific type from the extension would mislabel every
	// artifact whose extension this package has not heard of.
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = size

	var asset Asset
	if err := c.send(req, &asset); err != nil {
		if errors.Is(err, errUnprocessable) {
			return Asset{}, fmt.Errorf("%s: %w", name, ErrAssetExists)
		}
		return Asset{}, fmt.Errorf("uploading %s: %w", name, err)
	}
	return asset, nil
}

// DeleteAsset removes one attached file, so a caller that means to replace an
// artifact can say so explicitly.
func (c Client) DeleteAsset(ctx context.Context, repo Repo, assetID int64) error {
	path := fmt.Sprintf("/repos/%s/releases/assets/%d", repo, assetID)
	if err := c.do(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return fmt.Errorf("deleting asset %d: %w", assetID, err)
	}
	return nil
}

// Sentinels for the status codes this package interprets. Unexported: they are
// how it classifies a response internally, and every one a caller can act on
// is re-reported above as a named error with the subject in the message.
var (
	errNotFound      = errors.New("github: not found")
	errUnprocessable = errors.New("github: rejected")
)

func (c Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return API
}

func (c Client) uploadURL() string {
	if c.UploadURL != "" {
		return c.UploadURL
	}
	return Uploads
}

func (c Client) authenticate(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", acceptJSON)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
}

// do issues one JSON request. out may be nil for responses with no body worth
// keeping.
//
// body and out are `any` because this is the JSON boundary, the one place a
// value legitimately crosses into the type system untyped: body is marshalled
// on the way out, and out is unmarshalled into the CALLER's typed struct on the
// way back, so nothing untyped escapes this function. Every exported method
// above passes a concrete type, which is what keeps the boundary one function
// wide.
func (c Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding the request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, reader)
	if err != nil {
		return err
	}
	c.authenticate(req)
	if body != nil {
		req.Header.Set("Content-Type", acceptJSON)
	}
	return c.send(req, out)
}

// send performs a prepared request and decodes the result into out, which is
// the caller's typed struct. See do for why this is the one untyped parameter
// in the package.
func (c Client) send(req *http.Request, out any) error {
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode == http.StatusUnprocessableEntity:
		return fmt.Errorf("%s: %s: %w", resp.Status, describe(raw), errUnprocessable)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("%s: %s", resp.Status, describe(raw))
	}

	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decoding the response: %w", err)
	}
	return nil
}

// maxBody caps what is read from a response. GitHub's are small; an unbounded
// read is how a proxy's error page becomes a memory problem.
const maxBody = 1 << 20

// describe renders GitHub's own explanation of a refusal, which is far more
// useful than the status line: "already_exists" on an asset name and
// "not_found" on a repository arrive with the same 422 otherwise.
func describe(raw []byte) string {
	var envelope struct {
		Message string `json:"message"`
		Errors  []struct {
			Resource string `json:"resource"`
			Field    string `json:"field"`
			Code     string `json:"code"`
			Message  string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Message == "" {
		return trim(raw)
	}
	parts := []string{envelope.Message}
	for _, e := range envelope.Errors {
		detail := e.Message
		if detail == "" {
			detail = strings.TrimSpace(e.Resource + " " + e.Field + " " + e.Code)
		}
		if detail != "" {
			parts = append(parts, detail)
		}
	}
	return strings.Join(parts, "; ")
}

// trim bounds a non-JSON body, an HTML error page from something in front of
// the API, so an error message stays readable in a terminal.
func trim(b []byte) string {
	const limit = 200
	s := strings.TrimSpace(string(b))
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}
