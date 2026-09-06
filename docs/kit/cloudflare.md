# `kit/cloudflare` — DNS records

Finds a zone by name and makes one record say what it should say. Idempotent, and honest about which of those three things happened.

Standard library only. Three calls do not justify an SDK; a program needing the rest of Cloudflare's surface should use the official one rather than growing this.

Unlike most of `kit`, this needs no program installed. It speaks to an HTTP API.

```go
client := cloudflare.Client{Token: token}   // Zone:Read + Zone:DNS:Edit is enough

zone, err := client.ZoneID(ctx, "example.com")
if err != nil {
    return err
}
outcome, err := client.EnsureRecord(ctx, zone, cloudflare.Record{
    Type: cloudflare.TypeA, Name: "api.example.com", Content: "203.0.113.7", Proxied: true,
})
fmt.Println(outcome)   // created | updated | unchanged
```

## API

```go
func (c Client) ZoneID(ctx context.Context, domain string) (string, error)
func (c Client) Records(ctx context.Context, zoneID, recordType, name string) ([]Record, error)
func (c Client) EnsureRecord(ctx context.Context, zoneID string, r Record) (Outcome, error)
```

`Records` is the read half, exported because a caller that only wants to KNOW should not have to call something that writes. An empty result is not an error: absence is an answer.

| Symbol | What |
|---|---|
| `Client` | `Token`, plus `BaseURL` and `HTTP` for tests and for a proxied network |
| `Record` | `ID`, `Type`, `Name`, `Content`, `Proxied`, `TTL` |
| `Zone` | `ID`, `Name` |
| `Outcome` | `OutcomeCreated` · `OutcomeUpdated` · `OutcomeUnchanged` |
| `TypeA` · `TypeAAAA` · `TypeCNAME` · `TypeTXT` | The record types a service deployment uses. `EnsureRecord` accepts any type string; these are named because they are compared, and because a bare `"A"` in a request body reads like a typo |
| `API` | `https://api.cloudflare.com/client/v4`, the default base |
| `ProxiedTTL` | `1`, "automatic". Cloudflare **rejects any other TTL on a proxied record**, which is a confusing error to meet for the first time inside a deploy, so `EnsureRecord` applies it for you |
| `DefaultTimeout` | 20s. Cloudflare answers in well under a second, and a deploy that hangs on DNS is worse than one that fails on it |

## Origin certificates

The certificate a server presents to Cloudflare's edge when a domain is proxied. Not publicly trusted, and that is the point: Cloudflare terminates the browser's TLS at its edge and makes its own connection to the origin, which this secures.

```go
req, err := certs.New(certs.Wildcard("example.com"), 0)   // kit/certs: key stays here
pem, err := cloudflare.OriginCA{Key: originCAKey}.
    Issue(ctx, req.Hostnames, req.CSR, 0)                 // 0 = DefaultValidityDays
```

| Symbol | What |
|---|---|
| `OriginCA` | `Key`, plus `BaseURL` and `HTTP` |
| `Issue` | Signs a CSR, returns the certificate PEM. The private key is never sent |
| `RequestRSA` · `RequestECC` | Which key the CSR was made with. `kit/certs` produces RSA |
| `DefaultValidityDays` | 5475 (15 years), Cloudflare's maximum. This certificate is seen only by Cloudflare's edge, so the usual argument for short lifetimes is weaker than the cost of a silent expiry years later |

⚠️ **This endpoint does not accept API tokens.** It wants the account's **Origin CA Key**, a separate credential from the API Tokens page. Passing a token fails with an authentication error that reads exactly like a bad token, which is why `OriginCA` is a distinct type rather than a method on `Client`.

## Gotchas

⚠️ **Cloudflare reports failure in the BODY, not only in the status.** A `200` can carry `"success": false`. Checking the status alone is how a program "succeeds" at a change that never happened, which is why every call here unwraps the envelope and quotes Cloudflare's own error text.

⚠️ **`ZoneID` fails the same way for "no such domain" and "the token cannot see it."** Both are reported, because a scoped token missing `Zone:Read` looks exactly like a missing zone from here.

⚠️ **One record per type and name.** Matching is by type AND name, which is how Cloudflare identifies a record for this purpose. A round-robin, several A records under one name, is not something this expresses: it corrects the first and leaves the rest.

`Proxied: true` is Cloudflare's orange cloud: traffic goes through their edge, so the origin needs a certificate the edge trusts, and the origin sees `CF-Connecting-IP` rather than the real client address unless the server in front is told to trust it.
