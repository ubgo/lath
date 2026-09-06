package cloudflare_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ubgo/lath/kit/cloudflare"
)

// stub answers Cloudflare's shape: an envelope with success, errors, result.
func stub(t *testing.T, handler http.HandlerFunc) cloudflare.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return cloudflare.Client{Token: "t", BaseURL: srv.URL}
}

func envelope(w http.ResponseWriter, result any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
}

// TestEnsureACreatesWhenAbsent, the first deploy of an environment.
func TestEnsureACreatesWhenAbsent(t *testing.T) {
	t.Parallel()
	var posted atomic.Value
	c := stub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			envelope(w, []any{}) // no such record yet
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		posted.Store(body)
		envelope(w, map[string]any{"id": "rec1"})
	})

	got, err := c.EnsureRecord(context.Background(), "zone1", cloudflare.Record{
		Type: cloudflare.TypeA, Name: "api.example.com", Content: "203.0.113.7", Proxied: true})
	if err != nil {
		t.Fatal(err)
	}
	if got != cloudflare.OutcomeCreated {
		t.Errorf("outcome = %q, want created", got)
	}
	body, _ := posted.Load().(map[string]any)
	if body["content"] != "203.0.113.7" || body["proxied"] != true {
		t.Errorf("posted %v", body)
	}
	// Cloudflare rejects any TTL but 1 on a proxied record, and meeting that
	// for the first time inside a deploy is a confusing way to learn it.
	if body["ttl"] != float64(cloudflare.ProxiedTTL) {
		t.Errorf("ttl = %v, want %d", body["ttl"], cloudflare.ProxiedTTL)
	}
}

// TestEnsureAIsIdempotent. A deploy runs this every time and the common case
// is that nothing needs to change; saying "updated" then would be a lie.
func TestEnsureAIsIdempotent(t *testing.T) {
	t.Parallel()
	var writes atomic.Int32
	c := stub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		envelope(w, []any{map[string]any{
			"id": "rec1", "type": "A", "name": "api.example.com",
			"content": "203.0.113.7", "proxied": true, "ttl": 1,
		}})
	})

	got, err := c.EnsureRecord(context.Background(), "z", cloudflare.Record{
		Type: cloudflare.TypeA, Name: "api.example.com", Content: "203.0.113.7", Proxied: true})
	if err != nil {
		t.Fatal(err)
	}
	if got != cloudflare.OutcomeUnchanged {
		t.Errorf("outcome = %q, want unchanged", got)
	}
	if writes.Load() != 0 {
		t.Errorf("a record that already matched was written %d time(s)", writes.Load())
	}
}

// TestEnsureAUpdatesAMovedRecord covers the box changing address.
func TestEnsureAUpdatesAMovedRecord(t *testing.T) {
	t.Parallel()
	var method atomic.Value
	c := stub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			envelope(w, []any{map[string]any{
				"id": "rec1", "type": "A", "name": "api.example.com",
				"content": "198.51.100.1", "proxied": true, "ttl": 1,
			}})
			return
		}
		method.Store(r.Method)
		envelope(w, map[string]any{"id": "rec1"})
	})

	got, err := c.EnsureRecord(context.Background(), "z", cloudflare.Record{
		Type: cloudflare.TypeA, Name: "api.example.com", Content: "203.0.113.7", Proxied: true})
	if err != nil {
		t.Fatal(err)
	}
	if got != cloudflare.OutcomeUpdated {
		t.Errorf("outcome = %q, want updated", got)
	}
	// PUT, not PATCH: the whole record is being stated, and a partial update
	// omitting proxied would silently keep the old value.
	if method.Load() != http.MethodPut {
		t.Errorf("used %v, want PUT", method.Load())
	}
}

// TestFailureInThe200Body is the one that matters most: Cloudflare reports
// failure in the BODY, so a 200 can still be a refusal. Checking the status
// alone is how a deploy "succeeds" at a change that never happened.
func TestFailureInThe200Body(t *testing.T) {
	t.Parallel()
	c := stub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"errors":  []any{map[string]any{"code": 10000, "message": "Authentication error"}},
		})
	})

	_, err := c.EnsureRecord(context.Background(), "z", cloudflare.Record{
		Type: cloudflare.TypeA, Name: "api.example.com", Content: "203.0.113.7", Proxied: true})
	if err == nil {
		t.Fatal("a failed change was reported as success")
	}
	// Cloudflare's own message explains the refusal far better than a status.
	if !strings.Contains(err.Error(), "Authentication error") {
		t.Errorf("err = %v, want Cloudflare's message", err)
	}
}

// TestZoneIDRefusesAnUnknownDomain, with a message that names the two causes
// worth checking rather than an empty result.
func TestZoneIDRefusesAnUnknownDomain(t *testing.T) {
	t.Parallel()
	c := stub(t, func(w http.ResponseWriter, r *http.Request) { envelope(w, []any{}) })

	_, err := c.ZoneID(context.Background(), "nope.example")
	if err == nil {
		t.Fatal("an unknown zone was accepted")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("err = %v, want it to mention the token as a possible cause", err)
	}
}

// TestEnsureRecordWorksForAnyType. The mechanism is "make this record say
// this"; A is the common case, not the only one.
func TestEnsureRecordWorksForAnyType(t *testing.T) {
	t.Parallel()
	var posted atomic.Value
	c := stub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			envelope(w, []any{})
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		posted.Store(body)
		envelope(w, map[string]any{"id": "rec1"})
	})

	_, err := c.EnsureRecord(context.Background(), "z", cloudflare.Record{
		Type: cloudflare.TypeCNAME, Name: "www.example.com", Content: "example.com", TTL: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := posted.Load().(map[string]any)
	if body["type"] != cloudflare.TypeCNAME {
		t.Errorf("type = %v", body["type"])
	}
	// An unproxied record keeps the caller's TTL; only a proxied one is forced
	// to automatic.
	if body["ttl"] != float64(300) {
		t.Errorf("ttl = %v, want the caller's 300", body["ttl"])
	}
}

// TestEnsureRecordRejectsAnIncompleteRecord, rather than sending Cloudflare a
// half-formed body and relaying whichever error it happens to produce.
func TestEnsureRecordRejectsAnIncompleteRecord(t *testing.T) {
	t.Parallel()
	c := stub(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("an incomplete record reached the API")
	})
	for _, r := range []cloudflare.Record{
		{Name: "a.example.com", Content: "1.2.3.4"},
		{Type: cloudflare.TypeA, Content: "1.2.3.4"},
		{Type: cloudflare.TypeA, Name: "a.example.com"},
	} {
		if _, err := c.EnsureRecord(context.Background(), "z", r); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
}

// TestOriginCAUsesItsOwnCredential is the trap this type exists to prevent:
// the origin certificate endpoint does NOT accept API tokens, and passing one
// fails with an authentication error that reads exactly like a bad token.
func TestOriginCAUsesItsOwnCredential(t *testing.T) {
	t.Parallel()
	var auth, service atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		service.Store(r.Header.Get("X-Auth-User-Service-Key"))
		envelope(w, map[string]any{"certificate": "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----"})
	}))
	defer srv.Close()

	ca := cloudflare.OriginCA{Key: "v1.0-originkey", BaseURL: srv.URL}
	got, err := ca.Issue(context.Background(), []string{"*.example.com", "example.com"}, "-----BEGIN CERTIFICATE REQUEST-----", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "BEGIN CERTIFICATE") {
		t.Errorf("certificate = %q", got)
	}
	if service.Load() != "v1.0-originkey" {
		t.Errorf("X-Auth-User-Service-Key = %v", service.Load())
	}
	if auth.Load() != "" {
		t.Errorf("a bearer token was sent as well: %v", auth.Load())
	}
}

// TestOriginCARefusesWithoutItsKey, with a message that names the credential
// rather than letting Cloudflare answer "authentication error".
func TestOriginCARefusesWithoutItsKey(t *testing.T) {
	t.Parallel()
	_, err := cloudflare.OriginCA{}.Issue(context.Background(), []string{"example.com"}, "csr", 0)
	if err == nil {
		t.Fatal("issued without a credential")
	}
	if !strings.Contains(err.Error(), "Origin CA Key") {
		t.Errorf("err = %v, want it to name the credential", err)
	}
}

// TestOriginCARefusesAnEmptyCertificate. A success envelope with no
// certificate in it would otherwise be written to disk as an empty file, and
// Caddy refuses to start on a malformed cert, taking every site with it.
func TestOriginCARefusesAnEmptyCertificate(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		envelope(w, map[string]any{"certificate": ""})
	}))
	defer srv.Close()

	_, err := cloudflare.OriginCA{Key: "k", BaseURL: srv.URL}.
		Issue(context.Background(), []string{"example.com"}, "csr", 0)
	if err == nil {
		t.Fatal("an empty certificate was accepted")
	}
}

// TestRecordsIsAReadThatDoesNotWrite. A caller asking what exists should not
// have to call something that changes it.
func TestRecordsIsAReadThatDoesNotWrite(t *testing.T) {
	t.Parallel()
	var wrote atomic.Bool
	c := stub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			wrote.Store(true)
		}
		envelope(w, []any{map[string]any{
			"id": "rec1", "type": "A", "name": "api.example.com",
			"content": "203.0.113.7", "proxied": true, "ttl": 1,
		}})
	})

	got, err := c.Records(context.Background(), "z", cloudflare.TypeA, "api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "203.0.113.7" {
		t.Errorf("Records = %+v", got)
	}
	if wrote.Load() {
		t.Error("a read issued a write")
	}
}

// TestRecordsReportsAbsenceAsAnAnswer, not as an error: "no such record" is
// exactly what a caller checking before creating one needs to hear.
func TestRecordsReportsAbsenceAsAnAnswer(t *testing.T) {
	t.Parallel()
	c := stub(t, func(w http.ResponseWriter, r *http.Request) { envelope(w, []any{}) })
	got, err := c.Records(context.Background(), "z", cloudflare.TypeA, "nothing.example.com")
	if err != nil {
		t.Fatalf("absence was an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Records = %+v", got)
	}
}

// TestAnHTMLErrorPageIsReportedAsOne. Something in front of the API, a proxy,
// a captive portal, a WAF, answers with HTML rather than Cloudflare's
// envelope. The JSON decode fails, and the useful information is the status
// plus a glimpse of the body; reporting a bare "invalid character '<'" sends
// the caller looking for a bug in this package.
func TestAnHTMLErrorPageIsReportedAsOne(t *testing.T) {
	t.Parallel()
	c := stub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	})

	_, err := c.ZoneID(context.Background(), "example.com")
	if err == nil {
		t.Fatal("an HTML error page was accepted as a zone listing")
	}
	for _, want := range []string{"502", "unexpected response", "Bad Gateway"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v; want it to mention %q", err, want)
		}
	}
}

// TestAnEnormousErrorPageIsTruncated. An error message is read by a human in a
// terminal: pasting 64 KiB of HTML into it destroys the surrounding output,
// which usually includes the rest of the deploy's failure.
func TestAnEnormousErrorPageIsTruncated(t *testing.T) {
	t.Parallel()
	const flood = 5000
	c := stub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("x", flood)))
	})

	_, err := c.ZoneID(context.Background(), "example.com")
	if err == nil {
		t.Fatal("a flood of bytes was accepted as a zone listing")
	}
	if len(err.Error()) >= flood {
		t.Errorf("the error is %d bytes; the body was pasted in whole", len(err.Error()))
	}
	if !strings.Contains(err.Error(), "…") {
		t.Error("the truncation is not marked, so the reader cannot tell the body was cut")
	}
}

// TestAFailureWithNoReasonStillSaysSomething. Cloudflare can refuse with an
// empty error list, and "the request failed: " with nothing after it reads as
// a bug in the reporting rather than an answer from the API.
func TestAFailureWithNoReasonStillSaysSomething(t *testing.T) {
	t.Parallel()
	c := stub(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{}})
	})

	_, err := c.ZoneID(context.Background(), "example.com")
	if err == nil || !strings.Contains(err.Error(), "gave no reason") {
		t.Errorf("err = %v; want the empty-reason case spelled out", err)
	}
}

// TestEveryCloudflareReasonSurvives. A refusal often carries more than one
// error and the second is frequently the actionable one, "record already
// exists" after "insufficient permissions".
func TestEveryCloudflareReasonSurvives(t *testing.T) {
	t.Parallel()
	c := stub(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"errors": []map[string]any{
				{"code": 9109, "message": "invalid access"},
				{"code": 81057, "message": "record already exists"},
			},
		})
	})

	_, err := c.ZoneID(context.Background(), "example.com")
	if err == nil {
		t.Fatal("a refusal was accepted")
	}
	for _, want := range []string{"9109", "invalid access", "81057", "record already exists"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v; want it to keep %q", err, want)
		}
	}
}

// TestAResultThatIsNotTheExpectedShape. The envelope says success and the
// result is a string where a list of zones belongs; without naming the decode,
// this surfaces as an empty zone list and then as "zone not in this account",
// which is a lie that sends the caller to the Cloudflare dashboard.
func TestAResultThatIsNotTheExpectedShape(t *testing.T) {
	t.Parallel()
	c := stub(t, func(w http.ResponseWriter, _ *http.Request) {
		envelope(w, "not a list of zones")
	})

	_, err := c.ZoneID(context.Background(), "example.com")
	if err == nil {
		t.Fatal("a result of the wrong shape was accepted")
	}
	if !strings.Contains(err.Error(), "decoding the result") {
		t.Errorf("err = %v; want the decode named rather than reported as an absent zone", err)
	}
}

// TestAnUnreachableAPIFailsAsItself. The deploy step above this one prints the
// error verbatim, so a dial failure must arrive as a dial failure.
func TestAnUnreachableAPIFailsAsItself(t *testing.T) {
	t.Parallel()
	// A port nothing is listening on, in the reserved-for-documentation
	// range: no DNS lookup, no traffic that leaves the machine.
	c := cloudflare.Client{Token: "t", BaseURL: "http://127.0.0.1:1"}

	_, err := c.ZoneID(context.Background(), "example.com")
	if err == nil {
		t.Fatal("a connection to nothing succeeded")
	}
	if !strings.Contains(err.Error(), "example.com") {
		t.Errorf("err = %v; want the domain being looked up named", err)
	}
}

// TestACancelledContextStopsTheRequest. A deploy interrupted with ctrl-c must
// not sit in a network call, and the reason must survive.
func TestACancelledContextStopsTheRequest(t *testing.T) {
	t.Parallel()
	c := stub(t, func(w http.ResponseWriter, _ *http.Request) { envelope(w, []any{}) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.ZoneID(ctx, "example.com")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want the cancellation to stay reachable", err)
	}
}
