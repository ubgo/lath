package lock_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ubgo/lath/kit/internal/fsprobe"
	"github.com/ubgo/lath/kit/lock"
)

func lockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sub", "dir", "the.lock")
}

func TestAcquireAndRelease(t *testing.T) {
	t.Parallel()
	path := lockPath(t)

	l, err := lock.Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	// Parent directories are created, so a caller need not mkdir first.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the lock file was not created: %v", err)
	}
	if l.Info().PID != os.Getpid() {
		t.Errorf("Info().PID = %d; want this process", l.Info().PID)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("Release left the lock file behind")
	}
	// Release is idempotent: a deferred Release alongside an explicit one is
	// the normal shape and must not error.
	if err := l.Release(); err != nil {
		t.Errorf("second Release = %v; want nil", err)
	}
}

// TestSelfCannotDoubleAcquire pins the core guarantee.
func TestSelfCannotDoubleAcquire(t *testing.T) {
	t.Parallel()
	path := lockPath(t)
	first, err := lock.Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	_, err = lock.Acquire(context.Background(), path)
	if !errors.Is(err, lock.ErrHeld) {
		t.Fatalf("second Acquire = %v; want ErrHeld", err)
	}
	// The refusal must name the holder, "already locked" alone leaves an
	// operator with nothing to act on.
	if !strings.Contains(err.Error(), "pid") {
		t.Errorf("err = %v; want it to identify the holder", err)
	}
}

// TestFailsFastByDefault pins the decision: waiting is opt-in, because a
// default wait turns a forgotten lock into a job that hangs until a global
// timeout with no output.
func TestFailsFastByDefault(t *testing.T) {
	t.Parallel()
	path := lockPath(t)
	held, err := lock.Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	start := time.Now()
	if _, err := lock.Acquire(context.Background(), path); !errors.Is(err, lock.ErrHeld) {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %s to refuse; the default must not block", elapsed)
	}
}

// TestWaitAcquiresAfterRelease pins the opt-in path.
func TestWaitAcquiresAfterRelease(t *testing.T) {
	t.Parallel()
	path := lockPath(t)
	held, err := lock.Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(250 * time.Millisecond)
		_ = held.Release()
	}()

	second, err := lock.Acquire(context.Background(), path, lock.Wait(10*time.Second))
	if err != nil {
		t.Fatalf("Wait did not acquire after the holder released: %v", err)
	}
	_ = second.Release()
}

func TestWaitGivesUpAtItsDeadline(t *testing.T) {
	t.Parallel()
	path := lockPath(t)
	held, err := lock.Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	start := time.Now()
	_, err = lock.Acquire(context.Background(), path, lock.Wait(300*time.Millisecond))
	if !errors.Is(err, lock.ErrHeld) {
		t.Fatalf("err = %v; want ErrHeld", err)
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Errorf("gave up after %s; it should have waited its full budget", elapsed)
	}
}

// TestWaitHonoursCancellation pins that Ctrl-C always works.
func TestWaitHonoursCancellation(t *testing.T) {
	t.Parallel()
	path := lockPath(t)
	held, err := lock.Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()

	start := time.Now()
	if _, err := lock.Acquire(ctx, path, lock.Wait(time.Hour)); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("took %s; a cancelled wait must not run to its deadline", elapsed)
	}
}

// TestDeadHolderIsReclaimed pins the property that stops one crash wedging the
// tool forever: a lock whose PID is not running may be taken.
func TestDeadHolderIsReclaimed(t *testing.T) {
	t.Parallel()
	path := lockPath(t)
	writeHolder(t, path, lock.Info{
		PID:     4194303, // above any real pid_max: guaranteed absent
		Since:   time.Now(),
		Started: time.Now(),
		Host:    hostname(t),
	})

	l, err := lock.Acquire(context.Background(), path)
	if err != nil {
		t.Fatalf("a lock held by a dead process was not reclaimed: %v", err)
	}
	_ = l.Release()
}

// TestReusedPIDIsNotMistakenForTheHolder is the subtle one, and the reason
// Info records a start time at all.
//
// The lock names a PID that IS running, this test process, but records a
// start time from long ago. That is what a recycled PID looks like: the number
// is live, the process behind it is not the one that took the lock. Comparing
// PIDs alone would honour this lock forever.
func TestReusedPIDIsNotMistakenForTheHolder(t *testing.T) {
	// The distinction this test asserts exists only where start times can be
	// read. Asked of the package rather than guessed from the platform: ps
	// EXISTS on Windows and simply reports no start time, so "is ps
	// installed" was the wrong question and passed where it should have
	// skipped.
	if !lock.LivenessVerifiable() {
		t.Skip("process start times are unavailable here, so a recycled PID cannot be " +
			"distinguished from a live holder (documented: liveness is best-effort)")
	}
	t.Parallel()
	path := lockPath(t)
	writeHolder(t, path, lock.Info{
		PID:     os.Getpid(),
		Started: time.Now().Add(-72 * time.Hour),
		Since:   time.Now().Add(-72 * time.Hour),
		Host:    hostname(t),
	})

	l, err := lock.Acquire(context.Background(), path)
	if err != nil {
		t.Fatalf("a recycled PID was treated as the original holder: %v", err)
	}
	_ = l.Release()
}

// TestLiveHolderWithMatchingStartIsHonoured is the inverse: the same PID with
// a start time that DOES match must be respected, or the check would break
// every genuine lock.
func TestLiveHolderWithMatchingStartIsHonoured(t *testing.T) {
	t.Parallel()
	path := lockPath(t)
	l, err := lock.Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()

	if l.Info().Started.IsZero() {
		t.Skip("this platform could not report a process start time")
	}
	if _, err := lock.Acquire(context.Background(), path); !errors.Is(err, lock.ErrHeld) {
		t.Errorf("a live holder was not honoured: %v", err)
	}
}

// TestCorruptLockFileIsReclaimed pins that a truncated or hand-edited lock does
// not wedge the tool forever on the strength of unreadable bytes.
func TestCorruptLockFileIsReclaimed(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"", "not json", "{", `{"pid":`} {
		path := lockPath(t)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		l, err := lock.Acquire(context.Background(), path)
		if err != nil {
			t.Errorf("a lock file containing %q was not reclaimed: %v", content, err)
			continue
		}
		_ = l.Release()
	}
}

// TestReleaseDoesNotStealAnotherHoldersLock pins a subtle correctness point:
// if our lock was judged stale and taken by someone else, releasing ours must
// not delete THEIR file.
func TestReleaseDoesNotStealAnotherHoldersLock(t *testing.T) {
	t.Parallel()
	path := lockPath(t)
	mine, err := lock.Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate another process having taken over in the meantime.
	writeHolder(t, path, lock.Info{PID: 99999, Since: time.Now(), Host: "elsewhere"})

	if err := mine.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("Release removed a lock file belonging to a different holder")
	}
}

// TestConcurrentAcquireAdmitsExactlyOne is the guarantee under contention.
func TestConcurrentAcquireAdmitsExactlyOne(t *testing.T) {
	t.Parallel()
	path := lockPath(t)
	const racers = 16

	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners []*lock.Lock
	var held int

	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := lock.Acquire(context.Background(), path)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners = append(winners, l)
			case errors.Is(err, lock.ErrHeld):
				held++
			}
		}()
	}
	wg.Wait()

	if len(winners) != 1 {
		t.Errorf("%d goroutines acquired the lock; want exactly 1", len(winners))
	}
	if held != racers-len(winners) {
		t.Errorf("%d were refused with ErrHeld; want %d", held, racers-len(winners))
	}
	for _, w := range winners {
		_ = w.Release()
	}
}

func TestReadAndHeld(t *testing.T) {
	t.Parallel()
	path := lockPath(t)

	if _, err := lock.Read(path); err == nil {
		t.Error("Read of a missing lock succeeded")
	}
	if lock.Held(path) {
		t.Error("Held reported true for a missing lock")
	}

	l, err := lock.Acquire(context.Background(), path, lock.Note("task deploy"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()

	info, err := lock.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.PID != os.Getpid() || info.Note != "task deploy" {
		t.Errorf("Read() = %+v", info)
	}
	if !strings.Contains(info.String(), "task deploy") {
		t.Errorf("String() = %q; want the note included", info.String())
	}
	if !lock.Held(path) {
		t.Error("Held reported false for a lock this process holds")
	}
}

func writeHolder(t *testing.T, path string, i lock.Info) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(i)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func hostname(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

// TestZombieIsNotHeld covers a process that has exited but not been reaped.
//
// It still answers signal 0, so an existence check says yes, and a session
// advertised by it would be offered forever, with a socket that accepts
// connections nobody answers. This is not hypothetical: it happened, and the
// listing kept offering a run that had finished minutes earlier.
func TestZombieIsNotHeld(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("zombies are a unix concept")
	}
	// Started and left unreaped: between the child exiting and Wait being
	// called, it is a zombie. Wait is deliberately never called.
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Wait() })

	// Give it a moment to exit and become defunct.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if out, err := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output(); err == nil {
			if s := strings.TrimSpace(string(out)); s != "" && s[0] == 'Z' {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	path := filepath.Join(t.TempDir(), "zombie.lock")
	info := lock.Info{PID: pid, Host: "here"}
	blob, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatal(err)
	}

	if lock.Held(path) {
		t.Error("a defunct process is reported as holding the lock")
	}
}

// TestPathReportsWhereTheClaimLives. A caller holding a lock needs to be able
// to name the file, for a message or for cleanup.
func TestLockPath(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "x.lock")
	l, err := lock.Acquire(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if l.Path() != p {
		t.Errorf("Path() = %q, want %q", l.Path(), p)
	}
	if info := l.Info(); info.PID != os.Getpid() {
		t.Errorf("Info().PID = %d, want this process", info.PID)
	}
}

// TestStaleAfterGovernsAnotherHostsLock pins the exact rule, which is subtler
// than "old locks are reclaimed".
//
// A lock whose holder is verifiably ALIVE is never reclaimed, however old ,
// age does not override liveness. StaleAfter exists for the case liveness
// cannot be checked at all: a holder on a different machine, reached through a
// shared filesystem, where a PID means nothing here. Such a lock is honoured
// until it ages out, and then taken.
func TestStaleAfterGovernsAnotherHostsLock(t *testing.T) {
	t.Parallel()

	write := func(t *testing.T, since time.Time) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "remote.lock")
		blob, err := json.Marshal(lock.Info{
			PID: 999999, Host: "some-other-machine", Since: since,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, blob, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("fresh remote lock is honoured", func(t *testing.T) {
		t.Parallel()
		p := write(t, time.Now())
		_, err := lock.Acquire(context.Background(), p, lock.StaleAfter(time.Hour))
		if !errors.Is(err, lock.ErrHeld) {
			t.Errorf("err = %v, want ErrHeld: another host's fresh lock cannot be checked, so it is respected", err)
		}
	})

	t.Run("aged remote lock is reclaimed", func(t *testing.T) {
		t.Parallel()
		p := write(t, time.Now().Add(-24*time.Hour))
		l, err := lock.Acquire(context.Background(), p, lock.StaleAfter(time.Hour))
		if err != nil {
			t.Fatalf("a day-old remote lock was not reclaimed: %v", err)
		}
		defer l.Release()
	})
}

// TestALiveHolderIsNeverReclaimedByAge. The property the rule above rests on.
func TestALiveHolderIsNeverReclaimedByAge(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "live.lock")
	held, err := lock.Acquire(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	// Even with a staleness window of a nanosecond, a live holder holds.
	if _, err := lock.Acquire(context.Background(), p, lock.StaleAfter(time.Nanosecond)); !errors.Is(err, lock.ErrHeld) {
		t.Errorf("err = %v, want ErrHeld: age must not override a verified live holder", err)
	}
}

// TestStaleAfterDoesNotReclaimAFreshLock. The age backstop must not defeat
// the guard it backs up.
func TestStaleAfterDoesNotReclaimAFreshLock(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "fresh.lock")
	held, err := lock.Acquire(context.Background(), p, lock.StaleAfter(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	if _, err := lock.Acquire(context.Background(), p, lock.StaleAfter(time.Hour)); !errors.Is(err, lock.ErrHeld) {
		t.Errorf("err = %v, want ErrHeld: a lock taken moments ago is not stale", err)
	}
}

// TestAcquireReportsADirectoryItCannotCreate. The lock path is usually derived
// from a project path, so a wrong or unwritable root is an ordinary mistake;
// the error has to name the package and the path or it reads as a mysterious
// permission failure from somewhere deeper.
func TestAcquireReportsADirectoryItCannotCreate(t *testing.T) {
	t.Parallel()
	fsprobe.NeedsEnforcedDirectoryPermissions(t)
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	_, err := lock.Acquire(context.Background(), filepath.Join(parent, "new", "the.lock"))
	if err == nil {
		t.Fatal("a lock was acquired in a directory that could not be created")
	}
	if !strings.Contains(err.Error(), "lock:") {
		t.Errorf("err = %v; want the package named", err)
	}
	// Not ErrHeld: nobody holds it, and a caller retrying on ErrHeld would
	// spin forever against a permission problem.
	if errors.Is(err, lock.ErrHeld) {
		t.Error("a permission failure was reported as a held lock")
	}
}

// TestALockFileNamingNoProcessIsReclaimed. A PID of zero is what a truncated
// or hand-edited lock file yields once it still parses as JSON. Honouring it
// would block every future acquisition forever, with nothing running to
// explain why.
func TestALockFileNamingNoProcessIsReclaimed(t *testing.T) {
	t.Parallel()
	path := lockPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// This machine's hostname: a lock recorded elsewhere cannot have its
	// liveness checked from here and is honoured until it ages out, which is a
	// different rule tested separately.
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	blob, err := json.Marshal(lock.Info{PID: 0, Host: host, Since: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatal(err)
	}

	lk, err := lock.Acquire(context.Background(), path)
	if err != nil {
		t.Fatalf("a lock naming no process was honoured: %v", err)
	}
	t.Cleanup(func() { _ = lk.Release() })

	// The reclaiming process must now be the recorded holder, or the next
	// caller reclaims it too and two runs proceed at once.
	held, err := lock.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if held.PID != os.Getpid() {
		t.Errorf("holder PID = %d, want this process %d", held.PID, os.Getpid())
	}
}

// TestReleaseSurvivesAVanishedLockFile. Something else cleaned the directory,
// a tmpreaper, a `rm -rf`, a test helper. Release must not turn that into an
// error, because it runs in a defer at the end of successful work and would
// convert a completed deploy into a failed one.
func TestReleaseSurvivesAVanishedLockFile(t *testing.T) {
	t.Parallel()
	path := lockPath(t)
	lk, err := lock.Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	if err := lk.Release(); err != nil {
		t.Errorf("Release on a vanished lock = %v, want nil", err)
	}
}
