package secret_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/secret"
)

// TestReportFailedKeys, Sync deliberately continues past a failure so one bad
// key does not hide the other nineteen, which only works if the Report can say
// which ones failed.
func TestReportFailedKeys(t *testing.T) {
	t.Parallel()
	desired := secret.NewSet()
	for _, k := range []string{"GOOD_ONE", "BAD_ONE", "BAD_TWO"} {
		desired.Put(k, secret.New("v"))
	}
	store := &failingStore{fail: map[string]bool{"BAD_ONE": true, "BAD_TWO": true}}

	report, err := secret.Sync(context.Background(), store, desired)
	if err == nil {
		t.Fatal("a sync with failures reported success")
	}
	failed := report.FailedKeys()
	if len(failed) != 2 {
		t.Fatalf("FailedKeys = %v, want both failures", failed)
	}
	sort.Strings(failed)
	if failed[0] != "BAD_ONE" || failed[1] != "BAD_TWO" {
		t.Errorf("FailedKeys = %v", failed)
	}
	// The good key must still have been attempted, that is the point of
	// continuing.
	if !store.set["GOOD_ONE"] {
		t.Error("a failure stopped the sync; the remaining keys were skipped")
	}
	if report.Accounted() != 3 {
		t.Errorf("Accounted = %d, want every key", report.Accounted())
	}
}

// failingStore accepts every key except the ones named.
type failingStore struct {
	fail map[string]bool
	set  map[string]bool
}

func (f *failingStore) List(context.Context) ([]string, error) { return nil, nil }
func (f *failingStore) Set(_ context.Context, key string, _ secret.Value) error {
	if f.fail[key] {
		return errors.New("store refused " + key)
	}
	if f.set == nil {
		f.set = map[string]bool{}
	}
	f.set[key] = true
	return nil
}
func (f *failingStore) Delete(context.Context, string) error { return nil }

// Reserved refuses nothing: this store exists to test failure handling, not
// naming rules.
func (f *failingStore) Reserved(string) bool { return false }

// TestValueUnmarshalText covers the decoding half of the containment surface.
// A secret must survive a round trip through a config file without its
// plaintext becoming reachable by any other path.
func TestValueUnmarshalText(t *testing.T) {
	t.Parallel()
	var v secret.Value
	if err := v.UnmarshalText([]byte("ghp_token")); err != nil {
		t.Fatal(err)
	}
	if v.Reveal() != "ghp_token" {
		t.Errorf("Reveal = %q", v.Reveal())
	}
	// And it is still contained: the only door out is Reveal.
	if strings.Contains(v.String(), "ghp_token") {
		t.Errorf("String() disclosed the value: %q", v.String())
	}
	if out, err := v.MarshalText(); err == nil && strings.Contains(string(out), "ghp_token") {
		t.Errorf("MarshalText disclosed the value: %q", out)
	}
}
