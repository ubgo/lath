// Package cloudflare manages Cloudflare DNS records.
//
// Any Go program can use it: it knows nothing about pipelines, deploys or this
// tool. What it owns is the part that is the same for everyone, finding a zone
// by name and making one record say what it should say, idempotently, with
// Cloudflare's own refusal quoted when it says no.
//
// Standard library only, because this is three calls. A program needing the
// rest of Cloudflare's surface should use the official SDK rather than growing
// this.
//
// Requires nothing installed: unlike most of kit, this speaks to an HTTP API
// rather than driving a local binary.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// API is Cloudflare's v4 base. A constant so a test can point at a stub.
const API = "https://api.cloudflare.com/client/v4"

// DefaultTimeout bounds every call. Cloudflare answers in well under a second;
// a deploy that hangs on DNS is worse than one that fails on it.
const DefaultTimeout = 20 * time.Second

// ProxiedTTL is what Cloudflare requires for a proxied record: 1, meaning
// "automatic". Any other value is rejected, which is a confusing error to meet
// for the first time inside a deploy.
const ProxiedTTL = 1

// Record types, named because they are compared and because a bare "A" in a
// request body reads like a typo. Not an exhaustive list of what Cloudflare
// accepts: EnsureRecord takes any type string, and these are the ones a
// service deployment uses.
const (
	TypeA     = "A"
	TypeAAAA  = "AAAA"
	TypeCNAME = "CNAME"
	TypeTXT   = "TXT"
)

// Client talks to one Cloudflare account.
type Client struct {
	// Token is a scoped API token: Zone:Read plus Zone:DNS:Edit is enough for
	// everything here. An account-wide key would also work and is worth
	// avoiding: this package only ever needs one zone's records.
	Token string
	// BaseURL defaults to API. Set by tests.
	BaseURL string
	// HTTP defaults to a client with DefaultTimeout.
	HTTP *http.Client
	// originKey routes a request through the Origin CA credential instead of
	// the bearer token. Unexported: callers reach it through OriginCA, which
	// exists so the two credentials cannot be confused for one another.
	originKey string
}

// response is the envelope every Cloudflare v4 endpoint returns.
//
// Success is reported in the BODY, not only in the status, so a 200 can still
// be a failure. Checking the status alone is the classic way to "succeed" at a
// change that never happened.
type response struct {
	Success bool              `json:"success"`
	Errors  []cloudflareError `json:"errors"`
	Result  json.RawMessage   `json:"result"`
}

type cloudflareError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Zone is a Cloudflare zone, which is to say a domain.
type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Record is one DNS record.
type Record struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
}

// Outcome reports what EnsureA did, so a caller can say so rather than
// claiming a change it did not make.
type Outcome string

const (
	// OutcomeCreated means the record did not exist.
	OutcomeCreated Outcome = "created"
	// OutcomeUpdated means it existed and pointed somewhere else.
	OutcomeUpdated Outcome = "updated"
	// OutcomeUnchanged means it already said exactly this.
	OutcomeUnchanged Outcome = "unchanged"
)

// ZoneID resolves a domain to its zone.
//
// By name rather than by a configured id, because an id is an opaque string
// nobody can check by eye, and a wrong one fails with "zone not found" while
// looking perfectly plausible in a config file.
func (c Client) ZoneID(ctx context.Context, domain string) (string, error) {
	var zones []Zone
	if err := c.do(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(domain), nil, &zones); err != nil {
		return "", fmt.Errorf("looking up zone %q: %w", domain, err)
	}
	if len(zones) == 0 {
		return "", fmt.Errorf("zone %q is not in this Cloudflare account, or the token cannot see it", domain)
	}
	return zones[0].ID, nil
}

// Records lists the records of one type under one name.
//
// The read half, exported because a caller that only wants to KNOW should not
// have to call something that writes. Empty means no such record, which is not
// an error: absence is an answer.
func (c Client) Records(ctx context.Context, zoneID, recordType, name string) ([]Record, error) {
	var out []Record
	path := fmt.Sprintf("/zones/%s/dns_records?type=%s&name=%s",
		zoneID, url.QueryEscape(recordType), url.QueryEscape(name))
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, fmt.Errorf("listing %s %s: %w", recordType, name, err)
	}
	return out, nil
}

// EnsureRecord makes one record say exactly what r says, creating it when it
// is absent and correcting it when it differs.
//
// Idempotent on purpose: a deploy runs this every time and the common case is
// that nothing needs to change. The Outcome distinguishes those cases, so a
// log can say "unchanged" instead of implying work that did not happen.
//
// Matching is by type AND name, which is how Cloudflare identifies a record
// for these purposes. A name with several records of one type, a round-robin,
// is not something this expresses: it corrects the first and leaves the rest,
// so use the API directly for that.
func (c Client) EnsureRecord(ctx context.Context, zoneID string, r Record) (Outcome, error) {
	if r.Type == "" || r.Name == "" || r.Content == "" {
		return "", fmt.Errorf("a record needs Type, Name and Content")
	}
	name, ip, proxied := r.Name, r.Content, r.Proxied
	existing, err := c.Records(ctx, zoneID, r.Type, name)
	if err != nil {
		return "", err
	}

	// A proxied record must carry TTL 1 ("automatic"); Cloudflare rejects any
	// other value, which is a confusing error to meet for the first time in
	// the middle of a deploy. An unproxied record keeps the caller's TTL, or
	// automatic when it named none.
	ttl := r.TTL
	if proxied || ttl == 0 {
		ttl = ProxiedTTL
	}
	body := map[string]any{
		"type": r.Type, "name": name, "content": ip, "proxied": proxied, "ttl": ttl,
	}

	if len(existing) == 0 {
		if err := c.do(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", body, nil); err != nil {
			return "", fmt.Errorf("creating %s: %w", name, err)
		}
		return OutcomeCreated, nil
	}

	cur := existing[0]
	if cur.Content == ip && cur.Proxied == proxied && (proxied || cur.TTL == ttl) {
		return OutcomeUnchanged, nil
	}
	// PUT rather than PATCH: the full record is being stated, and a partial
	// update that omitted proxied would silently keep the old value.
	if err := c.do(ctx, http.MethodPut, "/zones/"+zoneID+"/dns_records/"+cur.ID, body, nil); err != nil {
		return "", fmt.Errorf("updating %s: %w", name, err)
	}
	return OutcomeUpdated, nil
}

// do performs one call and unwraps Cloudflare's envelope into out.
//
// body and out are `any` because this is the JSON boundary, the one place a
// value legitimately crosses into the type system untyped: body is marshalled
// on the way out, and out is unmarshalled into the CALLER's typed struct on
// the way back, so nothing untyped escapes past this function. Every exported
// method above passes a concrete type.
func (c Client) do(ctx context.Context, method, path string, body, out any) error {
	base := c.BaseURL
	if base == "" {
		base = API
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding the request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return err
	}
	if c.originKey != "" {
		req.Header.Set("X-Auth-User-Service-Key", c.originKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Content-Type", "application/json")

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
	var env response
	if err := json.Unmarshal(raw, &env); err != nil {
		// A body that is not the envelope is usually an HTML error page from
		// something in front of the API, so the status is the useful part.
		return fmt.Errorf("%s: unexpected response: %s", resp.Status, trim(raw))
	}
	if !env.Success {
		return fmt.Errorf("%s: %s", resp.Status, describe(env.Errors))
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("decoding the result: %w", err)
		}
	}
	return nil
}

// maxBody caps what is read from a response. Cloudflare's are small; an
// unbounded read is how a proxy's error page becomes a memory problem.
const maxBody = 64 << 10

// describe renders Cloudflare's own error list, which explains a refusal far
// better than a status code does.
func describe(errs []cloudflareError) string {
	if len(errs) == 0 {
		return "the request failed, and Cloudflare gave no reason"
	}
	out := ""
	for i, e := range errs {
		if i > 0 {
			out += "; "
		}
		out += fmt.Sprintf("%d %s", e.Code, e.Message)
	}
	return out
}

func trim(b []byte) string {
	const limit = 200
	s := string(bytes.TrimSpace(b))
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}

// ── origin certificates ─────────────────────────────────────────────────

// OriginCA issues origin certificates: the ones a server presents to
// Cloudflare's edge when a domain is proxied.
//
// They are NOT publicly trusted, and that is the point. Cloudflare terminates
// the browser's TLS at its edge with a public certificate and then makes its
// own connection to the origin, which this certificate secures. A browser
// reaching the origin directly would refuse it.
//
// Separate from Client because the authentication is different, and
// confusingly so: this endpoint does not accept API tokens. It wants the
// account's Origin CA Key, a distinct credential from the API Tokens page.
// Passing a token here fails with an authentication error that reads exactly
// like a bad token.
type OriginCA struct {
	// Key is the Origin CA Key, sent as X-Auth-User-Service-Key.
	Key string
	// BaseURL defaults to API. Set by tests.
	BaseURL string
	// HTTP defaults to a client with DefaultTimeout.
	HTTP *http.Client
}

// Origin certificate request types.
const (
	// RequestRSA suits a CSR generated with an RSA key, which is what
	// kit/certs produces.
	RequestRSA = "origin-rsa"
	// RequestECC suits an ECDSA CSR.
	RequestECC = "origin-ecc"
)

// DefaultValidityDays is how long an issued origin certificate lasts.
//
// Cloudflare's maximum is 15 years, and taking it is deliberate: this
// certificate is only ever seen by Cloudflare's edge, so the usual argument
// for short lifetimes, limiting the damage of a leak nobody noticed, is weaker
// than the cost of a silent expiry taking a site down years later with nothing
// to point at.
const DefaultValidityDays = 5475

// Issue signs a CSR and returns the certificate PEM.
//
// The private key is never sent: it stays with whoever made the request, which
// is the entire point of a CSR. Pair this with kit/certs.New.
func (o OriginCA) Issue(ctx context.Context, hostnames []string, csrPEM string, validityDays int) (string, error) {
	if o.Key == "" {
		return "", fmt.Errorf("an Origin CA Key is required; an API token is not accepted by this endpoint")
	}
	if len(hostnames) == 0 || csrPEM == "" {
		return "", fmt.Errorf("hostnames and a CSR are required")
	}
	if validityDays <= 0 {
		validityDays = DefaultValidityDays
	}

	body := map[string]any{
		"hostnames":          hostnames,
		"requested_validity": validityDays,
		"request_type":       RequestRSA,
		"csr":                csrPEM,
	}
	var result struct {
		Certificate string `json:"certificate"`
	}
	if err := o.post(ctx, "/certificates", body, &result); err != nil {
		return "", fmt.Errorf("issuing an origin certificate for %v: %w", hostnames, err)
	}
	if result.Certificate == "" {
		return "", fmt.Errorf("Cloudflare accepted the request for %v but returned no certificate", hostnames)
	}
	return result.Certificate, nil
}

// post is do, with the Origin CA credential instead of a bearer token.
func (o OriginCA) post(ctx context.Context, path string, body, out any) error {
	c := Client{BaseURL: o.BaseURL, HTTP: o.HTTP, originKey: o.Key}
	return c.do(ctx, http.MethodPost, path, body, out)
}
