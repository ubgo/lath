package confirm

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// The interactive half of this package can only be reached once the terminal
// check passes, and an external test has no terminal. withTTYCheck is the seam
// that exists for exactly this, so these tests live inside the package: the
// prompt-writing, answer-reading path is the part that decides whether a
// production deploy proceeds, and leaving it unexercised because it is
// awkward to reach is how it stays unexercised.

// alwaysTerminal satisfies the check so the interactive path runs.
func alwaysTerminal() Option { return withTTYCheck(func(io.Reader) bool { return true }) }

// TestYesReadsTheAnswerAndPrintsThePrompt. Both halves matter: an answer read
// without the prompt reaching the operator is a question nobody was asked.
func TestYesReadsTheAnswerAndPrintsThePrompt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		typed string
		want  bool
	}{
		{"y\n", true},
		{"YES\n", true},
		{"  yes  \n", true},
		{"n\n", false},
		{"\n", false},
		// EOF with nothing typed: ctrl-D, or input that ran out. A decline,
		// never a failure, and never a yes.
		{"", false},
		{"maybe\n", false},
	} {
		t.Run(strings.TrimSpace(tc.typed), func(t *testing.T) {
			t.Parallel()
			var out strings.Builder
			got, err := Yes(context.Background(), "proceed?",
				In(strings.NewReader(tc.typed)), Out(&out), alwaysTerminal())
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != tc.want {
				t.Errorf("Yes(%q) = %v, want %v", tc.typed, got, tc.want)
			}
			if !strings.Contains(out.String(), "proceed?") {
				t.Errorf("the prompt never reached the operator: %q", out.String())
			}
			// The default must be visible in the prompt, or "just press
			// Enter" is a guess about what happens next.
			if !strings.Contains(out.String(), "[y/N]") {
				t.Errorf("the prompt does not show the default: %q", out.String())
			}
		})
	}
}

// TestPhraseRequiresTheExactWords. The point of a phrase is that a reflexive
// "y" cannot satisfy it: anything but the required string, including the same
// string with surrounding space, is a decline.
func TestPhraseRequiresTheExactWords(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		typed   string
		wantErr bool
	}{
		{"production\n", false},
		// Whitespace is NOT trimmed, unlike a yes/no answer: the phrase exists
		// to make the operator type precisely this, and accepting a near miss
		// would give back the carelessness it was added to prevent.
		{"  production \n", true},
		{"y\n", true},
		{"prod\n", true},
		{"\n", true},
		{"", true},
	} {
		t.Run(strings.TrimSpace(tc.typed), func(t *testing.T) {
			t.Parallel()
			var out strings.Builder
			err := Phrase(context.Background(), "destroy?", "production",
				In(strings.NewReader(tc.typed)), Out(&out), alwaysTerminal())
			if tc.wantErr && !errors.Is(err, ErrDeclined) {
				t.Errorf("Phrase(%q) = %v, want a decline", tc.typed, err)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Phrase(%q) = %v, want acceptance", tc.typed, err)
			}
			if !strings.Contains(out.String(), "production") {
				t.Errorf("the prompt does not say what to type: %q", out.String())
			}
		})
	}
}

// TestAReadFailureIsNotADecline. A broken terminal and a refusal must be
// distinguishable: silently treating an I/O failure as "no" would abort a
// deploy and report it as the operator's choice.
func TestAReadFailureIsNotADecline(t *testing.T) {
	t.Parallel()
	broken := errors.New("input/output error")
	var out strings.Builder

	_, err := Yes(context.Background(), "proceed?",
		In(iotest.ErrReader(broken)), Out(&out), alwaysTerminal())
	if err == nil {
		t.Fatal("a broken reader was reported as an answer")
	}
	if !errors.Is(err, broken) {
		t.Errorf("err = %v; want the underlying failure to stay reachable", err)
	}
}

// TestAPromptThatCannotBePrintedIsNotAsked. Reading an answer to a question
// the operator never saw is the worst outcome available here.
func TestAPromptThatCannotBePrintedIsNotAsked(t *testing.T) {
	t.Parallel()
	broken := errors.New("broken pipe")

	_, err := Yes(context.Background(), "proceed?",
		In(strings.NewReader("y\n")), Out(failingWriter{broken}), alwaysTerminal())
	if err == nil {
		t.Fatal("an unprintable prompt was answered anyway")
	}
	if !errors.Is(err, broken) {
		t.Errorf("err = %v; want the write failure to stay reachable", err)
	}
}

// failingWriter is an output stream that has gone away, the shape a closed
// pipe or a detached terminal takes.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }
