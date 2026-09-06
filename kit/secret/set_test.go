package secret_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/secret"
)

func TestSetOrdersByKey(t *testing.T) {
	t.Parallel()
	s := secret.NewSet()
	for _, k := range []string{"ZED", "alpha", "MIDDLE", "BETA"} {
		s.Put(k, secret.New("v"))
	}
	// Stable ordering is what stops every regeneration looking like a change.
	got := fmt.Sprint(s.Keys())
	if want := "[BETA MIDDLE ZED alpha]"; got != want {
		t.Errorf("Keys() = %s; want %s", got, want)
	}
	for range 5 {
		if fmt.Sprint(s.Keys()) != got {
			t.Fatal("Keys() is not stable across calls")
		}
	}
}

func TestSetBasics(t *testing.T) {
	t.Parallel()
	s := secret.NewSet()
	if s.Len() != 0 || s.Has("x") || s.String() != "0 secrets" {
		t.Errorf("a new Set is not empty: %d %v %q", s.Len(), s.Has("x"), s.String())
	}
	s.Put("A", secret.New("one"))
	if s.String() != "1 secret" {
		t.Errorf("String() = %q; want the singular", s.String())
	}
	s.Put("A", secret.New("replaced"))
	if s.Len() != 1 {
		t.Errorf("Len() = %d after replacing a key; want 1", s.Len())
	}
	v, ok := s.Get("A")
	if !ok || v.Reveal() != "replaced" {
		t.Errorf("Get() = %q, %v; want the replacement", v.Reveal(), ok)
	}
	if _, ok := s.Get("absent"); ok {
		t.Error("Get reported an absent key as present")
	}
}

// TestSetZeroValueIsUsable pins that a Set need not be constructed, a struct
// field of this type works without initialisation.
func TestSetZeroValueIsUsable(t *testing.T) {
	t.Parallel()
	var s secret.Set
	s.Put("A", secret.New("x"))
	if s.Len() != 1 {
		t.Errorf("Len() = %d; the zero Set must accept a Put", s.Len())
	}
}

// TestSetNeverDisclosesInItsOwnRenderings pins that the group-level output is
// as safe as the individual values.
func TestSetNeverDisclosesInItsOwnRenderings(t *testing.T) {
	t.Parallel()
	s := secret.NewSet()
	s.Put("TOKEN", secret.New(theSecret))

	if strings.Contains(s.String(), theSecret) {
		t.Error("String() disclosed the plaintext")
	}
	// Names are withheld too: a secret's name often says what it opens.
	if strings.Contains(s.String(), "TOKEN") {
		t.Errorf("String() = %q; it should report a count only", s.String())
	}
	summary := strings.Join(s.Summary(), "\n")
	if strings.Contains(summary, theSecret) {
		t.Error("Summary() disclosed the plaintext")
	}
	if !strings.Contains(summary, "TOKEN") || !strings.Contains(summary, fmt.Sprint(len(theSecret))) {
		t.Errorf("Summary() = %q; want the name and the byte count", summary)
	}
}

// TestWriteFileRefusesAReadableMode is the guard that matters: the tool this
// replaces wrote every secret at 0644 for a year.
func TestWriteFileRefusesAReadableMode(t *testing.T) {
	t.Parallel()
	s := secret.NewSet()
	s.Put("TOKEN", secret.New(theSecret))
	dir := t.TempDir()

	for _, perm := range []os.FileMode{0o644, 0o640, 0o604, 0o666, 0o777, 0o660} {
		path := filepath.Join(dir, fmt.Sprintf("m%04o", perm))
		err := s.WriteFile(path, perm)
		if !errors.Is(err, secret.ErrPermTooOpen) {
			t.Errorf("WriteFile at %#o = %v; want ErrPermTooOpen", perm, err)
		}
		if _, statErr := os.Stat(path); statErr == nil {
			t.Errorf("WriteFile at %#o refused but still created the file", perm)
		}
	}
	// Owner-only modes are accepted.
	for _, perm := range []os.FileMode{0o600, 0o400, 0o200} {
		path := filepath.Join(dir, fmt.Sprintf("ok%04o", perm))
		if err := s.WriteFile(path, perm); err != nil {
			t.Errorf("WriteFile at %#o = %v; want success", perm, err)
		}
	}
}

func TestWriteFileContents(t *testing.T) {
	t.Parallel()
	s := secret.NewSet()
	s.Put("B_SECOND", secret.New("two"))
	s.Put("A_FIRST", secret.New("one"))
	// A PEM key: newlines must survive as one line, or the file is unparseable.
	s.Put("KEY", secret.New("-----BEGIN-----\nline two\n-----END-----\n"))

	path := filepath.Join(t.TempDir(), "out")
	if err := s.WriteFile(path, 0o600, "generated, do not edit", "do not commit"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)

	if !strings.HasPrefix(got, "# generated, do not edit\n# do not commit\n\n") {
		t.Errorf("header missing or malformed:\n%s", got)
	}
	if i, j := strings.Index(got, "A_FIRST"), strings.Index(got, "B_SECOND"); i > j {
		t.Error("entries are not in key order")
	}
	// One line per entry, however many newlines the value contains.
	body := got[strings.Index(got, "A_FIRST"):]
	if n := strings.Count(strings.TrimRight(body, "\n"), "\n"); n != 2 {
		t.Errorf("expected 3 entry lines, found %d newlines:\n%s", n+1, body)
	}
	if !strings.Contains(got, `KEY="-----BEGIN-----\nline two\n-----END-----\n"`) {
		t.Errorf("the multi-line value was not escaped onto one line:\n%s", got)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v; want 0600", info.Mode().Perm())
		}
	}
}

func TestWriteFileEmptySet(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "empty")
	if err := secret.NewSet().WriteFile(path, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 0 {
		t.Errorf("an empty Set wrote %q", b)
	}
}
