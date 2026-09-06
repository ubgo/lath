package out_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/out"
)

func TestLogFileAppendsAndCreatesDirs(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "deep", "nested", "task.log")

	w, err := out.LogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening must APPEND, not truncate. A supervisor restarting a task
	// would otherwise lose every previous run's log.
	w2, err := out.LogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w2.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "first\nsecond\n" {
		t.Errorf("log = %q; want both writes", content)
	}
}

func TestPrefix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		writes []string
		want   string
	}{
		{
			name:   "one write, several lines",
			writes: []string{"a\nb\nc\n"},
			want:   "[t] a\n[t] b\n[t] c\n",
		},
		{
			// The case a per-Write implementation gets wrong: a process
			// writing in fragments produces a prefix mid-line.
			name:   "a line split across writes gets ONE prefix",
			writes: []string{"hel", "lo\n"},
			want:   "[t] hello\n",
		},
		{
			name:   "trailing text with no newline",
			writes: []string{"no newline"},
			want:   "[t] no newline",
		},
		{
			name:   "writes resuming after a newline",
			writes: []string{"a\n", "b\n"},
			want:   "[t] a\n[t] b\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			w := out.Prefix(&buf, "[t] ")
			for _, s := range tc.writes {
				n, err := w.Write([]byte(s))
				if err != nil {
					t.Fatal(err)
				}
				// The count must be len(s), not the bytes actually emitted, or
				// io.Copy reports a short write for output that arrived intact.
				if n != len(s) {
					t.Fatalf("Write returned %d for %d bytes: a short write", n, len(s))
				}
			}
			if buf.String() != tc.want {
				t.Errorf("got %q; want %q", buf.String(), tc.want)
			}
		})
	}
}

func TestPrefixWithLogFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "x.log")
	f, err := out.LogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var term bytes.Buffer
	// The real shape: terminal AND file, prefixed.
	w := out.Prefix(&term, "[guard] ")
	if _, err := w.Write([]byte("starting\n")); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(term.String(), "[guard] ") {
		t.Errorf("got %q", term.String())
	}
}
