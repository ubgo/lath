package secret

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ubgo/lath/kit/fsx"
)

// maxGroupReadablePerm is the loosest mode a credentials file may carry.
// Anything with a group or other bit set is refused.
const maxGroupReadablePerm os.FileMode = 0o600

// ErrPermTooOpen reports an attempt to write credentials at a mode other users
// could read.
//
// Refused rather than warned about, because the failure it prevents is silent
// and long-lived: the tool this package replaces wrote every secret in
// plaintext at 0644 for a year without anyone noticing.
var ErrPermTooOpen = errors.New("secret: refusing to write credentials at a readable mode")

// Set is a named group of credentials.
//
// Invariant: iteration is ordered by key. Stable ordering matters more than it
// looks. A rendered file and a push sequence both derive from it, and an
// unstable order makes every regeneration look like a change.
type Set struct {
	values map[string]Value
}

// NewSet returns an empty Set, ready to use.
func NewSet() *Set { return &Set{values: make(map[string]Value)} }

// Put stores a credential, replacing any existing entry under the same key.
func (s *Set) Put(key string, v Value) {
	if s.values == nil {
		s.values = make(map[string]Value)
	}
	s.values[key] = v
}

// Get returns a credential and whether it was present.
func (s *Set) Get(key string) (Value, bool) {
	v, ok := s.values[key]
	return v, ok
}

// Has reports whether a key is present.
func (s *Set) Has(key string) bool { _, ok := s.values[key]; return ok }

// Keys returns the names in sorted order. See the ordering invariant on Set.
func (s *Set) Keys() []string {
	out := make([]string, 0, len(s.values))
	for k := range s.values {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Len reports how many credentials the Set holds.
func (s *Set) Len() int { return len(s.values) }

// String reports the count only, never a name, never a value.
//
// Names are withheld as well as values because a secret's name often discloses
// what system it opens, which is more than a log line needs.
func (s *Set) String() string {
	if s.Len() == 1 {
		return "1 secret"
	}
	return fmt.Sprintf("%d secrets", s.Len())
}

// Summary renders one redacted line per credential, in key order. This is the
// most detailed rendering the package offers, and it is redacted by
// construction rather than by the caller remembering.
func (s *Set) Summary() []string {
	return redactAll(s.Keys(), func(k string) Value { return s.values[k] })
}

// WriteFile renders the Set as a dotenv-shaped file at the given mode.
//
// Written atomically, so a reader sees the old file or the new one and never a
// half-written credential. Refuses any mode readable beyond the owner, see
// ErrPermTooOpen.
//
// Values are quoted with %q, which escapes newlines and quotes, so a multi-line
// credential such as a PEM key round-trips as one line.
func (s *Set) WriteFile(path string, perm os.FileMode, header ...string) error {
	if perm&^maxGroupReadablePerm != 0 {
		return fmt.Errorf("secret: %s at %#o: %w", path, perm, ErrPermTooOpen)
	}

	var buf strings.Builder
	for _, line := range header {
		fmt.Fprintf(&buf, "# %s\n", line)
	}
	if len(header) > 0 {
		buf.WriteString("\n")
	}
	for _, k := range s.Keys() {
		fmt.Fprintf(&buf, "%s=%q\n", k, s.values[k].Reveal())
	}

	if err := fsx.WriteAtomic(path, []byte(buf.String()), perm); err != nil {
		return err
	}
	return nil
}
