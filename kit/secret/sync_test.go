package secret_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ubgo/lath/kit/secret"
)

// fakeStore records what was asked of it. Reserved mirrors a real store's
// rule (GitHub refuses the GITHUB_ prefix) without naming one.
type fakeStore struct {
	mu       sync.Mutex
	live     map[string]string
	reserved string
	failOn   map[string]error
	setCalls []string
	delCalls []string
}

func newFake() *fakeStore {
	return &fakeStore{live: map[string]string{}, failOn: map[string]error{}}
}

func (f *fakeStore) Set(_ context.Context, key string, v secret.Value) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls = append(f.setCalls, key)
	if err, bad := f.failOn[key]; bad {
		return err
	}
	f.live[key] = v.Reveal()
	return nil
}

func (f *fakeStore) List(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.live))
	for k := range f.live {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delCalls = append(f.delCalls, key)
	delete(f.live, key)
	return nil
}

func (f *fakeStore) Reserved(key string) bool {
	return f.reserved != "" && strings.HasPrefix(key, f.reserved)
}

// comparingStore is a store that CAN read values back, exercising the optional
// Comparer upgrade.
type comparingStore struct {
	*fakeStore
	compares int
}

func (c *comparingStore) Same(_ context.Context, key string, v secret.Value) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.compares++
	existing, ok := c.live[key]
	return ok && existing == v.Reveal(), nil
}

func setOf(pairs map[string]string) *secret.Set {
	s := secret.NewSet()
	for k, v := range pairs {
		s.Put(k, secret.New(v))
	}
	return s
}

func TestSyncWritesEverything(t *testing.T) {
	t.Parallel()
	store := newFake()
	report, err := secret.Sync(context.Background(), store,
		setOf(map[string]string{"A": "1", "B": "2", "C": "3"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Set) != 3 || len(report.Failed) != 0 {
		t.Errorf("report = %+v", report)
	}
	if fmt.Sprint(store.setCalls) != "[A B C]" {
		t.Errorf("set order = %v; want key order, for stable output", store.setCalls)
	}
}

// TestSyncWritesUnconditionallyWithoutAComparer pins the default decided in
// the spec: no local state, so an unchanged value is written again rather than
// consulting a cache that would differ between a laptop and CI.
func TestSyncWritesUnconditionallyWithoutAComparer(t *testing.T) {
	t.Parallel()
	store := newFake()
	desired := setOf(map[string]string{"A": "1"})

	for range 3 {
		if _, err := secret.Sync(context.Background(), store, desired); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.setCalls) != 3 {
		t.Errorf("Set called %d times over 3 syncs; a plain Store must be written every time",
			len(store.setCalls))
	}
}

// TestComparerSkipsUnchanged pins the optional upgrade: a store that CAN read
// back is asked, and matching values are left alone.
func TestComparerSkipsUnchanged(t *testing.T) {
	t.Parallel()
	store := &comparingStore{fakeStore: newFake()}
	desired := setOf(map[string]string{"A": "1", "B": "2"})

	first, err := secret.Sync(context.Background(), store, desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Set) != 2 {
		t.Fatalf("first sync wrote %d; want 2", len(first.Set))
	}

	second, err := secret.Sync(context.Background(), store, desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Unchanged) != 2 || len(second.Set) != 0 {
		t.Errorf("second sync = %+v; want both unchanged", second)
	}

	// A changed value must still be written.
	desired.Put("A", secret.New("CHANGED"))
	third, err := secret.Sync(context.Background(), store, desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Set) != 1 || third.Set[0] != "A" {
		t.Errorf("third sync = %+v; want only A rewritten", third)
	}
}

// TestReservedKeysAreSkippedNotFailed pins that a name the store refuses is
// reported as skipped rather than attempted and failed.
func TestReservedKeysAreSkippedNotFailed(t *testing.T) {
	t.Parallel()
	store := newFake()
	store.reserved = "RESERVED_"
	report, err := secret.Sync(context.Background(), store,
		setOf(map[string]string{"OK": "1", "RESERVED_TOKEN": "2", "RESERVED_ACTOR": "3"}))
	if err != nil {
		t.Fatalf("reserved names must not fail the run: %v", err)
	}
	if len(report.Skipped) != 2 || len(report.Set) != 1 {
		t.Errorf("report = %+v", report)
	}
	for _, called := range store.setCalls {
		if strings.HasPrefix(called, "RESERVED_") {
			t.Errorf("a reserved key was attempted anyway: %s", called)
		}
	}
}

// TestPartialFailureIsFullyReported is the invariant that makes a half-published
// set diagnosable: every key is attempted, and every failure named.
func TestPartialFailureIsFullyReported(t *testing.T) {
	t.Parallel()
	store := newFake()
	boom := errors.New("the remote refused")
	store.failOn["C"] = boom

	desired := setOf(map[string]string{"A": "1", "B": "2", "C": "3", "D": "4", "E": "5"})
	report, err := secret.Sync(context.Background(), store, desired)

	if !errors.Is(err, secret.ErrSyncIncomplete) {
		t.Fatalf("err = %v; want ErrSyncIncomplete", err)
	}
	if !errors.Is(report.Failed["C"], boom) {
		t.Errorf("the underlying failure is not reachable: %v", report.Failed["C"])
	}
	// The point: D and E were still attempted after C failed.
	if len(store.setCalls) != 5 {
		t.Errorf("Set called %d times; want all 5: the run must not abort at the first failure",
			len(store.setCalls))
	}
	if len(report.Set) != 4 {
		t.Errorf("report.Set = %v; want the four that succeeded", report.Set)
	}
	// Completeness: every desired key is accounted for exactly once.
	if report.Accounted() != desired.Len() {
		t.Errorf("accounted for %d of %d keys", report.Accounted(), desired.Len())
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	t.Parallel()
	store := newFake()
	store.live["ORPHAN"] = "old"

	report, err := secret.Sync(context.Background(), store,
		setOf(map[string]string{"A": "1", "B": "2"}),
		secret.DryRun(), secret.Prune())
	if err != nil {
		t.Fatal(err)
	}
	if len(store.setCalls) != 0 || len(store.delCalls) != 0 {
		t.Errorf("a dry run touched the store: set=%v del=%v", store.setCalls, store.delCalls)
	}
	if len(report.Set) != 2 || len(report.Deleted) != 1 {
		t.Errorf("report = %+v; a dry run must still describe the full plan", report)
	}
	if !report.DryRun || !strings.Contains(report.String(), "dry run") {
		t.Errorf("the report does not identify itself as a dry run: %q", report.String())
	}
}

func TestPruneRemovesOrphansOnlyWhenAsked(t *testing.T) {
	t.Parallel()
	desired := map[string]string{"KEEP": "1"}

	t.Run("off by default", func(t *testing.T) {
		t.Parallel()
		store := newFake()
		store.live["ORPHAN"] = "x"
		report, err := secret.Sync(context.Background(), store, setOf(desired))
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Deleted) != 0 || len(store.delCalls) != 0 {
			t.Error("pruning happened without Prune: a deleted credential is not recoverable")
		}
	})

	t.Run("with Prune", func(t *testing.T) {
		t.Parallel()
		store := newFake()
		store.reserved = "RESERVED_"
		store.live["ORPHAN"] = "x"
		store.live["KEEP"] = "1"
		store.live["RESERVED_ONE"] = "y"

		report, err := secret.Sync(context.Background(), store, setOf(desired), secret.Prune())
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(report.Deleted) != "[ORPHAN]" {
			t.Errorf("Deleted = %v; want only the orphan", report.Deleted)
		}
		// A reserved name is never deleted: it is not ours to manage.
		for _, d := range store.delCalls {
			if strings.HasPrefix(d, "RESERVED_") {
				t.Errorf("prune deleted a reserved name: %s", d)
			}
		}
	})
}

func TestProgressReportsWithoutDisclosing(t *testing.T) {
	t.Parallel()
	store := newFake()
	store.reserved = "RESERVED_"
	var events []string
	_, err := secret.Sync(context.Background(), store,
		setOf(map[string]string{"TOKEN": theSecret, "RESERVED_X": "y"}),
		secret.Progress(func(e secret.Event) { events = append(events, e.String()) }))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(events, "\n")
	if strings.Contains(joined, theSecret) {
		t.Errorf("progress disclosed the plaintext:\n%s", joined)
	}
	if len(events) != 2 {
		t.Errorf("got %d events; want one per key", len(events))
	}
	if !strings.Contains(joined, fmt.Sprint(len(theSecret))) {
		t.Errorf("progress omits the byte count:\n%s", joined)
	}
}

func TestSyncHonoursCancellation(t *testing.T) {
	t.Parallel()
	store := newFake()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := secret.Sync(ctx, store, setOf(map[string]string{"A": "1", "B": "2"}))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled", err)
	}
	if len(store.setCalls) != 0 {
		t.Errorf("a cancelled sync wrote %v", store.setCalls)
	}
}

func TestSyncNilAndEmptySet(t *testing.T) {
	t.Parallel()
	for name, desired := range map[string]*secret.Set{"nil": nil, "empty": secret.NewSet()} {
		report, err := secret.Sync(context.Background(), newFake(), desired)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if report.Accounted() != 0 || !strings.Contains(report.String(), "nothing to do") {
			t.Errorf("%s: report = %q", name, report.String())
		}
	}
}

// provisioningStore implements the optional Provisioner upgrade.
type provisioningStore struct {
	*fakeStore
	ensured int
	err     error
}

func (p *provisioningStore) Ensure(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensured++
	return p.err
}

// TestProvisionIsOptInNotAutomatic pins the decision that matters most here.
//
// The destination is normally named by a positional argument, so auto-creating
// it would mean a mistyped environment silently provisions infrastructure
// instead of failing, and failing is the useful outcome, because it tells the
// operator they typed the wrong thing.
func TestProvisionIsOptInNotAutomatic(t *testing.T) {
	t.Parallel()
	store := &provisioningStore{fakeStore: newFake()}

	if _, err := secret.Sync(context.Background(), store, setOf(map[string]string{"A": "1"})); err != nil {
		t.Fatal(err)
	}
	if store.ensured != 0 {
		t.Errorf("Ensure was called %d times without Provision: a typo would create infrastructure", store.ensured)
	}
}

func TestProvisionCreatesTheDestinationFirst(t *testing.T) {
	t.Parallel()
	store := &provisioningStore{fakeStore: newFake()}

	report, err := secret.Sync(context.Background(), store,
		setOf(map[string]string{"A": "1"}), secret.Provision())
	if err != nil {
		t.Fatal(err)
	}
	if store.ensured != 1 {
		t.Errorf("Ensure called %d times; want exactly 1", store.ensured)
	}
	if !report.Provisioned {
		t.Error("the report does not record that the destination was ensured")
	}
	// Ordering matters: a write before the destination exists is the 404 this
	// option was added to remove.
	if len(store.setCalls) != 1 {
		t.Errorf("setCalls = %v", store.setCalls)
	}
}

// TestProvisionIsReadOnlyUnderDryRun pins that a dry run stays entirely
// read-only, creating a real environment while claiming to change nothing
// would be the worst possible violation of what a dry run means.
func TestProvisionIsReadOnlyUnderDryRun(t *testing.T) {
	t.Parallel()
	store := &provisioningStore{fakeStore: newFake()}

	report, err := secret.Sync(context.Background(), store,
		setOf(map[string]string{"A": "1"}), secret.Provision(), secret.DryRun())
	if err != nil {
		t.Fatal(err)
	}
	if store.ensured != 0 {
		t.Error("a dry run created the destination for real")
	}
	if !report.Provisioned {
		t.Error("a dry run should still report that it would provision")
	}
}

// TestProvisionFailureStopsBeforeWriting pins that a destination which cannot
// be created does not lead to a stream of confusing per-key failures.
func TestProvisionFailureStopsBeforeWriting(t *testing.T) {
	t.Parallel()
	boom := errors.New("insufficient permissions")
	store := &provisioningStore{fakeStore: newFake(), err: boom}

	_, err := secret.Sync(context.Background(), store,
		setOf(map[string]string{"A": "1", "B": "2"}), secret.Provision())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v; want the provisioning failure", err)
	}
	if len(store.setCalls) != 0 {
		t.Errorf("wrote %v after failing to create the destination", store.setCalls)
	}
}

// TestProvisionOnAStoreThatCannotIsAnError pins that the request is not
// silently ignored. A quiet no-op resurfaces later as a confusing "not found".
func TestProvisionOnAStoreThatCannotIsAnError(t *testing.T) {
	t.Parallel()
	_, err := secret.Sync(context.Background(), newFake(),
		setOf(map[string]string{"A": "1"}), secret.Provision())
	if !errors.Is(err, secret.ErrCannotProvision) {
		t.Errorf("err = %v; want ErrCannotProvision", err)
	}
}
