package session_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubgo/lath/kit/internal/fsprobe"
	"github.com/ubgo/lath/kit/lock"
	"github.com/ubgo/lath/kit/session"
)

// isolate points the session directory at a temp dir, so tests never see each
// other's sessions or the developer's real ones.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(session.DirEnv, dir)
	return dir
}

func TestAdvertiseAndList(t *testing.T) {
	dir := isolate(t)
	a, err := session.Advertise(context.Background(), "/proj/a|deploy local",
		session.Meta{"project": "a", "target": "deploy local"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if got := filepath.Dir(a.Socket()); got != dir {
		t.Errorf("socket in %q, want %q", got, dir)
	}

	list, err := session.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d sessions, want 1", len(list))
	}
	if list[0].Socket != a.Socket() {
		t.Errorf("socket = %q, want %q", list[0].Socket, a.Socket())
	}
	if list[0].Meta["target"] != "deploy local" {
		t.Errorf("meta = %v", list[0].Meta)
	}
	if list[0].PID != os.Getpid() {
		t.Errorf("pid = %d, want %d", list[0].PID, os.Getpid())
	}
}

// TestTwoProjectsDoNotCollide is the naming bug this file exists to prevent.
// An earlier design keyed sessions on the target alone, so two projects both
// running "deploy local" would have fought over one socket path.
func TestTwoProjectsDoNotCollide(t *testing.T) {
	isolate(t)
	a, err := session.Advertise(context.Background(), "/proj/acme_api|deploy local", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := session.Advertise(context.Background(), "/proj/billing-api|deploy local", nil)
	if err != nil {
		t.Fatalf("second project could not advertise the same target: %v", err)
	}
	defer b.Close()

	if a.Socket() == b.Socket() {
		t.Fatalf("both projects got %q", a.Socket())
	}
	list, err := session.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("got %d sessions, want 2", len(list))
	}
}

// TestIDSeparatesRunsOfOneProject. The same project stepped in two terminals
// must produce two sessions, which is why the PID is part of the id.
func TestIDSeparatesRunsOfOneProject(t *testing.T) {
	t.Parallel()
	const key = "/proj/a|deploy local"
	if session.ID(key, 100) == session.ID(key, 101) {
		t.Error("two runs of one project share an id")
	}
	if session.ID(key, 100) != session.ID(key, 100) {
		t.Error("ID is not stable for the same key and pid")
	}
	if strings.ContainsAny(session.ID(key, 100), `/\:`) {
		t.Errorf("id %q is not filesystem-safe", session.ID(key, 100))
	}
}

// TestDeadSessionIsSwept covers the crash case: a run that died leaves a lock
// and a socket behind, and a listing that offered them would hand out a socket
// nobody is serving.
func TestDeadSessionIsSwept(t *testing.T) {
	dir := isolate(t)

	// A lock naming a PID that cannot be alive, written the way lock.Read
	// expects. PID 0 is never a live process, so alive() rejects it without
	// depending on what else is running on this machine.
	dead := lock.Info{PID: 0, Host: "somewhere", Note: `{"target":"ghost"}`}
	blob, err := json.Marshal(dead)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, "deadbeef-4242.lock")
	sockPath := filepath.Join(dir, "deadbeef-4242.sock")
	if err := os.WriteFile(lockPath, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	list, err := session.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("a dead session was listed: %+v", list)
	}
	for _, p := range []string{lockPath, sockPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep", filepath.Base(p))
		}
	}
}

// TestCloseRemovesTheSocket. A listener that exits without unlinking leaves a
// path that accepts connections from nobody.
func TestCloseRemovesTheSocket(t *testing.T) {
	isolate(t)
	a, err := session.Advertise(context.Background(), "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.Socket(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Socket()); !os.IsNotExist(err) {
		t.Error("socket survived Close")
	}
	// Idempotent: a deferred Close after an explicit one must not error.
	if err := a.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	list, err := session.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("closed session still listed: %+v", list)
	}
}

// TestAdvertiseClearsAStaleSocket covers the reclaim path: a crashed run's
// lock is reclaimed, but its socket file would make Listen fail with "address
// already in use".
func TestAdvertiseClearsAStaleSocket(t *testing.T) {
	dir := isolate(t)
	id := session.ID("k", os.Getpid())
	stale := filepath.Join(dir, id+".sock")
	if err := os.WriteFile(stale, []byte("leftover"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := session.Advertise(context.Background(), "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := os.Stat(a.Socket()); !os.IsNotExist(err) {
		t.Error("a stale socket survived Advertise; Listen would fail")
	}
}

// TestDirIsOwnerOnly. The session directory carries a control channel into a
// running deploy, and filesystem permissions are the only thing scoping who
// may attach.
func TestDirIsOwnerOnly(t *testing.T) {
	dir := isolate(t)
	if _, err := session.Dir(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("session dir is %04o; group or others can reach it", perm)
	}
}

// TestCloseIsSafeTwice pins the promise in Close's doc comment. A deferred
// Close plus an explicit one on the success path is the ordinary shape of this
// API's use, and the second call must not report the absent socket as a
// failure.
func TestCloseIsSafeTwice(t *testing.T) {
	isolate(t)
	a, err := session.Advertise(context.Background(), "project", session.Meta{"target": "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Errorf("the second Close reported %v; the doc comment promises it is safe", err)
	}
}

// TestCloseOnNothingIsNothing. Advertise returns (nil, err) on failure, and
// the ordinary caller defers Close on the result before checking the error is
// nil, or does so in a cleanup helper. A nil dereference there would crash a
// deploy while REPORTING a failure, hiding the real one.
func TestCloseOnNothingIsNothing(t *testing.T) {
	var a *session.Advertised
	if err := a.Close(); err != nil {
		t.Errorf("Close on a nil advertisement = %v, want nil", err)
	}
}

// TestAdvertiseRefusesADirectoryItCannotSecure. The session directory carries
// control channels into running deploys; if its permissions cannot be
// enforced, continuing would advertise a channel other users on the machine
// can reach. A refusal is the only safe answer, and it must name the path.
func TestAdvertiseRefusesADirectoryItCannotSecure(t *testing.T) {
	fsprobe.NeedsEnforcedDirectoryPermissions(t)
	// A path whose PARENT is unwritable: MkdirAll cannot create it, which is
	// the same class of failure as being unable to secure it and the one that
	// can be provoked portably.
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	t.Setenv(session.DirEnv, filepath.Join(parent, "sessions"))

	_, err := session.Advertise(context.Background(), "project", session.Meta{"target": "deploy"})
	if err == nil {
		t.Fatal("a session was advertised in a directory that could not be created")
	}
	if !strings.Contains(err.Error(), "session:") {
		t.Errorf("err = %v; want the package named so the failure is attributable", err)
	}
}

// TestListRefusesAnUnusableDirectory. A listing that returned an empty slice
// here would say "no sessions are running", which is a different and wrong
// answer from "the session directory cannot be read".
func TestListRefusesAnUnusableDirectory(t *testing.T) {
	fsprobe.NeedsEnforcedDirectoryPermissions(t)
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	t.Setenv(session.DirEnv, filepath.Join(parent, "sessions"))

	got, err := session.List()
	if err == nil {
		t.Fatalf("List returned %v and no error for a directory it cannot use", got)
	}
	if got != nil {
		t.Error("List returned a partial answer alongside its error")
	}
}

// TestAdvertiseTwiceInOneProcessIsRefused. Two Advertise calls with the same
// key from the same PID resolve to the same id, and the lock is what stops the
// second one silently taking over the first one's socket path.
func TestAdvertiseTwiceInOneProcessIsRefused(t *testing.T) {
	isolate(t)
	first, err := session.Advertise(context.Background(), "project", session.Meta{"target": "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })

	second, err := session.Advertise(context.Background(), "project", session.Meta{"target": "deploy"})
	if err == nil {
		_ = second.Close()
		t.Fatal("the same session was advertised twice; two listeners would fight over one socket")
	}
	// The first advertisement must survive the second's refusal: a failed
	// claim that damaged the live one would take down a running deploy.
	if _, statErr := os.Stat(first.Socket()); statErr != nil && !os.IsNotExist(statErr) {
		t.Errorf("the live session was disturbed: %v", statErr)
	}
	if !lock.Held(strings.TrimSuffix(first.Socket(), ".sock") + ".lock") {
		t.Error("the refused second claim released the first session's lock")
	}
}
