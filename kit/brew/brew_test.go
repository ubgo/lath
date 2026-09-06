package brew_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/brew"
)

// full is a formula with every platform a Go CLI usually ships.
func full() brew.Formula {
	return brew.Formula{
		Name:        "volt",
		Description: "Build, release and ship Go CLIs",
		Homepage:    "https://github.com/acme/tools",
		Version:     "0.1.0",
		License:     "MIT",
		Platforms: []brew.Platform{
			{OS: brew.OSLinux, Arch: brew.ArchAMD64, URL: "https://example.com/l_amd64.tar.gz", SHA256: "cccc"},
			{OS: brew.OSMacOS, Arch: brew.ArchARM64, URL: "https://example.com/d_arm64.tar.gz", SHA256: "aaaa"},
			{OS: brew.OSMacOS, Arch: brew.ArchAMD64, URL: "https://example.com/d_amd64.tar.gz", SHA256: "bbbb"},
		},
	}
}

// TestRenderedFormulaIsValidRuby. The shape is the whole product of this
// package, and a syntax error reaches a user at `brew install`, never at
// release. Skipped where ruby is absent rather than asserted from memory: only
// a Ruby parser can make this claim.
func TestRenderedFormulaIsValidRuby(t *testing.T) {
	t.Parallel()
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("no ruby on this machine")
	}
	text, err := full().Render()
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(ruby, "-c") // syntax check only, nothing is executed
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("ruby rejected the formula: %v\n%s\n%s", err, out, text)
	}
}

// TestTheDSLNestingIsWhatHomebrewExpects. Homebrew's words are not Go's, and
// the translation is this package's job: on_macos/on_linux wrapping
// on_arm/on_intel, with url and sha256 inside.
func TestTheDSLNestingIsWhatHomebrewExpects(t *testing.T) {
	t.Parallel()
	text, err := full().Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"class Volt < Formula", "on_macos do", "on_linux do", "on_arm do", "on_intel do",
		`url "https://example.com/d_arm64.tar.gz"`, `sha256 "aaaa"`,
		`bin.install "volt"`, `system "#{bin}/volt", "--version"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the formula is missing %q:\n%s", want, text)
		}
	}
	// Go's own words must not leak into the DSL.
	for _, forbidden := range []string{"darwin", "amd64", "arm64"} {
		if strings.Contains(text, forbidden) && !strings.Contains(text, "https://example.com") {
			t.Errorf("%q reached the formula; Homebrew does not know that word", forbidden)
		}
	}
}

// TestOrderIsFixed. The formula is committed to a tap and diffed on every
// release; emitting platforms in call order would fill a diff with reordering
// and hide the two lines that changed.
func TestOrderIsFixed(t *testing.T) {
	t.Parallel()
	f := full()
	first, err := f.Render()
	if err != nil {
		t.Fatal(err)
	}
	// Same platforms, reversed.
	for i, j := 0, len(f.Platforms)-1; i < j; i, j = i+1, j-1 {
		f.Platforms[i], f.Platforms[j] = f.Platforms[j], f.Platforms[i]
	}
	second, err := f.Render()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("input order changed the formula:\n%s\n---\n%s", first, second)
	}
	if strings.Index(first, "on_macos") > strings.Index(first, "on_linux") {
		t.Error("macOS must come first, per the documented order")
	}
}

// TestClassNameFollowsHomebrewsRule. A class named anything else loads as a
// different formula than the file claims to be, and the error names neither.
func TestClassNameFollowsHomebrewsRule(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"volt":       "Volt",
		"my-cli":     "MyCli",
		"acme-cli":   "AcmeCli",
		"foo_bar":    "FooBar",
		"volt.tools": "VoltTools",
	} {
		if got := brew.ClassName(name); got != want {
			t.Errorf("ClassName(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestValidateReportsEverythingAtOnce. A caller fixing a release
// configuration wants the whole list, not a round trip per field.
func TestValidateReportsEverythingAtOnce(t *testing.T) {
	t.Parallel()
	err := brew.Formula{
		Description: "Does things.",
		Version:     "v1.2.0",
		Platforms:   []brew.Platform{{OS: "plan9", Arch: "sparc", URL: ""}},
	}.Validate()
	if err == nil {
		t.Fatal("an unusable formula validated")
	}
	for _, want := range []string{"Name", "Homepage", "period", "\"v\"", "plan9", "sparc", "URL", "SHA256"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err does not mention %q:\n%v", want, err)
		}
	}
}

// TestAVersionWithAVIsRefusedNotTrimmed. Homebrew compares versions, and a "v"
// makes every comparison wrong — `brew upgrade` simply stops seeing new
// releases. Trimming silently would leave the caller and the formula
// disagreeing about what shipped.
func TestAVersionWithAVIsRefusedNotTrimmed(t *testing.T) {
	t.Parallel()
	f := full()
	f.Version = "v0.1.0"
	if _, err := f.Render(); err == nil {
		t.Fatal("a v-prefixed version rendered")
	}
}

// TestAMissingDigestIsRefused. An empty sha256 renders a formula Homebrew
// accepts and then fails to verify: worse than one that does not render.
func TestAMissingDigestIsRefused(t *testing.T) {
	t.Parallel()
	f := full()
	f.Platforms[0].SHA256 = ""
	if _, err := f.Render(); err == nil {
		t.Fatal("a formula with no digest rendered")
	}
}

// TestDuplicatePlatformIsRefused. Ruby would take the second silently, so the
// artifact a user downloads would not be the one the release meant to ship.
func TestDuplicatePlatformIsRefused(t *testing.T) {
	t.Parallel()
	f := full()
	f.Platforms = append(f.Platforms, brew.Platform{
		OS: brew.OSMacOS, Arch: brew.ArchARM64, URL: "https://example.com/other.tar.gz", SHA256: "dddd",
	})
	if _, err := f.Render(); err == nil {
		t.Fatal("a duplicated platform rendered")
	}
}

// TestBinaryAndTestCommandDefaultAndOverride. The defaults cover the usual Go
// CLI; the overrides exist for an archive whose executable is not named after
// the formula, and for a binary whose version flag is not --version.
func TestBinaryAndTestCommandDefaultAndOverride(t *testing.T) {
	t.Parallel()
	f := full()
	f.Binary = "voltcli"
	f.TestCommand = []string{"version", "--json"}
	text, err := f.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `bin.install "voltcli"`) {
		t.Errorf("the binary override was ignored:\n%s", text)
	}
	if !strings.Contains(text, `system "#{bin}/voltcli", "version", "--json"`) {
		t.Errorf("the test override was ignored:\n%s", text)
	}
}

// TestPathIsWhereTapsKeepFormulas. A file in the wrong place is a tap that
// installs nothing, with no error explaining why.
func TestPathIsWhereTapsKeepFormulas(t *testing.T) {
	t.Parallel()
	if got := full().Path(); got != "Formula/volt.rb" {
		t.Errorf("Path() = %q", got)
	}
}
