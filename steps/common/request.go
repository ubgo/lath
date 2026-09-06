package common

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ubgo/lath/pipeline"
)

const (
	// DefaultRequestMethod is what a Request sends when the caller names no
	// method. POST rather than PATCH: this step knows nothing about the
	// receiver, and POST is what "here is a body, do something with it" means
	// to the widest range of them.
	DefaultRequestMethod = http.MethodPost
	// DefaultRequestTimeout bounds the call. A service that will not answer
	// promptly is not going to do the work either.
	DefaultRequestTimeout = 15 * time.Second
	// DefaultRequestLabel names a Request the caller did not label.
	DefaultRequestLabel = "request"
	// DefaultContentType is sent when the caller names none. JSON, because
	// that is what an admin or webhook endpoint accepts unless it says
	// otherwise, and a caller sending anything else knows it.
	DefaultContentType = "application/json"
	// responseTailBytes caps how much of a failed response is quoted. Enough
	// for a message and a hint, short of pasting a page of HTML into a log.
	responseTailBytes = 500
)

// Request calls an HTTP endpoint at a point in the pipeline, with a body the
// caller builds from pipeline state.
//
// This is a mechanism, not a policy, and it is the only one this package has
// any business owning. It was called SwitchTraffic and was written to point a
// reverse proxy at a new release, which is one thing you do with an HTTP call
// after a release is healthy. The others are just as common: purging a CDN,
// registering with service discovery, posting a deploy marker to an
// observability vendor, opening a change ticket, telling a chat channel. Named
// for the deploy step it was first used for, every one of those looked like it
// needed a step that did not exist.
//
// What stays here is the part that is the same everywhere: when it fires, that
// the body is built from state the pipeline proved was produced, how long it
// waits, which statuses count, and quoting what the far end said when it
// refuses. What the receiver wants, the URL, the method, the shape of the
// payload, belongs to the definition, because Caddy, Traefik, nginx, Fastly
// and Slack agree about none of it.
//
// A traffic switch is then this step plus a Body function; see
// docs/steps/README.md for that recipe, including refusing to route to an
// empty set of containers.
type Request struct {
	// Label names this step in plans, the debugger and logs. Empty uses
	// DefaultRequestLabel.
	//
	// Worth setting whenever a pipeline has more than one: "switch-traffic"
	// and "purge-cdn" are what a reader needs, and "request" twice is not.
	Label string
	// URL is the full destination, including any path and query.
	URL string
	// Method defaults to DefaultRequestMethod.
	Method string
	// Reads are the state keys Body will read.
	//
	// Declared as data rather than discovered at run time so they join
	// Requires: a missing producer then fails validation before anything runs,
	// instead of failing this step minutes into a deploy. Same reason
	// docker.Build takes ArgsFromState as keys.
	Reads []pipeline.Key
	// Body builds the request body from pipeline state.
	//
	// A function rather than a template string because every receiver wants a
	// different shape, and a caller escaping JSON inside a format string will
	// eventually get it wrong in a way only production notices. Returning an
	// error is also how a caller refuses to send: a payload that would be
	// meaningless, an empty upstream list, say, stops the deploy here rather
	// than being accepted by the far end and taking effect.
	Body func(*pipeline.State) ([]byte, error)
	// ContentType defaults to DefaultContentType.
	ContentType string
	// Headers are sent with the request, for an API token or a vendor's
	// versioning header. Content-Type is set from ContentType, not here.
	Headers map[string]string
	// Accept reports whether a status counts as success. Nil accepts 2xx.
	//
	// Exists because "success" is the receiver's definition, not this
	// package's: an idempotent registration that answers 409 when the entry is
	// already there has succeeded, and a step that called it a failure would
	// make a replayed deploy fail on its second run.
	Accept func(status int) bool
	// Timeout bounds the call; zero means DefaultRequestTimeout.
	Timeout time.Duration
	// Client sends the request. Nil uses http.DefaultClient.
	//
	// Settable for a receiver behind mutual TLS or a proxy, which is
	// configuration this step has no way to express and no business owning.
	Client *http.Client
}

// Name returns Label, or DefaultRequestLabel when the caller set none.
func (r Request) Name() string {
	if r.Label != "" {
		return r.Label
	}
	return DefaultRequestLabel
}

// Docs explains the step in a plan. See pipeline.Documented.
func (r Request) Docs() pipeline.Docs {
	method := r.Method
	if method == "" {
		method = DefaultRequestMethod
	}
	return pipeline.Docs{
		Summary: "call an HTTP endpoint with a body built from pipeline state",
		Detail:  method + " " + r.URL,
	}
}

// Requires is exactly what the caller declared in Reads, so the pipeline can
// prove those producers run first.
func (r Request) Requires() []pipeline.Key { return r.Reads }
func (Request) Provides() []pipeline.Key   { return nil }

// Replayable reports that re-running this step is indistinguishable from
// running it once.
//
// True because the request is rebuilt from the same state and sent again:
// setting a value to what it already is changes nothing. A caller whose
// endpoint is NOT idempotent, one that appends rather than sets, should say so
// by wrapping this step rather than by hoping nobody presses rerun.
func (Request) Replayable() bool { return true }

// Validate checks the author-supplied configuration.
func (r Request) Validate() error {
	if r.URL == "" {
		return fmt.Errorf("URL is required")
	}
	if r.Body == nil {
		return fmt.Errorf("Body is required: this step cannot guess what the receiver expects")
	}
	return nil
}

func (r Request) Run(ctx context.Context, s *pipeline.State) error {
	body, err := r.Body(s)
	if err != nil {
		return fmt.Errorf("%s: building the payload: %w", r.Name(), err)
	}

	method := r.Method
	if method == "" {
		method = DefaultRequestMethod
	}
	if s.DryRun() {
		s.Detailf("would %s %s with %d bytes", method, r.URL, len(body))
		return nil
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, r.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s: %w", r.Name(), err)
	}
	contentType := r.ContentType
	if contentType == "" {
		contentType = DefaultContentType
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}

	s.Detailf("%s %s (%d bytes)", method, r.URL, len(body))
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %s %s: %w", r.Name(), method, r.URL, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if !r.accepts(resp.StatusCode) {
		// The receiver's own message is the only thing that explains a
		// rejection, so it is quoted rather than reduced to a status code.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, responseTailBytes))
		return fmt.Errorf("%s: %s %s: %s: %s",
			r.Name(), method, r.URL, resp.Status, bytes.TrimSpace(detail))
	}
	s.Detailf("%s accepted", resp.Status)
	return nil
}

// accepts applies Accept, defaulting to 2xx.
func (r Request) accepts(status int) bool {
	if r.Accept != nil {
		return r.Accept(status)
	}
	return status >= http.StatusOK && status <= 299
}
