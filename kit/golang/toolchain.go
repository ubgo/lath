package golang

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// The files and variables the go command itself reads to choose a toolchain.
// WIRE FORMATS: defined by the go command, not chosen here.
const (
	workFile  = "go.work"
	modFile   = "go.mod"
	workEnv   = "GOWORK"
	workOff   = "off"
	directive = "toolchain"
	goLine    = "go"
	// toolchainDefault is the go.work spelling of "no suggestion", which pins
	// nothing.
	toolchainDefault = "default"
)

// releaseVersion is a go line exact enough to name a release: 1.26.4, or a
// pre-release such as 1.27rc1. "1.26" alone is a language version and names
// no toolchain the go command could download.
var releaseVersion = regexp.MustCompile(`^1\.[0-9]+(\.[0-9]+|rc[0-9]+)$`)

// ReadToolchain returns the GOTOOLCHAIN value that pins a build in dir to the
// toolchain its workspace or module names.
//
// Why pin at all: under the default GOTOOLCHAIN=auto the go command uses the
// NEWER of the installed Go and the one the files name, so a laptop with a
// newer Go than CI builds a different binary from the same commit. Pinning
// makes the files the only answer.
//
// The file is found the way the go command finds it: GOWORK when set (and
// "off" skips the workspace), otherwise the nearest go.work at or above dir,
// otherwise the nearest go.mod. Its toolchain line wins; without one, a go
// line that names a complete release (go 1.26.4) is the implied toolchain,
// which is what the go command does with it. Anything less exact is
// ErrNoToolchain, with the command that fixes it.
func ReadToolchain(dir string) (string, error) {
	gowork, _ := os.LookupEnv(workEnv)
	return readToolchain(dir, gowork)
}

// PinnedToolchain is the GOTOOLCHAIN value Build runs under: Toolchain when
// set, otherwise ReadToolchain's answer for dir as the BUILD will see it, so
// a GOWORK given in Env decides which file is read, exactly as it decides
// which file the go command reads. Reading lath's own GOWORK instead would
// pin the workspace's Go for a build that runs without the workspace.
func (o BuildOptions) PinnedToolchain(dir string) (string, error) {
	if o.Toolchain != "" {
		return o.Toolchain, nil
	}
	gowork, _ := os.LookupEnv(workEnv)
	for _, kv := range o.Env {
		if v, ok := strings.CutPrefix(kv, workEnv+"="); ok {
			gowork = v
		}
	}
	return readToolchain(dir, gowork)
}

func readToolchain(dir, gowork string) (string, error) {
	file, err := governingFile(dir, gowork)
	if err != nil {
		return "", err
	}
	f, err := os.Open(file)
	if err != nil {
		return "", fmt.Errorf("golang: reading %s: %w", file, err)
	}
	defer func() { _ = f.Close() }() // read-only: a close error loses nothing

	var toolchain, version string
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := scan.Text()
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case directive:
			toolchain = fields[1]
		case goLine:
			version = fields[1]
		}
	}
	if err := scan.Err(); err != nil {
		return "", fmt.Errorf("golang: reading %s: %w", file, err)
	}
	implied := ""
	if releaseVersion.MatchString(version) {
		implied = "go" + version
	}
	switch {
	case toolchain != "" && toolchain != toolchainDefault:
		// The go command runs the newer of the two, so a toolchain line older
		// than a complete go line would pin a Go that refuses the module.
		if implied != "" && olderThan(toolchain, implied) {
			return implied, nil
		}
		return toolchain, nil
	case implied != "":
		return implied, nil
	}
	edit := "go mod edit"
	if filepath.Base(file) == workFile {
		edit = "go work edit"
	}
	return "", fmt.Errorf("golang: %w: %s has no toolchain line and its go line (%q) names no release; "+
		"add one with `%s -toolchain=go1.N.P`, or set Toolchain", ErrNoToolchain, file, version, edit)
}

// olderThan compares two toolchain names of the form go1.N.P (pre-releases
// sort before their release). Names it cannot read compare as not older, so
// the explicit toolchain line stands.
func olderThan(a, b string) bool {
	pa, okA := toolchainParts(a)
	pb, okB := toolchainParts(b)
	if !okA || !okB {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

var toolchainName = regexp.MustCompile(`^go1\.([0-9]+)(?:\.([0-9]+)|rc([0-9]+))?(?:-.*)?$`)

// toolchainParts reads go1.N.P as [N, 1, P] and go1.NrcR as [N, 0, R], so a
// release sorts after its release candidates.
func toolchainParts(name string) ([3]int, bool) {
	m := toolchainName.FindStringSubmatch(name)
	if m == nil {
		return [3]int{}, false
	}
	minor, _ := strconv.Atoi(m[1])
	if m[3] != "" {
		rc, _ := strconv.Atoi(m[3])
		return [3]int{minor, 0, rc}, true
	}
	patch, _ := strconv.Atoi(m[2])
	return [3]int{minor, 1, patch}, true
}

// governingFile is the go.work or go.mod the go command would read in dir.
func governingFile(dir, gowork string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("golang: %w", err)
	}
	switch work := gowork; {
	case work == workOff:
	case work != "":
		if !filepath.IsAbs(work) {
			// The go command refuses a relative GOWORK; resolving it here
			// would pin a build the go command will not run.
			return "", fmt.Errorf("golang: GOWORK=%q must be an absolute path, as the go command requires", work)
		}
		return work, nil
	default:
		if found, ok := findUp(abs, workFile); ok {
			return found, nil
		}
	}
	if found, ok := findUp(abs, modFile); ok {
		return found, nil
	}
	return "", fmt.Errorf("golang: %w: no %s or %s at or above %s", ErrNoToolchain, workFile, modFile, abs)
}

func findUp(dir, name string) (string, bool) {
	for {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
