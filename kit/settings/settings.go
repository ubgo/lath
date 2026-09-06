// Package settings reads deploy-time configuration from a file that the
// environment can override.
//
// The shape every deployment ends up needing: values live in a file so a
// laptop deploy needs no ceremony, and any of them can be overridden by an
// environment variable so CI needs no file. Neither half is interesting on its
// own; the combination, and reporting what is missing before the work starts,
// is what this owns.
//
// Standard library only, and a deliberately small dialect: KEY=VALUE, # for
// comments, optional quotes, optional `export`. It is NOT a shell parser and
// does no interpolation, because a settings file that can execute is a
// settings file that can surprise.
package settings

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Set is settings loaded from one file, with the environment layered over it.
type Set struct {
	// file is what the file said. Values from the environment are NOT copied
	// in here: they are read at Get time, so a Set cannot go stale against the
	// environment it is asked about.
	file map[string]string
	// path is remembered for error messages, which are the only thing that
	// tells a reader which file to edit.
	path string
}

// From wraps values a caller has already parsed.
//
// The reason this exists: the dialect below is deliberately small, and a real
// .env parser does more, notably `$(command)` substitution, which is how a
// credential stays in a password manager rather than in the file. A caller
// with such a parser, and any project that already depends on one, should use
// it and hand the result here, keeping only what this package is actually
// good at: the environment override and reporting every missing key at once.
//
// path is used in error messages, so a reader knows which file to edit.
func From(values map[string]string, path string) Set {
	copied := make(map[string]string, len(values))
	for k, v := range values {
		copied[k] = v
	}
	return Set{file: copied, path: path}
}

// Load reads path.
//
// A MISSING FILE IS NOT AN ERROR. The file is a convenience; a caller that
// supplies everything through the environment should not be made to create an
// empty one. What is or is not required is Require's business, and it reports
// it by name rather than by filename.
func Load(path string) (Set, error) {
	s := Set{file: map[string]string{}, path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("settings: reading %s: %w", path, err)
	}
	for n, line := range strings.Split(string(raw), "\n") {
		key, value, ok := parse(line)
		if !ok {
			continue
		}
		if key == "" {
			return s, fmt.Errorf("settings: %s line %d: a value with no name", path, n+1)
		}
		s.file[key] = value
	}
	return s, nil
}

// parse reads one line, reporting whether it held a setting at all.
func parse(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	key, value, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	return strings.TrimSpace(key), unquote(strings.TrimSpace(value)), true
}

// unquote strips one matching pair of quotes.
//
// Only a pair, and only the outermost: a value that happens to contain quotes
// keeps them, because a settings file is not a shell and half-stripping is
// worse than not stripping.
func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// Get returns a setting, empty when it is set nowhere.
//
// The environment wins when the variable is PRESENT, which includes present
// and empty. That is the only way to switch off something the file turns on,
// and a caller that wanted "empty means absent" can compare to "" itself.
func (s Set) Get(key string) string {
	if v, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(v)
	}
	return s.file[key]
}

// Int returns a setting as an integer, or fallback when it is unset or is not
// a number.
//
// Unparseable falls back rather than failing, because the values this is used
// for, a port, a timeout, have correct defaults, and failing a deploy over a
// value that has one is noise.
func (s Set) Int(key string, fallback int) int {
	n, err := strconv.Atoi(s.Get(key))
	if err != nil {
		return fallback
	}
	return n
}

// Has reports whether a key is set anywhere and is not empty.
func (s Set) Has(key string) bool { return s.Get(key) != "" }

// Require reports EVERY missing key at once.
//
// All of them, not the first: discovering three missing values one attempt at
// a time is three round trips through whatever slow thing follows, which for a
// deploy is an image build.
func (s Set) Require(keys ...string) error {
	var missing []string
	for _, key := range keys {
		if !s.Has(key) {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("missing setting(s) %s: set them in %s or in the environment",
		strings.Join(missing, ", "), s.path)
}

// Path is the file this was loaded from, for a caller writing its own message.
func (s Set) Path() string { return s.path }
