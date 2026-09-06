package confirm_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ubgo/lath/kit/confirm"
)

// TestNonInteractiveFailsFastRatherThanHanging is the mechanism that earns
// this package its place. A prompt that blocks forever in CI hangs the job
// until a global timeout with nothing in the log explaining the silence.
func TestNonInteractiveFailsFastRatherThanHanging(t *testing.T) {
	t.Parallel()
	// A pipe with no data: an interactive read would block here forever.
	pr, pw := ioPipe(t)
	defer pw.Close()

	done := make(chan error, 1)
	go func() {
		_, err := confirm.Yes(context.Background(), "proceed?", confirm.In(pr), confirm.Out(&bytes.Buffer{}))
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, confirm.ErrNotInteractive) {
			t.Errorf("err = %v; want ErrNotInteractive", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt blocked on a non-terminal; this is the CI hang it must prevent")
	}
}

func TestPhraseNonInteractive(t *testing.T) {
	t.Parallel()
	pr, pw := ioPipe(t)
	defer pw.Close()
	err := confirm.Phrase(context.Background(), "destroy?", "prod",
		confirm.In(pr), confirm.Out(&bytes.Buffer{}))
	if !errors.Is(err, confirm.ErrNotInteractive) {
		t.Errorf("err = %v; want ErrNotInteractive", err)
	}
}

// TestAssumeAnswersWithoutATerminal pins how a --yes flag passes a decision
// already made, without faking stdin.
func TestAssumeAnswersWithoutATerminal(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer

	ok, err := confirm.Yes(context.Background(), "proceed?", confirm.Assume(true), confirm.Out(&out))
	if err != nil || !ok {
		t.Errorf("Assume(true) = %v, %v; want true, nil", ok, err)
	}
	ok, err = confirm.Yes(context.Background(), "proceed?", confirm.Assume(false), confirm.Out(&out))
	if err != nil || ok {
		t.Errorf("Assume(false) = %v, %v; want false, nil", ok, err)
	}
	// Nothing is printed when nothing is being asked.
	if out.Len() != 0 {
		t.Errorf("Assume still wrote a prompt: %q", out.String())
	}

	if err := confirm.Phrase(context.Background(), "destroy?", "prod", confirm.Assume(true)); err != nil {
		t.Errorf("Phrase with Assume(true) = %v; want nil", err)
	}
	if err := confirm.Phrase(context.Background(), "destroy?", "prod", confirm.Assume(false)); !errors.Is(err, confirm.ErrDeclined) {
		t.Errorf("Phrase with Assume(false) = %v; want ErrDeclined", err)
	}
}

// TestCancellationUnblocksAWaitingPrompt pins that Ctrl-C works even mid-prompt.
func TestCancellationUnblocksAWaitingPrompt(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Assume is absent and the reader is not a terminal, so this returns before
	// reading; the point is that it returns at all, promptly.
	done := make(chan struct{})
	go func() {
		_, _ = confirm.Yes(ctx, "proceed?", confirm.In(terminalLike("")), confirm.Out(&bytes.Buffer{}))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled prompt did not return")
	}
}

// TestErrDeclinedNamesTheRequiredPhrase pins that a refusal tells the operator
// what they were supposed to type.
func TestErrDeclinedNamesTheRequiredPhrase(t *testing.T) {
	t.Parallel()
	err := confirm.Phrase(context.Background(), "destroy?", "production", confirm.Assume(false))
	if !errors.Is(err, confirm.ErrDeclined) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "confirm") {
		t.Errorf("err = %v; want it attributed to the package", err)
	}
}

func ioPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, w
}

// terminalLike returns a reader that is not an *os.File, so it can never be
// mistaken for a terminal, used to assert the non-interactive path.
func terminalLike(s string) *strings.Reader { return strings.NewReader(s) }
