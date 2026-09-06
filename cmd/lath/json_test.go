package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTakeJSONFlag(t *testing.T) {
	t.Parallel()
	out, found := takeJSONFlag([]string{"prod", jsonFlag, "--apply"})
	if !found {
		t.Error("flag not found")
	}
	if strings.Join(out, " ") != "prod --apply" {
		t.Errorf("args = %v; the target's own flags must survive", out)
	}
	if _, found := takeJSONFlag([]string{"prod"}); found {
		t.Error("found a flag that was not passed")
	}
}

// TestPrintTargetsJSONIsParseable is the whole point of the flag: a consumer
// should never have to scrape a listing laid out for reading.
func TestPrintTargetsJSONIsParseable(t *testing.T) {
	targets := []Target{
		{Command: "deploy", Func: "Deploy", Doc: "Deploy ships it", File: "deploy.go"},
		{Command: "secrets push", Func: "Push", Namespace: "secrets", File: "secrets/push.go"},
	}
	out := captureOutput(t, func() {
		if err := printTargetsJSON(targets, []string{"deploy.go: Helper(int) string"}); err != nil {
			t.Fatal(err)
		}
	})

	var got listJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(got.Targets) != 2 {
		t.Fatalf("targets = %+v", got.Targets)
	}
	if got.Targets[0].Command != "deploy" || got.Targets[1].Namespace != "secrets" {
		t.Errorf("targets = %+v", got.Targets)
	}
	if len(got.Rejected) != 1 {
		t.Errorf("a function that is ALMOST a target must be reported, not dropped: %+v", got.Rejected)
	}

	// The dispatch details are implementation. Emitting them would make the Go
	// identifier and the signature a promise to every consumer.
	if strings.Contains(out, "Deploy") && !strings.Contains(out, "Deploy ships it") {
		t.Errorf("the Go identifier leaked into the payload:\n%s", out)
	}
}

// TestPrintTargetsJSONNeverNull. A consumer iterating the field should not
// have to special-case a definition that exposes nothing.
func TestPrintTargetsJSONNeverNull(t *testing.T) {
	out := captureOutput(t, func() {
		if err := printTargetsJSON(nil, nil); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, "null") {
		t.Errorf("empty targets rendered as null:\n%s", out)
	}
	var got listJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Targets == nil {
		t.Error("Targets is nil rather than an empty array")
	}
}

func TestPrintCacheJSON(t *testing.T) {
	isolateCache(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "proj-abcd1234")
	if err := os.WriteFile(bin, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(bin, definition{Dir: dir, Hash: "abcd1234"},
		[]Target{{Command: "deploy"}}); err != nil {
		t.Fatal(err)
	}
	m, err := readManifest(bin)
	if err != nil {
		t.Fatal(err)
	}

	entries := []cacheEntry{
		{Path: bin, Size: 6, Manifest: m, HasManifest: true, ProjectExists: true},
		// Predates manifests: it must still be listed, with the optional
		// fields simply absent.
		{Path: filepath.Join(dir, "old-ffff0000"), Size: 4},
	}
	out := captureOutput(t, func() {
		if err := printCacheJSON(entries); err != nil {
			t.Fatal(err)
		}
	})

	var got cacheJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if got.TotalBytes != 10 {
		t.Errorf("TotalBytes = %d, want the sum", got.TotalBytes)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries = %+v", got.Entries)
	}
	if got.Entries[0].Hash != "abcd1234" || got.Entries[0].Built == nil {
		t.Errorf("manifest fields missing: %+v", got.Entries[0])
	}
	if got.Entries[1].Built != nil {
		t.Errorf("an entry with no manifest carries a build time: %+v", got.Entries[1])
	}
}

// TestListJSONThroughTheDispatch. The flag has to work where a user types it.
func TestListJSONThroughTheDispatch(t *testing.T) {
	needsGo(t)
	useLocalEngine(t)
	isolateCache(t)
	t.Chdir(t.TempDir())

	var code int
	captureOutput(t, func() { code = run([]string{string(VerbInit)}) })
	if code != exitOK {
		t.Fatalf("init exited %d", code)
	}

	out := captureOutput(t, func() { code = run([]string{string(VerbList), jsonFlag}) })
	if code != exitOK {
		t.Fatalf("list --json exited %d:\n%s", code, out)
	}
	var got listJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not valid JSON: human output leaked in:\n%s", out)
	}
	if len(got.Targets) == 0 {
		t.Error("the scaffolded definition reported no targets")
	}
}
