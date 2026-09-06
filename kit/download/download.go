// Package download fetches a URL to a file, verifying it before it lands.
//
// The checksum is the point. An unverified binary fetched over the network and
// then executed is the supply-chain problem in miniature, and the usual
// alternative, curl piped to a file, checked by eye, verifies nothing.
package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ubgo/lath/kit/fsx"
)

const (
	// DefaultTimeout bounds the whole transfer, not just the connection. A
	// stalled download that never errors is indistinguishable from a slow one
	// without it.
	DefaultTimeout = 5 * time.Minute
	// maxRedirects caps the redirect chain. The default client follows ten;
	// this is stated so the limit is a decision rather than an inheritance.
	maxRedirects = 5
	// filePerm is the mode a downloaded file lands with. Not executable:
	// marking a fetched artifact runnable is the caller's decision to make.
	filePerm = 0o644
)

// ErrChecksumMismatch reports that the fetched bytes did not match.
var ErrChecksumMismatch = errors.New("download: checksum does not match")

// ErrStatus reports a non-2xx response.
var ErrStatus = errors.New("download: unexpected status")

type config struct {
	sha256  string
	headers map[string]string
	timeout time.Duration
	client  *http.Client
}

// Option configures a download.
type Option func(*config)

// SHA256 requires the fetched bytes to match a hex digest.
func SHA256(hexDigest string) Option {
	return func(c *config) { c.sha256 = strings.ToLower(strings.TrimSpace(hexDigest)) }
}

// Header sets a request header. An authorization token, an accept type.
func Header(key, value string) Option {
	return func(c *config) {
		if c.headers == nil {
			c.headers = map[string]string{}
		}
		c.headers[key] = value
	}
}

// Timeout bounds the entire transfer.
func Timeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// Client supplies an http.Client, for a caller with its own transport, proxy
// or instrumentation. The package never reaches for http.DefaultClient, whose
// settings are process-global and someone else's to change.
func Client(hc *http.Client) Option { return func(c *config) { c.client = hc } }

// ToFile fetches url into dst.
//
// Invariant: dst is written atomically and only after verification, so a failed
// or unverified download never leaves a partial file that a later step would
// find and assume is good. That is the whole reason this is not four lines of
// io.Copy at the call site.
func ToFile(ctx context.Context, url, dst string, opts ...Option) error {
	c := config{timeout: DefaultTimeout}
	for _, o := range opts {
		o(&c)
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	client := c.client
	if client == nil {
		client = &http.Client{
			// Its OWN transport, not http.DefaultTransport.
			//
			// A fresh http.Client with a nil Transport uses the package-global
			// one, so "we do not use http.DefaultClient" was true and beside
			// the point: the connection pool was still shared with everything
			// else in the process. Anything calling CloseIdleConnections on it
			// — httptest.Server.Close does, on every close — can break an
			// unrelated in-flight request. That is a real download failing
			// because some other component tidied up.
			//
			// Found by running the suite under emulation, where everything is
			// slow enough to widen the window: a parallel test's server closed
			// while another test's download was still reading.
			Transport: http.DefaultTransport.(*http.Transport).Clone(),
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("download: stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		}
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download: fetching %s: %w", url, err)
	}
	defer func() {
		// Drained before closing so the connection can be reused; an
		// undrained body forces a new one on every call.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode > 299 {
		return fmt.Errorf("download: %s: %s: %w", url, resp.Status, ErrStatus)
	}

	// Buffered in memory, then verified, then written. Streaming straight to
	// dst would put unverified bytes at the destination path, which is exactly
	// what the verification exists to prevent.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("download: reading %s: %w", url, err)
	}

	if c.sha256 != "" {
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != c.sha256 {
			return fmt.Errorf("download: %s: got %s, want %s: %w",
				url, got, c.sha256, ErrChecksumMismatch)
		}
	}

	if err := fsx.WriteAtomic(dst, body, os.FileMode(filePerm)); err != nil {
		return err
	}
	return nil
}
