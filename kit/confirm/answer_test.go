package confirm

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// alwaysTTY makes the answer-parsing path reachable without a real terminal.
func alwaysTTY(io.Reader) bool { return true }

// TestYesAnswerParsing pins exactly which answers authorise an action.
//
// The default is the important column: an empty answer, a stray newline from a
// pipe, or a reflexive Enter must never be the thing that authorises something
// destructive.
func TestYesAnswerParsing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		answer string
		want   bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"  yes  \n", true},
		{"\n", false}, // a bare Enter
		{"", false},   // EOF with nothing typed
		{"n\n", false},
		{"no\n", false},
		{"maybe\n", false},
		{"ye\n", false}, // a prefix is not a yes
		{"yy\n", false},
		{"1\n", false},
		{"true\n", false},
	} {
		t.Run(strings.TrimSpace(tc.answer)+"|", func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			got, err := Yes(context.Background(), "proceed?",
				In(strings.NewReader(tc.answer)), Out(&out), withTTYCheck(alwaysTTY))
			if err != nil {
				t.Fatalf("answer %q: %v", tc.answer, err)
			}
			if got != tc.want {
				t.Errorf("answer %q = %v; want %v", tc.answer, got, tc.want)
			}
			if !strings.Contains(out.String(), "[y/N]") {
				t.Errorf("the prompt does not show that no is the default: %q", out.String())
			}
		})
	}
}

// TestPhraseRequiresAnExactMatch pins that the phrase gate cannot be satisfied
// by anything close. Typing the environment's own name is the point, it makes
// the operator look at what they are affecting.
func TestPhraseRequiresAnExactMatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		answer string
		ok     bool
	}{
		{"prod\n", true},
		{"prod", true},     // EOF without a newline still counts
		{"prod\r\n", true}, // a Windows line ending
		{"PROD\n", false},  // case matters
		{"Prod\n", false},
		{" prod\n", false}, // no trimming: they must type precisely this
		{"prod \n", false},
		{"production\n", false},
		{"y\n", false},
		{"\n", false},
		{"", false},
	} {
		t.Run(strings.TrimSpace(tc.answer)+"|", func(t *testing.T) {
			t.Parallel()
			err := Phrase(context.Background(), "destroy the database?", "prod",
				In(strings.NewReader(tc.answer)), Out(&bytes.Buffer{}), withTTYCheck(alwaysTTY))
			switch {
			case tc.ok && err != nil:
				t.Errorf("answer %q = %v; want it accepted", tc.answer, err)
			case !tc.ok && !errors.Is(err, ErrDeclined):
				t.Errorf("answer %q = %v; want ErrDeclined", tc.answer, err)
			}
		})
	}
}

// TestPhrasePromptShowsWhatToType pins that the operator is told the exact
// string, or the gate is unpassable rather than deliberate.
func TestPhrasePromptShowsWhatToType(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	_ = Phrase(context.Background(), "destroy?", "prod-eu-1",
		In(strings.NewReader("\n")), Out(&out), withTTYCheck(alwaysTTY))
	if !strings.Contains(out.String(), "prod-eu-1") {
		t.Errorf("the prompt does not name the required phrase: %q", out.String())
	}
}

// TestIsTerminalRejectsNonFiles pins the real detector's behaviour for the
// types a caller can actually pass.
func TestIsTerminalRejectsNonFiles(t *testing.T) {
	t.Parallel()
	if isTerminal(strings.NewReader("x")) {
		t.Error("a strings.Reader was reported as a terminal")
	}
	if isTerminal(&bytes.Buffer{}) {
		t.Error("a bytes.Buffer was reported as a terminal")
	}
}
