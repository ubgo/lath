package certs_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ubgo/lath/kit/certs"
)

// TestNewProducesAParseableKeyAndRequest, because "it looked like PEM" is not
// the same as "a CA will accept it".
func TestNewProducesAParseableKeyAndRequest(t *testing.T) {
	t.Parallel()
	got, err := certs.New([]string{"api.example.com"}, 0)
	if err != nil {
		t.Fatal(err)
	}

	block, _ := pem.Decode([]byte(got.CSR))
	if block == nil || block.Type != certs.BlockCSR {
		t.Fatalf("CSR block = %v", block)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatalf("a CA would reject this CSR: %v", err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Errorf("the CSR is not correctly self-signed: %v", err)
	}

	keyBlock, _ := pem.Decode([]byte(got.PrivateKey))
	if keyBlock == nil || keyBlock.Type != certs.BlockPrivateKey {
		t.Fatalf("key block = %v", keyBlock)
	}
	// PKCS#8, not PKCS#1: the two are not interchangeable to a parser that
	// wants one of them.
	if _, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes); err != nil {
		t.Errorf("the key is not PKCS#8: %v", err)
	}
}

// TestEveryHostnameBecomesASAN including the first, which is also the common
// name. Modern clients ignore the common name entirely, so a certificate whose
// name appears only there is rejected by every browser.
func TestEveryHostnameBecomesASAN(t *testing.T) {
	t.Parallel()
	names := []string{"*.example.com", "example.com"}
	got, err := certs.New(names, 0)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(got.CSR))
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range names {
		if !slices.Contains(csr.DNSNames, want) {
			t.Errorf("%q is not a SAN: %v", want, csr.DNSNames)
		}
	}
	if csr.Subject.CommonName != names[0] {
		t.Errorf("common name = %q", csr.Subject.CommonName)
	}
}

// TestWildcardCoversTheBareDomainToo. "*.example.com" does NOT match
// "example.com", and a certificate covering only the wildcard fails on the
// bare domain in a way that looks like a server fault.
func TestWildcardCoversTheBareDomainToo(t *testing.T) {
	t.Parallel()
	got := certs.Wildcard("example.com")
	if !slices.Contains(got, "*.example.com") || !slices.Contains(got, "example.com") {
		t.Errorf("Wildcard = %v, want both the wildcard and the bare domain", got)
	}
}

// TestNewRefusesNothingToSign, rather than producing a certificate request
// that names nobody.
func TestNewRefusesNothingToSign(t *testing.T) {
	t.Parallel()
	for _, names := range [][]string{nil, {}, {""}, {"a.example.com", ""}} {
		if _, err := certs.New(names, 0); err == nil {
			t.Errorf("accepted %v", names)
		}
	}
}

// TestPrivateKeyIsNotInTheCSR. They travel to different places: the request
// goes to a certificate authority, the key never leaves the machine.
func TestPrivateKeyIsNotInTheCSR(t *testing.T) {
	t.Parallel()
	got, err := certs.New([]string{"api.example.com"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.CSR, certs.BlockPrivateKey) {
		t.Error("the CSR carries the private key")
	}
}

// TestMatchAcceptsAPairAndRejectsAStranger. The failure this prevents is not a
// site that serves badly, it is a TLS terminator that refuses to START, taking
// every other site on a shared instance with it.
func TestMatchAcceptsAPairAndRejectsAStranger(t *testing.T) {
	t.Parallel()
	pair := selfSigned(t)
	other := selfSigned(t)

	if err := certs.Match(pair.cert, pair.key); err != nil {
		t.Errorf("a real pair was rejected: %v", err)
	}
	if err := certs.Match(pair.cert, other.key); err == nil {
		t.Error("a key from a different certificate was accepted")
	}
}

// TestMatchRejectsRubbishClearly, rather than reporting a mismatch for input
// that was never a certificate.
func TestMatchRejectsRubbishClearly(t *testing.T) {
	t.Parallel()
	pair := selfSigned(t)
	for _, tc := range []struct{ cert, key, want string }{
		{"not pem", pair.key, "not PEM"},
		{pair.cert, "not pem", "not PEM"},
	} {
		err := certs.Match(tc.cert, tc.key)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("err = %v, want it to mention %q", err, tc.want)
		}
	}
}

type pemPair struct{ cert, key string }

// selfSigned makes a throwaway certificate and its key, so Match can be tested
// without a certificate authority.
func selfSigned(t *testing.T) pemPair {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pemPair{
		cert: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		key:  string(pem.EncodeToMemory(&pem.Block{Type: certs.BlockPrivateKey, Bytes: keyDER})),
	}
}

// TestMatchAcceptsAllThreeKeyEncodings verifies the claim parsePrivateKey's
// comment makes.
//
// Which encoding a certificate authority hands back is not the caller's
// choice, so "accepts all three" is a capability this package advertises, and
// an advertised capability nobody exercises is a claim rather than a fact. Two
// of the three branches were unreached until this test existed.
func TestMatchAcceptsAllThreeKeyEncodings(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkcs1 := x509.MarshalPKCS1PrivateKey(rsaKey)
	sec1, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8EC, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		encoding string
		key      crypto.Signer
		der      []byte
		block    string
	}{
		{"PKCS#1", rsaKey, pkcs1, "RSA PRIVATE KEY"},
		{"SEC 1", ecKey, sec1, "EC PRIVATE KEY"},
		// The ECDSA half of PKCS#8: the RSA half is already covered, and the
		// type switch inside Match is what differs between them.
		{"PKCS#8 (ECDSA)", ecKey, pkcs8EC, certs.BlockPrivateKey},
	} {
		t.Run(tc.encoding, func(t *testing.T) {
			t.Parallel()
			certPEM := certificateFor(t, tc.key)
			keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: tc.block, Bytes: tc.der}))
			if err := certs.Match(certPEM, keyPEM); err != nil {
				t.Errorf("a %s key was rejected: %v", tc.encoding, err)
			}
		})
	}
}

// TestMatchRejectsAKeyItCannotParse. The failure has to name the encodings,
// because the caller's next move is to re-export the key in one of them.
func TestMatchRejectsAKeyItCannotParse(t *testing.T) {
	t.Parallel()
	pair := selfSigned(t)
	garbage := string(pem.EncodeToMemory(&pem.Block{
		Type: certs.BlockPrivateKey, Bytes: []byte("this is not a key"),
	}))

	err := certs.Match(pair.cert, garbage)
	if err == nil {
		t.Fatal("a key that is not a key was accepted")
	}
	for _, want := range []string{"PKCS#8", "PKCS#1", "SEC 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v; want it to name %s so the caller knows what to export", err, want)
		}
	}
}

// TestMatchRejectsACertificateThatIsNotOne. PEM framing is not proof of
// content: a key file relabelled CERTIFICATE decodes as PEM and fails only
// when parsed.
func TestMatchRejectsACertificateThatIsNotOne(t *testing.T) {
	t.Parallel()
	pair := selfSigned(t)
	notACert := string(pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: []byte("nonsense"),
	}))

	err := certs.Match(notACert, pair.key)
	if err == nil || !strings.Contains(err.Error(), "parsing the certificate") {
		t.Errorf("err = %v; want the parse failure named", err)
	}
}

// TestNewRefusesAnEmptyHostname. An empty entry would become an empty SAN,
// producing a certificate that is valid, useless, and issued: the authority
// signs it, the wait for issuance succeeds, and it matches nothing.
func TestNewRefusesAnEmptyHostname(t *testing.T) {
	t.Parallel()
	_, err := certs.New([]string{"example.com", ""}, 2048)
	if err == nil {
		t.Fatal("an empty hostname was accepted")
	}
	if !strings.Contains(err.Error(), "not a hostname") {
		t.Errorf("err = %v; want it to say which input was wrong", err)
	}
}

// certificateFor issues a throwaway self-signed certificate for a key, so
// Match has something to compare a public key against.
func certificateFor(t *testing.T, key crypto.Signer) string {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
