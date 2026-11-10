// Package argv splits a command's arguments into positionals and flags, and
// refuses a flag the command does not know.
//
// Why it exists: a definition's target receives its arguments as plain
// strings (`func Deploy(ctx context.Context, args ...string) error`), and the
// natural way to read a flag from them is slices.Contains(args, "--apply").
// That reads a misspelt flag as absent rather than wrong, so `--aply` runs the
// dry run the operator did not ask for, and `--prebuit` quietly takes the
// path the flag existed to avoid. Parse makes the misspelling an error that
// names the flag it probably meant.
//
// Deliberately small. It is not a flag library: no types, no defaults, no
// help text, no short-flag bundling. A target that wants those can import
// the standard library's flag package. This answers the one question every
// target has and gets wrong by default: is this argument one I accept?
package argv

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnknownFlag reports an argument that looks like a flag and is not one
// the command accepts.
var ErrUnknownFlag = errors.New("unknown flag")

// endOfFlags ends flag parsing: every argument after it is positional, so a
// positional that starts with a dash can still be passed.
const endOfFlags = "--"

// valueSeparator joins a flag to its value (--tag=abc1234), and marks a known
// flag as one that takes a value ("--tag=").
const valueSeparator = "="

// Args is a parsed argument list.
type Args struct {
	positional []string
	flags      map[string]string
	// known maps every declared flag to whether it takes a value, so Has and
	// Value can refuse a name the target never declared.
	known map[string]bool
}

// Parse separates args into positionals and flags, accepting only the flags
// in known.
//
// known is spelt the way a user types the flags, dashes included. A flag that
// takes a value is declared with a trailing "=" ("--tag="), and must then be
// given as --tag=value; every other flag is a switch and must be given bare.
// Both mistakes are refused rather than guessed at:
//
//   - "--tag abc1234 prod" would otherwise read abc1234 as the first
//     positional, which a target takes for the environment;
//   - "--apply=false" would otherwise read as --apply, and apply.
//
// An argument is a flag when it starts with "-" and is not exactly "-" (which
// conventionally names stdin). Everything after a bare "--" is positional.
// Repeating a flag is allowed and the last value wins, as with most CLIs.
//
// A known entry that is not a flag is a programming error in the target, and
// is reported as such rather than silently never matching.
func Parse(args []string, known ...string) (Args, error) {
	a := Args{flags: map[string]string{}, known: map[string]bool{}}
	var names []string
	for _, k := range known {
		name, takesValue := strings.CutSuffix(k, valueSeparator)
		if !isFlag(name) || strings.Contains(name, valueSeparator) {
			return Args{}, fmt.Errorf("argv: known flag %q must start with - (and end in = if it takes a value)", k)
		}
		a.known[name] = takesValue
		names = append(names, name)
	}
	for i, arg := range args {
		if arg == endOfFlags {
			a.positional = append(a.positional, args[i+1:]...)
			break
		}
		if !isFlag(arg) {
			a.positional = append(a.positional, arg)
			continue
		}
		name, value, hasValue := strings.Cut(arg, valueSeparator)
		takesValue, ok := a.known[name]
		switch {
		case !ok:
			return Args{}, unknown(name, names)
		case takesValue && !hasValue:
			return Args{}, fmt.Errorf("argv: %s takes a value; give it as %s=VALUE", name, name)
		case !takesValue && hasValue:
			return Args{}, fmt.Errorf("argv: %s takes no value; leave it out to turn it off", name)
		}
		a.flags[name] = value
	}
	return a, nil
}

// Positional returns the arguments that are not flags, in order.
func (a Args) Positional() []string { return append([]string(nil), a.positional...) }

// Has reports whether flag was given.
//
// It panics on a flag that was not declared to Parse: asking about one is a
// misspelling in the target's own code, and answering false would bring back
// the silent mistake this package exists to remove.
func (a Args) Has(flag string) bool {
	a.mustKnow(flag)
	_, ok := a.flags[flag]
	return ok
}

// Value returns the value given as flag=value, and whether the flag was
// given. It panics on an undeclared flag, as Has does.
func (a Args) Value(flag string) (string, bool) {
	a.mustKnow(flag)
	v, ok := a.flags[flag]
	return v, ok
}

func (a Args) mustKnow(flag string) {
	if _, ok := a.known[flag]; !ok {
		panic(fmt.Sprintf("argv: %q was not declared to Parse", flag))
	}
}

func isFlag(s string) bool { return len(s) > 1 && s[0] == '-' }

// maxSuggestDistance bounds how different a known flag may be from the one
// typed and still be offered as what was meant. Two edits cover a dropped,
// doubled or swapped letter; more than that suggests a flag the user was not
// thinking of, which is worse than no suggestion.
const maxSuggestDistance = 2

// unknown builds the refusal: the flag, the closest known one when it is
// close, and the full list, so the user can fix the command without reading
// the target's source.
func unknown(name string, known []string) error {
	var suggestion string
	best := maxSuggestDistance + 1
	for _, k := range known {
		if d := distance(name, k); d < best {
			best, suggestion = d, k
		}
	}
	want := "none"
	if len(known) > 0 {
		want = strings.Join(known, ", ")
	}
	if suggestion != "" {
		return fmt.Errorf("%w %q (did you mean %q? accepted: %s)", ErrUnknownFlag, name, suggestion, want)
	}
	return fmt.Errorf("%w %q (accepted: %s)", ErrUnknownFlag, name, want)
}

// distance is the Levenshtein distance between a and b, over bytes: flags are
// ASCII, and a byte-wise answer for anything else is still a fair "how far".
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
