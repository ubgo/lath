package secret_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/ubgo/lath/kit/secret"
)

// theSecret is distinctive enough that any leak through any path is
// unambiguous when searched for.
const theSecret = "ghp_pl4int3xt_must_never_appear_anywhere"

// TestNoStandardPathDisclosesThePlaintext is the test the package exists for.
//
// Every entry is a real way a credential escapes in production: a debug print,
// a struct dump, a JSON config written to disk, a structured log line, a
// rendered template. One gap makes the whole type theatre, so they are checked
// together rather than scattered. A new path added to the type must be added
// here, and a path removed will fail loudly.
func TestNoStandardPathDisclosesThePlaintext(t *testing.T) {
	t.Parallel()
	v := secret.New(theSecret)

	type config struct {
		Host  string
		Token secret.Value
	}
	cfg := config{Host: "db.internal", Token: v}

	jsonBytes, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshalling a struct containing a secret must succeed: %v", err)
	}
	indented, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuf, nil)).Info("starting", "config", cfg, "token", v)

	var tmplBuf bytes.Buffer
	if err := template.Must(template.New("t").Parse("{{.Token}}|{{.Host}}")).
		Execute(&tmplBuf, cfg); err != nil {
		t.Fatal(err)
	}

	text, err := v.MarshalText()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, got string }{
		{"%v", fmt.Sprintf("%v", v)},
		{"%s", fmt.Sprintf("%s", v)},
		{"%q", fmt.Sprintf("%q", v)},
		{"%x", fmt.Sprintf("%x", v)},
		{"%X", fmt.Sprintf("%X", v)},
		{"%d", fmt.Sprintf("%d", v)},
		{"%#v", fmt.Sprintf("%#v", v)},
		{"%+v", fmt.Sprintf("%+v", v)},
		{"%.5s truncated verb", fmt.Sprintf("%.5s", v)},
		{"String()", v.String()},
		{"GoString()", v.GoString()},
		{"print", fmt.Sprint(v)},
		{"println", fmt.Sprintln(v)},
		{"inside a struct, %v", fmt.Sprintf("%v", cfg)},
		{"inside a struct, %+v", fmt.Sprintf("%+v", cfg)},
		{"inside a struct, %#v", fmt.Sprintf("%#v", cfg)},
		{"inside a slice", fmt.Sprintf("%v", []secret.Value{v})},
		{"inside a map", fmt.Sprintf("%v", map[string]secret.Value{"k": v})},
		{"as a pointer", fmt.Sprintf("%v", &v)},
		{"json.Marshal", string(jsonBytes)},
		{"json.MarshalIndent", string(indented)},
		{"MarshalText", string(text)},
		{"slog JSON handler", logBuf.String()},
		{"text/template", tmplBuf.String()},
		{"errors.New wrapping", fmt.Errorf("failed for %v", v).Error()},
	} {
		if strings.Contains(tc.got, theSecret) {
			t.Errorf("%s DISCLOSED the plaintext: %s", tc.name, tc.got)
		}
		if tc.got == "" {
			t.Errorf("%s produced nothing; a redaction must still say something", tc.name)
		}
	}
}

// TestRevealIsTheOnlyDoor pins the other half: the value must still be usable.
// A type that cannot yield its secret is safe and useless.
func TestRevealIsTheOnlyDoor(t *testing.T) {
	t.Parallel()
	if got := secret.New(theSecret).Reveal(); got != theSecret {
		t.Errorf("Reveal() = %q; want the plaintext back verbatim", got)
	}
}

// TestRedactionNamesTheSize pins that the placeholder carries the length,
// which is what distinguishes "set to an empty string" from "40 bytes".
func TestRedactionNamesTheSize(t *testing.T) {
	t.Parallel()
	got := secret.New("0123456789").String()
	if !strings.Contains(got, "10") {
		t.Errorf("String() = %q; want it to report the byte count", got)
	}
}

// TestZeroValueIsSafeAndDistinct pins that a struct field needs no
// initialisation, and that "unset" reads differently from a populated secret.
func TestZeroValueIsSafeAndDistinct(t *testing.T) {
	t.Parallel()
	var zero secret.Value

	if !zero.IsZero() || zero.Len() != 0 || zero.Reveal() != "" {
		t.Errorf("the zero Value is not empty: %v %d %q", zero.IsZero(), zero.Len(), zero.Reveal())
	}
	if got := zero.String(); !strings.Contains(got, "unset") {
		t.Errorf("zero String() = %q; want it to say unset", got)
	}
	// The distinction that matters: an unset variable and a real secret must
	// not render identically, or a missing credential looks configured.
	if zero.String() == secret.New("x").String() {
		t.Error("an unset Value renders the same as a populated one")
	}
	if _, err := json.Marshal(zero); err != nil {
		t.Errorf("marshalling the zero Value failed: %v", err)
	}
}

func TestEqualIsCorrect(t *testing.T) {
	t.Parallel()
	a, b, c := secret.New("same"), secret.New("same"), secret.New("different")
	if !a.Equal(b) {
		t.Error("identical secrets compared unequal")
	}
	if a.Equal(c) {
		t.Error("different secrets compared equal")
	}
	// Length differences must not panic the constant-time comparison.
	if a.Equal(secret.New("")) || secret.New("").Equal(a) {
		t.Error("an empty secret compared equal to a populated one")
	}
	var zero secret.Value
	if !zero.Equal(secret.New("")) {
		t.Error("the zero Value and an empty secret must compare equal")
	}
}

// TestRoundTripThroughJSON pins that a credential can be read from config ,
// the inbound direction stays open by design.
func TestRoundTripThroughJSON(t *testing.T) {
	t.Parallel()
	type config struct {
		Token secret.Value `json:"token"`
	}
	var cfg config
	if err := json.Unmarshal([]byte(`{"token":"`+theSecret+`"}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Token.Reveal() != theSecret {
		t.Errorf("Reveal() = %q; want the value from the config", cfg.Token.Reveal())
	}
	// And re-marshalling it must NOT put the plaintext back on disk.
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), theSecret) {
		t.Errorf("re-marshalling disclosed the plaintext: %s", out)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("LATH_SECRET_TEST", theSecret)
	v, err := secret.FromEnv("LATH_SECRET_TEST")
	if err != nil {
		t.Fatal(err)
	}
	if v.Reveal() != theSecret {
		t.Errorf("Reveal() = %q", v.Reveal())
	}

	// Blank is missing, matching env.Require. A variable set to whitespace is
	// what a shell produces from an unset lookup.
	t.Setenv("LATH_SECRET_TEST", "   ")
	if _, err := secret.FromEnv("LATH_SECRET_TEST"); err == nil {
		t.Error("a whitespace-only variable was accepted as a credential")
	}
	if _, err := secret.FromEnv("LATH_SECRET_DEFINITELY_UNSET_x9"); err == nil {
		t.Error("an unset variable was accepted")
	}
}

func TestFromFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(path, []byte(theSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := secret.FromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if v.Reveal() != theSecret {
		t.Errorf("Reveal() = %q", v.Reveal())
	}

	_, err = secret.FromFile(filepath.Join(dir, "missing"))
	if err == nil {
		t.Fatal("reading a missing file succeeded")
	}
	// The error must name the path AND must not be able to contain content.
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("err = %v; want it to name the file", err)
	}
}
