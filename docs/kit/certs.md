# `kit/certs` — keys and signing requests

Makes the private key and CSR a certificate authority asks for. It knows nothing about which CA will sign the result: Let's Encrypt, a Cloudflare origin CA, an internal PKI and a mutual-TLS setup all start here.

No files. The caller decides where a key lives, because that decision and its permissions are the whole security of the thing.

```go
req, err := certs.New(certs.Wildcard("example.com"), 0)   // 0 = DefaultBits
// req.PrivateKey — PEM, PKCS#8. Write 0600, never log.
// req.CSR        — PEM, hand to a CA.
```

| Symbol | What |
|---|---|
| `New(hostnames, bits)` | Generates a keypair and a CSR covering every hostname |
| `Request` | `PrivateKey`, `CSR`, `Hostnames`. The key never leaves the struct alone, so it cannot be sent where the request was meant to go |
| `Wildcard(domain)` | `["*.domain", "domain"]` |
| `DefaultBits` | 2048. Every public CA accepts it, handshakes are cheaper, and the margin over 4096 is not one anybody's threat model turns on |
| `BlockPrivateKey` · `BlockCSR` | PEM headers, named because a file whose header lies is rejected with a parse error that explains nothing |

## Gotchas

⚠️ **`*.example.com` does not match `example.com`.** A certificate covering only the wildcard fails on the bare domain in a way that looks like a server fault rather than a certificate one, which is why `Wildcard` returns both. It also does not match `a.b.example.com`: a wildcard is one label deep, which is why hostname conventions that stay single-label need only one certificate.

⚠️ **The first hostname is both the common name and a SAN.** That duplication is deliberate: modern clients ignore the common name entirely and validate against SANs only, so a certificate whose name appears in CN alone is rejected by every browser.

The key is PKCS#8, not PKCS#1. The two are not interchangeable to a parser that wants one of them.
