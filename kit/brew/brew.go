// Package brew renders a Homebrew formula for a released binary.
//
// Rendering only. Where the formula is committed, which tap it belongs to,
// what the description says and which versions are worth publishing are
// decisions about a project; the shape Homebrew demands is not, and getting it
// wrong is a failure a user discovers at `brew install` rather than at
// release. That shape is what this package owns.
//
// The output is a "binary formula": it downloads a prebuilt archive per
// platform and installs the executable, which is what a Go CLI ships. It does
// not model formulas that build from source, have dependencies, or install
// services. Those exist, they are a different shape, and pretending one
// template covers them would produce plausible files that do not work.
//
// To publish, render and commit — the composition is eight lines with kit/git:
//
//	text, err := formula.Render()
//	repo, _ := git.Clone(ctx, nil, tapURL, dir)
//	os.WriteFile(filepath.Join(dir, "Formula", formula.Name+".rb"), []byte(text), 0o644)
//	repo.Commit(ctx, "volt 1.2.0", "Formula/volt.rb")
//	repo.Push(ctx)
package brew

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// The platforms a formula can carry, named because Homebrew's DSL calls them
// something else than Go does and the translation is this package's job.
//
// Homebrew nests `on_macos`/`on_linux` around `on_arm`/`on_intel`; Go says
// darwin/linux and arm64/amd64. A caller writes what Go writes, since that is
// what its build matrix already produced.
const (
	OSMacOS = "darwin"
	OSLinux = "linux"

	ArchARM64 = "arm64"
	ArchAMD64 = "amd64"
)

// OSValues and ArchValues are the canonical lists, so a caller can check its
// build matrix against what a formula can express from one place.
var (
	OSValues   = []string{OSMacOS, OSLinux}
	ArchValues = []string{ArchARM64, ArchAMD64}
)

// homebrewOS and homebrewArch translate to the DSL's own words.
var (
	homebrewOS   = map[string]string{OSMacOS: "on_macos", OSLinux: "on_linux"}
	homebrewArch = map[string]string{ArchARM64: "on_arm", ArchAMD64: "on_intel"}
)

// Platform is one downloadable build.
//
// SHA256 is required and not defaulted: an empty digest renders a formula
// Homebrew accepts and then fails to verify, which is worse than a formula
// that does not render at all.
type Platform struct {
	// OS is a Go GOOS value; see OSValues.
	OS string
	// Arch is a Go GOARCH value; see ArchValues.
	Arch string
	// URL is where the archive for this platform can be downloaded.
	URL string
	// SHA256 is the archive's hex digest, as kit/checksum computes it.
	SHA256 string
}

// Formula is everything needed to render one.
type Formula struct {
	// Name is the formula's name, which is also what a user types to install
	// it. Lower case, hyphens allowed.
	Name string
	// Description is Homebrew's `desc`. One line, no trailing period, and
	// conventionally not starting with the formula's own name; `brew audit`
	// enforces all three.
	Description string
	// Homepage is the project's URL. Required by `brew audit`.
	Homepage string
	// Version is the released version WITHOUT a leading "v". Homebrew's
	// version comparison is semver-ish and a "v" makes every comparison
	// wrong, which shows up as `brew upgrade` refusing to see a new release.
	Version string
	// License is an SPDX identifier. Optional, omitted when empty.
	License string
	// Binary is the executable to install from inside the archive. Empty
	// means Name, which is the usual case.
	Binary string
	// Platforms are the builds this version ships. At least one is required.
	Platforms []Platform
	// TestCommand is the arguments `brew test` runs, after the binary itself.
	// Empty means {"--version"}.
	//
	// A test block is not optional in practice: `brew audit --strict` fails
	// without one, and a formula that installs a binary nobody ever executed
	// is how a broken archive ships.
	TestCommand []string
}

// ClassName is the Ruby class Homebrew expects for a formula name.
//
// Homebrew derives it by capitalising each hyphen-separated word and joining
// them: "volt" is Volt, "my-cli" is MyCli. A class named anything else loads
// as a different formula than the file claims to be, and the error names
// neither. Exported because a caller writing the file path usually wants the
// class name too, to grep for it or to check an existing tap entry.
func ClassName(name string) string {
	var b strings.Builder
	for _, word := range strings.FieldsFunc(name, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	}) {
		runes := []rune(strings.ToLower(word))
		runes[0] = unicode.ToUpper(runes[0])
		b.WriteString(string(runes))
	}
	return b.String()
}

// Validate reports what would make Homebrew reject the formula, all at once.
//
// Together rather than one at a time: a caller fixing a release configuration
// wants the whole list, not a round trip per field.
func (f Formula) Validate() error {
	var problems []string
	if f.Name == "" {
		problems = append(problems, "Name is required")
	}
	if f.Version == "" {
		problems = append(problems, "Version is required")
	}
	if strings.HasPrefix(f.Version, "v") {
		// Refused rather than trimmed: a caller passing "v1.2.0" believes
		// that is the version, and silently changing it means the formula and
		// the caller disagree about what shipped.
		problems = append(problems, fmt.Sprintf(
			"Version %q must not start with \"v\"; Homebrew compares versions and a \"v\" makes every comparison wrong", f.Version))
	}
	if f.Homepage == "" {
		problems = append(problems, "Homepage is required (brew audit enforces it)")
	}
	if strings.HasSuffix(f.Description, ".") {
		problems = append(problems, "Description must not end with a period (brew audit enforces it)")
	}
	if len(f.Platforms) == 0 {
		problems = append(problems, "at least one Platform is required")
	}
	seen := map[string]bool{}
	for i, p := range f.Platforms {
		where := fmt.Sprintf("Platforms[%d]", i)
		if homebrewOS[p.OS] == "" {
			problems = append(problems, fmt.Sprintf("%s: OS %q is not one of %v", where, p.OS, OSValues))
		}
		if homebrewArch[p.Arch] == "" {
			problems = append(problems, fmt.Sprintf("%s: Arch %q is not one of %v", where, p.Arch, ArchValues))
		}
		if p.URL == "" {
			problems = append(problems, where+": URL is required")
		}
		if p.SHA256 == "" {
			problems = append(problems, where+": SHA256 is required; an empty digest renders a formula that installs unverified bytes")
		}
		key := p.OS + "/" + p.Arch
		if seen[key] {
			problems = append(problems, fmt.Sprintf("%s: %s appears twice; the second would silently win", where, key))
		}
		seen[key] = true
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("brew: %s", strings.Join(problems, "; "))
}

// Render produces the formula's Ruby source.
//
// Platforms are emitted in a fixed order (macOS before Linux, arm before
// intel) regardless of the order they were given: the formula is committed to
// a tap and diffed on every release, and a diff full of reordering hides the
// two lines that actually changed.
func (f Formula) Render() (string, error) {
	if err := f.Validate(); err != nil {
		return "", err
	}
	binary := f.Binary
	if binary == "" {
		binary = f.Name
	}
	test := f.TestCommand
	if len(test) == 0 {
		test = []string{"--version"}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by lath. Edits are overwritten by the next release.\n")
	fmt.Fprintf(&b, "class %s < Formula\n", ClassName(f.Name))
	if f.Description != "" {
		fmt.Fprintf(&b, "  desc %q\n", f.Description)
	}
	fmt.Fprintf(&b, "  homepage %q\n", f.Homepage)
	fmt.Fprintf(&b, "  version %q\n", f.Version)
	if f.License != "" {
		fmt.Fprintf(&b, "  license %q\n", f.License)
	}

	for _, os := range OSValues {
		platforms := f.byOS(os)
		if len(platforms) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n  %s do\n", homebrewOS[os])
		for _, p := range platforms {
			fmt.Fprintf(&b, "    %s do\n", homebrewArch[p.Arch])
			fmt.Fprintf(&b, "      url %q\n", p.URL)
			fmt.Fprintf(&b, "      sha256 %q\n", p.SHA256)
			fmt.Fprintf(&b, "    end\n")
		}
		fmt.Fprintf(&b, "  end\n")
	}

	fmt.Fprintf(&b, "\n  def install\n    bin.install %q\n  end\n", binary)
	fmt.Fprintf(&b, "\n  test do\n    system \"#{bin}/%s\"", binary)
	for _, arg := range test {
		fmt.Fprintf(&b, ", %q", arg)
	}
	fmt.Fprintf(&b, "\n  end\nend\n")
	return b.String(), nil
}

// byOS returns one OS's platforms in ArchValues order.
func (f Formula) byOS(os string) []Platform {
	var out []Platform
	for _, p := range f.Platforms {
		if p.OS == os {
			out = append(out, p)
		}
	}
	rank := map[string]int{}
	for i, a := range ArchValues {
		rank[a] = i
	}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Arch] < rank[out[j].Arch] })
	return out
}

// Path is where a formula file belongs inside a tap checkout.
//
// Taps keep formulas in a Formula/ directory; Homebrew also accepts the tap
// root, but every tap generated by anything puts them in Formula/, and a file
// in the wrong place is a tap that installs nothing with no error to explain
// it.
func (f Formula) Path() string { return "Formula/" + f.Name + ".rb" }
