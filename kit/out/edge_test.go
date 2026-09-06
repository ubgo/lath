package out_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ubgo/lath/kit/out"
)

// failingWriter fails after letting a fixed number of writes through, so a
// mid-line failure can be observed.
type failingWriter struct {
	allow int
	err   error
}

func (f *failingWriter) Write(b []byte) (int, error) {
	if f.allow <= 0 {
		return 0, f.err
	}
	f.allow--
	return len(b), nil
}

// ── Prefix ───────────────────────────────────────────────────────────────

// TestPrefixAcrossWriteBoundaries pins the case the implementation exists for:
// a line arriving in several Writes gets ONE prefix, not one per Write.
//
// It matters because that is exactly how a subprocess's output arrives, in
// pipe-sized chunks with no relationship to line boundaries, so a naive
// implementation looks correct in tests that write whole lines and produces
// "[dev] par[dev] tial" against a real process.
func TestPrefixAcrossWriteBoundaries(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := out.Prefix(&buf, "[dev] ")
	for _, chunk := range []string{"hel", "lo wor", "ld\nsec", "ond\n"} {
		if _, err := io.WriteString(w, chunk); err != nil {
			t.Fatal(err)
		}
	}
	want := "[dev] hello world\n[dev] second\n"
	if buf.String() != want {
		t.Errorf("got %q; want %q", buf.String(), want)
	}
}

func TestPrefixEdgeShapes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		prefix string
		writes []string
		want   string
	}{
		{"a single line", "> ", []string{"one\n"}, "> one\n"},
		// No trailing newline: the line is still prefixed, and no phantom
		// newline is invented.
		{"no trailing newline", "> ", []string{"partial"}, "> partial"},
		// A blank line is a line and gets a prefix, dropping it would silently
		// reflow a subprocess's output.
		{"blank lines", "> ", []string{"\n\n"}, "> \n> \n"},
		{"leading blank line", "> ", []string{"\nafter\n"}, "> \n> after\n"},
		{"consecutive newlines mid-text", "> ", []string{"a\n\nb\n"}, "> a\n> \n> b\n"},
		// Nothing written means nothing emitted, no lone prefix.
		{"an empty write", "> ", []string{""}, ""},
		{"no writes at all", "> ", nil, ""},
		{"an empty prefix", "", []string{"a\nb\n"}, "a\nb\n"},
		{"carriage returns are not line ends", "> ", []string{"a\r\nb\r\n"}, "> a\r\n> b\r\n"},
		{"resumed after a partial line", "> ", []string{"par", "tial\n"}, "> partial\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			w := out.Prefix(&buf, tc.prefix)
			for _, s := range tc.writes {
				if _, err := io.WriteString(w, s); err != nil {
					t.Fatal(err)
				}
			}
			if buf.String() != tc.want {
				t.Errorf("got %q; want %q", buf.String(), tc.want)
			}
		})
	}
}

// TestPrefixReportsTheFullCountOnSuccess pins the io.Writer contract: a
// successful Write reports the caller's byte count, not the larger number
// actually sent downstream. io.Copy treats n != len(b) as a short write.
func TestPrefixReportsTheFullCountOnSuccess(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := out.Prefix(&buf, "[a-long-prefix] ")
	input := []byte("one\ntwo\nthree\n")
	n, err := w.Write(input)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(input) {
		t.Errorf("Write returned %d for %d bytes in; a caller reads that as a short write", n, len(input))
	}
}

// TestPrefixPropagatesErrors pins that a downstream failure is reported rather
// than swallowed. A log that silently stops is worse than one that errors.
func TestPrefixPropagatesErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("disk full")
	for _, allow := range []int{0, 1, 2} {
		w := out.Prefix(&failingWriter{allow: allow, err: sentinel}, "> ")
		_, err := io.WriteString(w, "one\ntwo\nthree\n")
		if !errors.Is(err, sentinel) {
			t.Errorf("with %d writes allowed: err = %v; want the downstream error", allow, err)
		}
	}
}

// TestPrefixWorksThroughMultiWriter pins the composition the dev watchdog
// uses, prefix into both a terminal and a log file.
func TestPrefixWorksThroughMultiWriter(t *testing.T) {
	t.Parallel()
	var a, b bytes.Buffer
	w := out.Prefix(io.MultiWriter(&a, &b), "[x] ")
	if _, err := io.WriteString(w, "line\n"); err != nil {
		t.Fatal(err)
	}
	if a.String() != "[x] line\n" || b.String() != a.String() {
		t.Errorf("a = %q, b = %q", a.String(), b.String())
	}
}

// ── LogFile ──────────────────────────────────────────────────────────────

// TestLogFileCreatesMissingParents pins that a caller need not mkdir first ,
// the first run of a task on a fresh checkout has no log directory.
func TestLogFileCreatesMissingParents(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "deep", "nested", "dev.log")
	f, err := out.LogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(f, "hello\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Errorf("content = %q", got)
	}
}

// TestLogFileAppends pins that reopening does not truncate. Truncating would
// destroy the history of a crash loop at the moment it is most wanted.
func TestLogFileAppends(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "dev.log")
	for _, line := range []string{"first\n", "second\n", "third\n"} {
		f, err := out.LogFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, line); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first\nsecond\nthird\n" {
		t.Errorf("content = %q; a reopen truncated the log", got)
	}
}

// TestLogFileConcurrentAppends pins that two writers do not interleave WITHIN
// a line. O_APPEND makes each write atomic, which is what keeps a supervisor
// and its child from corrupting each other's lines.
func TestLogFileConcurrentAppends(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "concurrent.log")
	f, err := out.LogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const writers, perWriter = 8, 50
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			line := strings.Repeat(string(rune('a'+i)), 64) + "\n"
			for range perWriter {
				if _, err := io.WriteString(f, line); err != nil {
					t.Errorf("writer %d: %v", i, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if len(lines) != writers*perWriter {
		t.Errorf("got %d lines; want %d", len(lines), writers*perWriter)
	}
	for i, l := range lines {
		if len(l) != 64 || strings.Count(l, string(l[0])) != 64 {
			t.Fatalf("line %d is interleaved: %q", i, l)
		}
	}
}

func TestLogFileUnwritablePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The parent of the log path is a regular file, so MkdirAll must fail.
	if _, err := out.LogFile(filepath.Join(blocker, "sub", "x.log")); err == nil {
		t.Error("opening a log beneath a regular file succeeded")
	}
}

// TestLogFileCloseReportsPath pins that a close failure names the file. A bare
// "file already closed" in a deploy log is unactionable.
func TestLogFileCloseReportsPath(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "x.log")
	f, err := out.LogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	err = f.Close()
	if err == nil {
		t.Fatal("a second Close reported success")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("err = %v; want it to name %s", err, path)
	}
}

// TestLogFileOntoADirectory pins the open failure that survives MkdirAll:
// the parent can be created and the target still be unopenable.
func TestLogFileOntoADirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "iam-a-directory")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := out.LogFile(target); err == nil {
		t.Fatal("opening a directory as a log file succeeded")
	} else if !strings.Contains(err.Error(), target) {
		t.Errorf("err = %v; want it to name the path", err)
	}
}

// TestPrefixPropagatesErrorOnAnUnterminatedLine pins the branch a failure hits
// when the chunk has no newline. The shape a crashing process produces as its
// last partial write, which is the output most worth not losing silently.
func TestPrefixPropagatesErrorOnAnUnterminatedLine(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("pipe closed")
	// allow=1 lets the prefix through, so the failure lands on the content.
	w := out.Prefix(&failingWriter{allow: 1, err: sentinel}, "> ")
	n, err := io.WriteString(w, "a partial line with no newline")
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v; want the downstream error", err)
	}
	if n != 0 {
		t.Errorf("n = %d; a failed write must not claim bytes were accepted", n)
	}
}
