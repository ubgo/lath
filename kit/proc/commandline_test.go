package proc_test

import (
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/proc"
)

// TestCommandLineIsPasteable is the whole point: printing argv with %v gives
// `[build -t x .]`, which reads as the slice it is and cannot be run. Showing
// an operator what a step would do is only useful if they can then do it.
func TestCommandLineIsPasteable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, program, want string
		args                []string
	}{
		{"docker", "docker", "docker build -t app:1 .", []string{"build", "-t", "app:1", "."}},
		{"no arguments", "ls", "ls", nil},
		{"paths and flags stay bare", "git", "git rev-parse --short=7 HEAD",
			[]string{"rev-parse", "--short=7", "HEAD"}},
		{"a mount is one word", "docker", "docker run -v /srv/a:/app/a:ro img",
			[]string{"run", "-v", "/srv/a:/app/a:ro", "img"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := proc.CommandLine(tc.program, tc.args...); got != tc.want {
				t.Errorf("CommandLine = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCommandLineQuotesWhatNeedsIt, over-quoting is ugly, under-quoting gives
// a line that looks right and does something else.
func TestCommandLineQuotesWhatNeedsIt(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"plain", "sh plain"},
		{"has space", "sh 'has space'"},
		{"$HOME", "sh '$HOME'"},
		{"`whoami`", "sh '`whoami`'"},
		{"a;rm -rf /", "sh 'a;rm -rf /'"},
		{"", "sh ''"},
		{"it's", `sh 'it'\''s'`},
	}
	for _, tc := range cases {
		if got := proc.CommandLine("sh", tc.in); got != tc.want {
			t.Errorf("CommandLine(sh, %q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCommandLineQuotedValuesAreInert. A quoted word must not expand when the
// line is pasted, which is why single quotes are used rather than double.
func TestCommandLineQuotedValuesAreInert(t *testing.T) {
	t.Parallel()
	line := proc.CommandLine("echo", "$HOME", "`id`")
	if strings.Count(line, "'") < 4 {
		t.Errorf("expandable values were not quoted: %s", line)
	}
	if strings.Contains(line, `"`) {
		t.Errorf("double quotes would still expand: %s", line)
	}
}
