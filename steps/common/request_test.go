package common_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
)

// switchTraffic is the recipe this step replaced a dedicated SwitchTraffic
// step with: a Request plus a Body function. It lives in the tests because it
// is what a definition writes, and if that is more than a handful of lines the
// generalisation cost more than it bought.
//
// It carries the policy that belongs to the caller, refusing to route to an
// empty set, which is why that policy is no longer in the step.
func switchTraffic(adminAPI string) common.Request {
	return common.Request{
		Label:  "switch-traffic",
		URL:    adminAPI + "/config/upstreams",
		Method: http.MethodPatch,
		Reads:  []pipeline.Key{common.KeyRouted},
		Body: func(s *pipeline.State) ([]byte, error) {
			routed, err := pipeline.Get[[]string](s, common.KeyRouted)
			if err != nil {
				return nil, err
			}
			// A proxy told to route to nothing takes the site down, and the
			// request succeeds while doing it.
			if len(routed) == 0 {
				return nil, fmt.Errorf("no routed containers: mark the process that takes inbound traffic with Route")
			}
			return []byte(`["` + strings.Join(routed, `","`) + `"]`), nil
		},
	}
}

// withRouted provides the key the recipe reads: the containers that take
// inbound traffic, not every container the release started.
func withRouted(mode pipeline.Mode, names ...string) *pipeline.State {
	s := deployState(mode)
	pipeline.Set(s, common.KeyRouted, names)
	return s
}

func TestRequestSendsTheCallersPayload(t *testing.T) {
	t.Parallel()
	var gotBody, gotMethod, gotPath, gotType atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody.Store(string(b))
		gotMethod.Store(r.Method)
		gotPath.Store(r.URL.Path)
		gotType.Store(r.Header.Get("Content-Type"))
	}))
	defer srv.Close()

	err := switchTraffic(srv.URL).
		Run(context.Background(), withRouted(pipeline.ModeExecute, "app-web-0", "app-web-1"))
	if err != nil {
		t.Fatal(err)
	}

	if gotBody.Load() != `["app-web-0","app-web-1"]` {
		t.Errorf("body = %v", gotBody.Load())
	}
	if gotMethod.Load() != http.MethodPatch {
		t.Errorf("method = %v; want the caller's method", gotMethod.Load())
	}
	if gotPath.Load() != "/config/upstreams" {
		t.Errorf("path = %v", gotPath.Load())
	}
	if gotType.Load() != common.DefaultContentType {
		t.Errorf("content-type = %v; want the default", gotType.Load())
	}
}

// TestRequestDefaultsToPost. The step knows nothing about the receiver, and
// POST is what "here is a body, do something with it" means to the widest
// range of them. PATCH was the default while this was a proxy step, which is
// the kind of assumption a mechanism should not carry.
func TestRequestDefaultsToPost(t *testing.T) {
	t.Parallel()
	var gotMethod atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotMethod.Store(r.Method)
	}))
	defer srv.Close()

	err := common.Request{URL: srv.URL, Body: emptyBody}.
		Run(context.Background(), deployState(pipeline.ModeExecute))
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod.Load() != common.DefaultRequestMethod {
		t.Errorf("method = %v; want %s", gotMethod.Load(), common.DefaultRequestMethod)
	}
}

// TestRequestReadsBecomeRequires is what keeps the body's inputs checkable: a
// Body reading a key nothing produces must fail validation before the deploy
// starts, not three minutes in when the request is finally built.
func TestRequestReadsBecomeRequires(t *testing.T) {
	t.Parallel()
	step := switchTraffic("http://proxy")
	if got := step.Requires(); len(got) != 1 || got[0] != common.KeyRouted {
		t.Fatalf("Requires() = %v, want the declared Reads", got)
	}
	// A pipeline whose Request reads a key nobody provides must not validate.
	p := pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{step}}
	if err := p.Validate(); err == nil {
		t.Error("a Request reading an unprovided key passed validation")
	}
}

// TestRequestLabelsItself. A pipeline that switches traffic AND purges a CDN
// holds two of these, and a plan listing "request" twice says nothing.
func TestRequestLabelsItself(t *testing.T) {
	t.Parallel()
	if got := (common.Request{}).Name(); got != common.DefaultRequestLabel {
		t.Errorf("unlabelled Request is called %q", got)
	}
	if got := switchTraffic("http://x").Name(); got != "switch-traffic" {
		t.Errorf("Name() = %q, want the caller's label", got)
	}
}

// TestRequestFailsOnARejection pins that a receiver refusing the payload stops
// the deploy: continuing would leave traffic on the old release while
// StopPrevious removed it.
func TestRequestFailsOnARejection(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, "upstream dial address is not valid")
			}))
			defer srv.Close()

			err := switchTraffic(srv.URL).
				Run(context.Background(), withRouted(pipeline.ModeExecute, "app-web-0"))
			if err == nil {
				t.Fatalf("status %d was reported as success", status)
			}
			// The receiver's own message is the only thing that explains a
			// rejection, so it must survive into the error.
			if !strings.Contains(err.Error(), "not valid") {
				t.Errorf("err = %v; want the response quoted", err)
			}
			// And the label, or a plan with three requests cannot be read.
			if !strings.Contains(err.Error(), "switch-traffic") {
				t.Errorf("err = %v; want the step's label", err)
			}
		})
	}
}

// TestRequestAcceptDecidesSuccess. Success is the receiver's definition, not
// this package's: an idempotent registration answering 409 when the entry is
// already there has succeeded, and calling that a failure would break every
// replayed deploy.
func TestRequestAcceptDecidesSuccess(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	step := common.Request{URL: srv.URL, Body: emptyBody}
	if err := step.Run(context.Background(), deployState(pipeline.ModeExecute)); err == nil {
		t.Fatal("409 was accepted by default")
	}
	step.Accept = func(status int) bool { return status < 500 }
	if err := step.Run(context.Background(), deployState(pipeline.ModeExecute)); err != nil {
		t.Errorf("Accept was ignored: %v", err)
	}
}

// TestRequestSendsItsHeaders covers the token an API needs, which no amount of
// body-shaping can carry.
func TestRequestSendsItsHeaders(t *testing.T) {
	t.Parallel()
	var gotAuth, gotType atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		gotType.Store(r.Header.Get("Content-Type"))
	}))
	defer srv.Close()

	err := common.Request{
		URL: srv.URL, Body: emptyBody, ContentType: "text/plain",
		Headers: map[string]string{"Authorization": "Bearer t0ken"},
	}.Run(context.Background(), deployState(pipeline.ModeExecute))
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth.Load() != "Bearer t0ken" {
		t.Errorf("Authorization = %v", gotAuth.Load())
	}
	// ContentType owns the header; Headers must not be able to fight it.
	if gotType.Load() != "text/plain" {
		t.Errorf("content-type = %v", gotType.Load())
	}
}

func TestRequestDryRunSendsNothing(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	if err := switchTraffic(srv.URL).
		Run(context.Background(), withRouted(pipeline.ModeDryRun, "app-web-0")); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 0 {
		t.Errorf("a dry run made %d request(s)", hits.Load())
	}
}

// TestRequestBodyFailureStopsBeforeSending pins that a payload the caller
// could not build never reaches the receiver as something malformed. It is
// also how a caller refuses to send at all: see the empty-routed case below.
func TestRequestBodyFailureStopsBeforeSending(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	boom := errors.New("cannot resolve container ports")
	err := common.Request{
		URL:  srv.URL,
		Body: func(*pipeline.State) ([]byte, error) { return nil, boom },
	}.Run(context.Background(), deployState(pipeline.ModeExecute))

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v; want the payload failure", err)
	}
	if hits.Load() != 0 {
		t.Error("a request was sent despite the payload failing to build")
	}
}

// TestRecipeRefusesAnEmptyDestinationList. Routing to nothing takes a site
// down and succeeds while doing it: the request is well-formed and the proxy
// accepts it. The guard belongs to the caller now, because "an empty list is
// meaningless" is true of a proxy's upstreams and false of, say, a purge.
func TestRecipeRefusesAnEmptyDestinationList(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the proxy was asked to route to nothing")
	}))
	defer srv.Close()

	err := switchTraffic(srv.URL).Run(context.Background(), withRouted(pipeline.ModeExecute))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "Route") {
		t.Errorf("err = %v, want it to name the flag that fixes it", err)
	}
}

func TestRequestValidate(t *testing.T) {
	t.Parallel()
	if err := (common.Request{Body: emptyBody}).Validate(); err == nil {
		t.Error("accepted an empty URL")
	}
	// A nil Body cannot be guessed at: every receiver wants a different shape.
	if err := (common.Request{URL: "http://x"}).Validate(); err == nil {
		t.Error("accepted a nil Body")
	}
	if err := (common.Request{URL: "http://x", Body: emptyBody}).Validate(); err != nil {
		t.Errorf("rejected a valid configuration: %v", err)
	}
}

func TestRequestUnreachableReceiver(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	err := switchTraffic(url).
		Run(context.Background(), withRouted(pipeline.ModeExecute, "app-web-0"))
	if err == nil {
		t.Fatal("an unreachable receiver was reported as success")
	}
}

func emptyBody(*pipeline.State) ([]byte, error) { return []byte(`{}`), nil }
