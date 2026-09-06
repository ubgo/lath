// Package certs makes the private key and signing request a certificate
// authority asks for.
//
// Any Go program can use it: it knows nothing about which CA will sign the
// result. Let's Encrypt, a Cloudflare origin CA, an internal PKI and a mutual
// TLS setup all start here.
//
// Standard library only, and no files: the caller decides where a key lives,
// because that decision, and its permissions, is the whole security of the
// thing.
package certs

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
)

// DefaultBits is the RSA key size. 2048 rather than 4096 because every public
// CA accepts it, TLS handshakes are measurably cheaper, and the difference in
// margin is not one anybody's threat model turns on.
const DefaultBits = 2048

// PEM block types, named because they are structural: a file whose header says
// CERTIFICATE REQUEST when it holds a key is rejected with a parse error that
// explains nothing.
const (
	BlockPrivateKey = "PRIVATE KEY"
	BlockCSR        = "CERTIFICATE REQUEST"
)

// Request is a generated keypair and the signing request that goes with it.
//
// Both are PEM. The key never leaves this struct on its own, so a caller
// cannot accidentally send it where the request was meant to go.
type Request struct {
	// PrivateKey is PEM, PKCS#8. Write it 0600 and never log it.
	PrivateKey string
	// CSR is PEM, to be handed to a certificate authority.
	CSR string
	// Hostnames is what the request covers, in the order given.
	Hostnames []string
}

// New generates a key and a certificate signing request for hostnames.
//
// The first hostname becomes the common name and every hostname becomes a SAN,
// including that first one. That duplication is deliberate: modern clients
// ignore the common name entirely and validate against SANs only, so a
// certificate whose name appears in CN alone is rejected by every browser.
//
// bits of 0 means DefaultBits.
func New(hostnames []string, bits int) (Request, error) {
	if len(hostnames) == 0 {
		return Request{}, fmt.Errorf("certs: at least one hostname is required")
	}
	for _, h := range hostnames {
		if h == "" {
			return Request{}, fmt.Errorf("certs: an empty hostname is not a hostname")
		}
	}
	if bits == 0 {
		bits = DefaultBits
	}

	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return Request{}, fmt.Errorf("certs: generating a key: %w", err)
	}
	template := x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: hostnames[0]},
		DNSNames:           hostnames,
		SignatureAlgorithm: x509.SHA256WithRSA,
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &template, key)
	if err != nil {
		return Request{}, fmt.Errorf("certs: creating the request: %w", err)
	}
	// PKCS#8 rather than PKCS#1: it is what every modern consumer expects, and
	// the two are not interchangeable to a parser that wants one of them.
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Request{}, fmt.Errorf("certs: encoding the key: %w", err)
	}

	return Request{
		PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: BlockPrivateKey, Bytes: der})),
		CSR:        string(pem.EncodeToMemory(&pem.Block{Type: BlockCSR, Bytes: csr})),
		Hostnames:  append([]string(nil), hostnames...),
	}, nil
}

// Wildcard is the certificate covering every single-label subdomain of a
// domain, plus the domain itself.
//
// Both, because "*.example.com" does NOT match "example.com", and a
// certificate that covers only the wildcard fails on the bare domain in a way
// that looks like a server fault rather than a certificate one. It also does
// not match "a.b.example.com": a wildcard is one label deep, which is why
// hostname conventions that stay single-label need only one certificate.
func Wildcard(domain string) []string {
	return []string{"*." + domain, domain}
}

// Match reports whether a certificate and a private key belong together.
//
// Worth checking before installing a pair anywhere that matters: a mismatched
// pair is not a certificate that fails for one visitor, it is a server that
// refuses to start. Caddy, nginx and every other TLS terminator treat it as a
// fatal configuration error, so on a shared instance one bad pair takes down
// every site it serves.
//
// Compares the public keys, which is the only thing that actually has to
// agree. Both arguments are PEM.
func Match(certPEM, keyPEM string) error {
	certBlock, _ := pem.Decode([]byte(certPEM))
	if certBlock == nil {
		return fmt.Errorf("certs: the certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return fmt.Errorf("certs: parsing the certificate: %w", err)
	}
	keyBlock, _ := pem.Decode([]byte(keyPEM))
	if keyBlock == nil {
		return fmt.Errorf("certs: the key is not PEM")
	}
	key, err := parsePrivateKey(keyBlock.Bytes)
	if err != nil {
		return err
	}
	// Every stdlib private key type carries Public(); comparing through
	// crypto.PublicKey's Equal keeps this correct for RSA and ECDSA alike
	// without a type switch that would need editing for the next algorithm.
	type publicKey interface{ Equal(crypto.PublicKey) bool }
	pub, ok := key.Public().(publicKey)
	if !ok {
		return fmt.Errorf("certs: unsupported key type %T", key.Public())
	}
	if !pub.Equal(cert.PublicKey) {
		return fmt.Errorf("certs: the key does not match the certificate")
	}
	return nil
}

// signer is what every stdlib private key implements.
type signer interface{ Public() crypto.PublicKey }

// parsePrivateKey accepts the three encodings a key file arrives in.
//
// All three, because which one a certificate authority hands back is not
// something the caller chose, and failing on the format rather than on the
// content is a confusing way to reject a perfectly good key.
func parsePrivateKey(der []byte) (signer, error) {
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if s, ok := k.(signer); ok {
			return s, nil
		}
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	return nil, fmt.Errorf("certs: the key is not PKCS#8, PKCS#1 or SEC 1")
}
