package checksum_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/checksum"
)

// artifacts writes throwaway files and returns their paths.
func artifacts(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return dir, paths
}

// TestTheManifestIsWhatSha256sumReads is the reason the format is not
// negotiable. Someone who downloads an artifact must be able to check it with
// the tool already on their machine, with nothing from this project installed.
//
// Skipped where no such tool exists rather than asserted from memory: the
// claim is "the system tool accepts this", and only the system tool can say so.
func TestTheManifestIsWhatSha256sumReads(t *testing.T) {
	t.Parallel()
	tool := ""
	for _, candidate := range []string{"sha256sum", "shasum"} {
		if path, err := exec.LookPath(candidate); err == nil {
			tool = path
			break
		}
	}
	if tool == "" {
		t.Skip("no sha256sum or shasum on this machine")
	}

	dir, paths := artifacts(t, map[string]string{
		"app_darwin_arm64.tar.gz": "the darwin build",
		"app_linux_amd64.tar.gz":  "the linux build",
	})
	if _, err := checksum.Write(dir, paths); err != nil {
		t.Fatal(err)
	}

	args := []string{"-c", checksum.DefaultName}
	if strings.HasSuffix(tool, "shasum") {
		args = append([]string{"-a", "256"}, args...)
	}
	cmd := exec.Command(tool, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%s rejected our manifest: %v\n%s", filepath.Base(tool), err, out)
	}
}

// TestCorruptionIsCaught. The whole point: a byte changes and verification
// fails, naming the file.
func TestCorruptionIsCaught(t *testing.T) {
	t.Parallel()
	dir, paths := artifacts(t, map[string]string{"app.tar.gz": "the real build"})
	manifest, err := checksum.Manifest(paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := checksum.Verify(dir, manifest); err != nil {
		t.Fatalf("a good file failed: %v", err)
	}

	if err := os.WriteFile(paths[0], []byte("a substituted build"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = checksum.Verify(dir, manifest)
	if !errors.Is(err, checksum.ErrMismatch) {
		t.Fatalf("err = %v; want ErrMismatch", err)
	}
	if !strings.Contains(err.Error(), "app.tar.gz") {
		t.Errorf("err = %v; want the file named", err)
	}
}

// TestAListedFileThatIsAbsentIsAFailure. A manifest describing artifacts that
// were never uploaded is the exact failure this exists to catch; skipping the
// missing one would report a broken release as a good one.
func TestAListedFileThatIsAbsentIsAFailure(t *testing.T) {
	t.Parallel()
	dir, paths := artifacts(t, map[string]string{"app.tar.gz": "build"})
	manifest, err := checksum.Manifest(paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths[0]); err != nil {
		t.Fatal(err)
	}

	if err := checksum.Verify(dir, manifest); err == nil {
		t.Fatal("a manifest listing a file that does not exist verified")
	}
}

// TestNamesAreBasenames. A manifest is consumed where the files were
// DOWNLOADED, not where they were built; a leaked build path turns
// verification into a puzzle about someone else's filesystem.
func TestNamesAreBasenames(t *testing.T) {
	t.Parallel()
	_, paths := artifacts(t, map[string]string{"app.tar.gz": "build"})
	entries, err := checksum.Manifest(paths)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Name != "app.tar.gz" {
		t.Errorf("Name = %q, want the basename", entries[0].Name)
	}
	if strings.Contains(checksum.Render(entries), string(os.PathSeparator)) {
		t.Errorf("a path leaked into the manifest:\n%s", checksum.Render(entries))
	}
}

// TestOrderIsStable. Manifests are committed, attached to releases and diffed;
// filesystem order would make two runs over the same files differ, and a diff
// full of reordering hides the one line that changed.
func TestOrderIsStable(t *testing.T) {
	t.Parallel()
	_, paths := artifacts(t, map[string]string{
		"z.tar.gz": "z", "a.tar.gz": "a", "m.tar.gz": "m",
	})
	first, err := checksum.Manifest(paths)
	if err != nil {
		t.Fatal(err)
	}
	// Reversed input, identical output.
	for i, j := 0, len(paths)-1; i < j; i, j = i+1, j-1 {
		paths[i], paths[j] = paths[j], paths[i]
	}
	second, err := checksum.Manifest(paths)
	if err != nil {
		t.Fatal(err)
	}
	if checksum.Render(first) != checksum.Render(second) {
		t.Errorf("input order changed the manifest:\n%s\n%s",
			checksum.Render(first), checksum.Render(second))
	}
}

// TestParseAcceptsWhatOtherToolsWrite. A manifest written by shasum -b marks
// names with '*' for "binary mode", which is meaningless here but common in
// the wild; refusing it would make this unable to read half the manifests it
// meets.
func TestParseAcceptsWhatOtherToolsWrite(t *testing.T) {
	t.Parallel()
	const digest = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	entries, err := checksum.Parse(strings.NewReader(
		digest + "  plain.tar.gz\n" +
			"\n" + // blank lines are skipped
			digest + " *binary.tar.gz\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("parsed %d entries, want 2: %+v", len(entries), entries)
	}
	if entries[1].Name != "binary.tar.gz" {
		t.Errorf("Name = %q; want the binary marker stripped", entries[1].Name)
	}
}

// TestParseRefusesWhatItCannotUnderstand. A manifest that parses to fewer
// entries than it has lines is a verification that silently checks less than
// it claims.
func TestParseRefusesWhatItCannotUnderstand(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]string{
		"no name":            "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08\n",
		"not a digest":       "not-a-digest  app.tar.gz\n",
		"truncated digest":   "9f86d08  app.tar.gz\n",
		"prose from a human": "these are the checksums\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := checksum.Parse(strings.NewReader(input)); err == nil {
				t.Errorf("%q parsed as a manifest", input)
			}
		})
	}
}

// TestRoundTrip. Render and Parse are used at opposite ends of a release, one
// on the machine that built the artifact and one on the machine that fetched
// it, so a disagreement between them is invisible until a user reports it.
func TestRoundTrip(t *testing.T) {
	t.Parallel()
	_, paths := artifacts(t, map[string]string{"a.tar.gz": "a", "b.zip": "b"})
	original, err := checksum.Manifest(paths)
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := checksum.Parse(strings.NewReader(checksum.Render(original)))
	if err != nil {
		t.Fatal(err)
	}
	if checksum.Render(parsed) != checksum.Render(original) {
		t.Errorf("round trip changed the manifest:\n%s\n%s",
			checksum.Render(original), checksum.Render(parsed))
	}
}

// TestVerifyFileChecksOneArtifact. A downloader has one file and a whole
// manifest: running the full verification would fail on every artifact it did
// not download.
func TestVerifyFileChecksOneArtifact(t *testing.T) {
	t.Parallel()
	dir, paths := artifacts(t, map[string]string{"a.tar.gz": "a", "b.tar.gz": "b"})
	manifest, err := checksum.Manifest(paths)
	if err != nil {
		t.Fatal(err)
	}

	if err := checksum.VerifyFile(filepath.Join(dir, "a.tar.gz"), manifest); err != nil {
		t.Errorf("a good artifact failed: %v", err)
	}
	// An artifact nobody listed is a different problem from a corrupt one, and
	// a caller retries only one of them.
	stranger := filepath.Join(dir, "unlisted.tar.gz")
	if err := os.WriteFile(stranger, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checksum.VerifyFile(stranger, manifest); !errors.Is(err, checksum.ErrNotListed) {
		t.Errorf("err = %v; want ErrNotListed", err)
	}
}
