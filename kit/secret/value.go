package secret

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"os"

	"log/slog"

	"github.com/ubgo/lath/kit/env"
)

// redactedUnset is what an empty Value renders as. Distinguished from a
// populated one because "the variable was never set" and "the secret is 40
// bytes" are different problems, and a single placeholder for both hides the
// more common one.
const redactedUnset = "«secret unset»"

// Value holds a credential.
//
// The plaintext is unreachable through fmt, encoding/json, encoding.TextMarshaler
// and log/slog. Reveal is the only door.
//
// Invariant: the zero Value is valid and empty, IsZero reports true, Len is 0,
// and every rendering says "unset". A struct field of this type therefore needs
// no initialisation to be safe.
//
// Copying is safe and cheap; Value is immutable after construction.
type Value struct {
	// plaintext is unexported so no reflective walk outside this package can
	// reach it, and so the only paths out are the ones defined below.
	plaintext string
}

// New wraps a plaintext credential.
func New(plaintext string) Value { return Value{plaintext: plaintext} }

// FromEnv reads a credential from the environment, failing when it is unset or
// blank.
//
// Wraps env.Require rather than os.Getenv so a variable set to whitespace ,
// what a shell produces from an unset lookup, is treated as missing here too.
func FromEnv(name string) (Value, error) {
	v, err := env.Require(name)
	if err != nil {
		return Value{}, err
	}
	return Value{plaintext: v}, nil
}

// FromFile reads a credential from a file.
//
// Why it exists rather than leaving callers to os.ReadFile: the bytes never
// pass through a variable of a printable type, so there is no intermediate
// value for a debug print to find.
func FromFile(path string) (Value, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Value{}, fmt.Errorf("secret: reading %s: %w", path, err)
	}
	return Value{plaintext: string(b)}, nil
}

// Reveal returns the plaintext.
//
// Named to be greppable: every place a credential escapes this package can be
// found by searching for one word, which is what makes an audit tractable.
// Call it as late as possible and never assign the result to a longer-lived
// variable than the call needs.
func (v Value) Reveal() string { return v.plaintext }

// Len reports the plaintext's length in bytes. Safe to log, and often enough to
// spot a variable that was set to the empty string or to a shell expansion that
// did not expand.
func (v Value) Len() int { return len(v.plaintext) }

// IsZero reports whether the Value carries nothing.
func (v Value) IsZero() bool { return v.plaintext == "" }

// Equal reports whether two credentials match, in constant time.
//
// It exists less for the timing property than for what its absence would cause:
// without it, comparing two credentials means Reveal on both, which puts two
// plaintext strings into local variables and adds two entries to the audit that
// is meant to stay short enough to read. Constant-time comparison is then free,
// and correct if this package is ever used somewhere an attacker can measure.
func (v Value) Equal(other Value) bool {
	return subtle.ConstantTimeCompare([]byte(v.plaintext), []byte(other.plaintext)) == 1
}

// ── the containment surface ──────────────────────────────────────────────
//
// Each method below closes a path by which the plaintext would otherwise
// escape. They must ALL be present: one gap and the guarantee is theatre,
// because the disclosure will happen through whichever path was forgotten.

// String renders the Value for humans without disclosing it.
func (v Value) String() string {
	if v.IsZero() {
		return redactedUnset
	}
	return fmt.Sprintf("«secret %d bytes»", len(v.plaintext))
}

// GoString covers %#v, which does not route through String and would otherwise
// print the struct field verbatim.
func (v Value) GoString() string { return v.String() }

// Format covers every verb, %s, %v, %q, %x, %d and anything else, because
// implementing String alone leaves %q and %x printing the plaintext.
//
// Width and precision flags are deliberately ignored: honouring a precision
// would let a caller print a prefix of the secret one character at a time.
func (v Value) Format(f fmt.State, verb rune) {
	_, _ = f.Write([]byte(v.String()))
}

// MarshalJSON always redacts, and there is no path through this package that
// makes it emit plaintext.
//
// Decided rather than defaulted. Redacting silently would let a task that
// generates a credentials file by marshalling a struct write the placeholder
// where a credential belongs, failing days later far from the cause, but only
// if a plaintext path existed to be forgotten. With encoding unable to disclose
// anything, dumping a config for a log is unconditionally safe, and writing a
// real credentials file has to be done deliberately through Reveal, which is
// the right shape for producing live credentials on disk.
func (v Value) MarshalJSON() ([]byte, error) { return json.Marshal(v.String()) }

// MarshalText covers encoding.TextMarshaler, which json, yaml and text
// templates reach for before falling back to String.
func (v Value) MarshalText() ([]byte, error) { return []byte(v.String()), nil }

// UnmarshalJSON is permitted: reading a credential out of a config file is the
// normal way one arrives. Only the outbound direction is closed.
func (v *Value) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("secret: %w", err)
	}
	v.plaintext = s
	return nil
}

// UnmarshalText mirrors UnmarshalJSON for the text-based decoders.
func (v *Value) UnmarshalText(b []byte) error {
	v.plaintext = string(b)
	return nil
}

// LogValue covers log/slog, which would otherwise reflect over the struct.
func (v Value) LogValue() slog.Value { return slog.StringValue(v.String()) }

// redactAll is used by Set.Summary and the Sync reporting, so every place that
// describes a credential in this package goes through one implementation.
func redactAll(keys []string, get func(string) Value) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s (%d bytes)", k, get(k).Len()))
	}
	return out
}
