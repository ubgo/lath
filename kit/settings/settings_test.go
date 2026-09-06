package settings_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/settings"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "deploy.env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadReadsTheSmallDialect. KEY=VALUE, comments, optional quotes, optional
// export. Not a shell: no interpolation, because a settings file that can
// execute is one that can surprise.
func TestLoadReadsTheSmallDialect(t *testing.T) {
	s, err := settings.Load(write(t, `
# a comment
HOST=example.com
  PADDED  =  spaces are trimmed
QUOTED="quoted value"
SINGLE='single'
export EXPORTED=yes
EMPTY=
INNER=a"b
not a setting
`))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"HOST": "example.com", "PADDED": "spaces are trimmed",
		"QUOTED": "quoted value", "SINGLE": "single", "EXPORTED": "yes",
		"EMPTY": "", "INNER": `a"b`,
	} {
		if got := s.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// TestMissingFileIsNotAnError. The file is a convenience; a caller supplying
// everything through the environment should not be made to create an empty
// one.
func TestMissingFileIsNotAnError(t *testing.T) {
	s, err := settings.Load(filepath.Join(t.TempDir(), "absent.env"))
	if err != nil {
		t.Fatalf("a missing file was an error: %v", err)
	}
	t.Setenv("FROM_ENV", "value")
	if got := s.Get("FROM_ENV"); got != "value" {
		t.Errorf("Get = %q, want the environment to still work", got)
	}
}

// TestPresentEnvironmentWinsIncludingEmpty is the rule that makes a file
// value clearable. Treating empty as absent leaves a setting no caller can
// switch off without editing a file others share.
func TestPresentEnvironmentWinsIncludingEmpty(t *testing.T) {
	s, err := settings.Load(write(t, "DOMAIN=example.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Get("DOMAIN"); got != "example.com" {
		t.Fatalf("file value = %q", got)
	}
	t.Setenv("DOMAIN", "")
	if got := s.Get("DOMAIN"); got != "" {
		t.Errorf("an explicitly empty environment variable did not clear the file value: %q", got)
	}
	t.Setenv("DOMAIN", "other.example")
	if got := s.Get("DOMAIN"); got != "other.example" {
		t.Errorf("Get = %q, want the environment", got)
	}
}

// TestRequireNamesEveryMissingKey. Discovering three missing values one
// attempt at a time is three round trips through an image build.
func TestRequireNamesEveryMissingKey(t *testing.T) {
	s, err := settings.Load(write(t, "PRESENT=yes\nBLANK=\n"))
	if err != nil {
		t.Fatal(err)
	}
	err = s.Require("PRESENT", "BLANK", "ABSENT")
	if err == nil {
		t.Fatal("missing settings were accepted")
	}
	for _, want := range []string{"BLANK", "ABSENT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "PRESENT") {
		t.Errorf("error names a key that was set: %v", err)
	}
	// The message must say which file to edit, or it is a puzzle.
	if !strings.Contains(err.Error(), s.Path()) {
		t.Errorf("error does not name the file: %v", err)
	}
	if err := s.Require("PRESENT"); err != nil {
		t.Errorf("a satisfied Require failed: %v", err)
	}
}

// TestIntFallsBackRatherThanFailing, because the values this is used for have
// correct defaults and failing over one is noise.
func TestIntFallsBackRatherThanFailing(t *testing.T) {
	s, err := settings.Load(write(t, "PORT=2222\nNOT_A_PORT=ssh\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Int("PORT", 22); got != 2222 {
		t.Errorf("PORT = %d", got)
	}
	if got := s.Int("NOT_A_PORT", 22); got != 22 {
		t.Errorf("unparseable = %d, want the fallback", got)
	}
	if got := s.Int("ABSENT", 22); got != 22 {
		t.Errorf("absent = %d, want the fallback", got)
	}
}

// TestLoadRejectsANamelessValue rather than storing it under "" where nothing
// can ever ask for it.
func TestLoadRejectsANamelessValue(t *testing.T) {
	_, err := settings.Load(write(t, "=orphan\n"))
	if err == nil {
		t.Fatal("a value with no name was accepted")
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("err = %v, want the line number", err)
	}
}

// TestFromWrapsValuesSomeoneElseParsed is what keeps this package small: a
// project with a real .env parser, one that does $(command) substitution and
// interpolation, uses it and gets the override and Require behaviour here.
func TestFromWrapsValuesSomeoneElseParsed(t *testing.T) {
	s := settings.From(map[string]string{"TOKEN": "from-a-real-parser"}, "/path/to/deploy.env")

	if got := s.Get("TOKEN"); got != "from-a-real-parser" {
		t.Errorf("Get = %q", got)
	}
	// The environment still wins, which is the point of wrapping at all.
	t.Setenv("TOKEN", "from-env")
	if got := s.Get("TOKEN"); got != "from-env" {
		t.Errorf("Get = %q, want the environment", got)
	}
	// And errors still name the file to edit.
	err := s.Require("ABSENT")
	if err == nil || !strings.Contains(err.Error(), "/path/to/deploy.env") {
		t.Errorf("err = %v", err)
	}
}

// TestFromCopiesTheMap, so a caller mutating its own map later cannot change
// settings that were already read.
func TestFromCopiesTheMap(t *testing.T) {
	given := map[string]string{"TOKEN": "original"}
	s := settings.From(given, "deploy.env")
	given["TOKEN"] = "changed"
	if got := s.Get("TOKEN"); got != "original" {
		t.Errorf("Get = %q, want the value as it was when From was called", got)
	}
}
