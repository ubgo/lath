// Package wait polls for a condition.
//
// It exists for the retry loop everyone writes and few write correctly: an
// HTTP client with no timeout, a sleep that ignores cancellation, a body never
// drained. Each of those is invisible until the day the thing being waited for
// never arrives.
package wait

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Defaults.
const (
	// DefaultInterval is how often the condition is checked.
	DefaultInterval = 250 * time.Millisecond
	// DefaultTimeout bounds the whole wait. Bounded rather than infinite
	// because a wait with no deadline is a hang with better manners.
	DefaultTimeout = 30 * time.Second
	// httpClientTimeout bounds a single request, separately from the overall
	// wait: one server holding a connection open must not consume the entire
	// budget in a single attempt.
	httpClientTimeout = 5 * time.Second
	// minSuccessStatus and maxSuccessStatus bound the 2xx range HTTPOK accepts.
	minSuccessStatus = 200
	maxSuccessStatus = 299
)

// ErrTimeout means the condition was not met in time.
var ErrTimeout = errors.New("wait: condition not met before timeout")

type config struct {
	interval time.Duration
	timeout  time.Duration
	// The rest apply only to HTTPOK. Kept on one config so a caller passes
	// Interval and Header to the same call without two option types.
	headers map[string]string
	method  string
	client  *http.Client
	accept  func(status int) bool
}

// Option configures a wait.
type Option func(*config)

// Interval sets how often the condition is checked.
func Interval(d time.Duration) Option { return func(c *config) { c.interval = d } }

// Timeout bounds the whole wait.
func Timeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// Header sets a request header on each probe, an authorization token for a
// health endpoint that is not public, or a Host for a virtual host.
func Header(key, value string) Option {
	return func(c *config) {
		if c.headers == nil {
			c.headers = map[string]string{}
		}
		c.headers[key] = value
	}
}

// Method overrides the probe verb. Defaults to GET; HEAD is cheaper where the
// endpoint supports it.
func Method(m string) Option { return func(c *config) { c.method = m } }

// Client supplies an http.Client, for a caller with its own transport, proxy,
// or a certificate the default pool does not trust.
//
// The package never reaches for http.DefaultClient: its settings are
// process-global and someone else's to change.
func Client(hc *http.Client) Option { return func(c *config) { c.client = hc } }

// Accept decides which status codes mean ready.
//
// Defaults to 2xx. Exists because "ready" is not universal, an endpoint
// behind auth may answer 401 while perfectly alive, and a caller waiting on it
// should not have to fork this package to say so.
func Accept(fn func(status int) bool) Option { return func(c *config) { c.accept = fn } }

func build(opts []Option) config {
	c := config{interval: DefaultInterval, timeout: DefaultTimeout}
	for _, o := range opts {
		o(&c)
	}
	// A non-positive interval would panic inside time.NewTicker, turning a
	// caller's arithmetic slip, Interval(total/attempts) with attempts high
	// enough to floor at zero, into a crash rather than a slow poll.
	if c.interval <= 0 {
		c.interval = DefaultInterval
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	return c
}

// Until polls cond until it returns true, the context ends, or the timeout
// elapses.
//
// An error from cond aborts immediately rather than being retried. A condition
// that cannot be evaluated is different from one that is not yet true, and
// retrying the former just delays the report by the full timeout.
//
// cond is checked once BEFORE the first sleep, so an already-satisfied
// condition returns at once instead of paying an interval for nothing.
func Until(ctx context.Context, cond func(context.Context) (bool, error), opts ...Option) error {
	c := build(opts)
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		ok, err := cond(ctx)
		if err != nil {
			return fmt.Errorf("wait: %w", err)
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			// Distinguishes "we ran out of time" from "the caller cancelled",
			// which are different events to a reader of the log.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("wait: after %s: %w", c.timeout, ErrTimeout)
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// HTTPOK polls a URL until it answers with a 2xx.
//
// The common case by a wide margin. Every health gate and smoke test, and
// the one whose hand-written version usually lacks a client timeout and leaks
// connections by not draining bodies.
func HTTPOK(ctx context.Context, url string, opts ...Option) error {
	c := build(opts)
	client := c.client
	if client == nil {
		client = &http.Client{Timeout: httpClientTimeout}
	}
	method := c.method
	if method == "" {
		method = http.MethodGet
	}
	accept := c.accept
	if accept == nil {
		accept = func(code int) bool { return code >= minSuccessStatus && code <= maxSuccessStatus }
	}

	return Until(ctx, func(ctx context.Context) (bool, error) {
		req, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			// A malformed URL will never become valid; abort rather than
			// retry it for the full timeout.
			return false, err
		}
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			// A connection refused is exactly what waiting is for.
			return false, nil
		}
		// Drained before closing so the connection can be reused; otherwise
		// every attempt opens a new one and the pool grows for the duration.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return accept(resp.StatusCode), nil
	}, opts...)
}
