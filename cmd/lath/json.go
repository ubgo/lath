package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// jsonFlag switches a listing from human output to machine output.
//
// Why it exists: everything lath prints is shaped for a person, and anything
// consuming it. A script, an editor plugin, an agent proposing a change, has
// to scrape columns that were laid out for reading. A stable JSON shape costs
// almost nothing here and removes a whole class of brittle parsing.
const jsonFlag = "--json"

// takeJSONFlag removes jsonFlag from args, reporting whether it was present.
//
// Removed rather than passed through for the same reason as --debug: a target
// parses its own arguments, and a flag it did not define is at best ignored.
func takeJSONFlag(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	found := false
	for _, a := range args {
		if a == jsonFlag {
			found = true
			continue
		}
		out = append(out, a)
	}
	return out, found
}

// targetJSON is one discovered command.
//
// A distinct type rather than marshalling Target directly: Target carries
// fields that describe HOW lath dispatches (the Go identifier, the package
// name, the signature), which are implementation and would become a promise
// the moment they appeared in output. What a consumer needs is the command, a
// description, and where it came from.
type targetJSON struct {
	// Command is what the user types, "deploy", or "secrets push".
	Command string `json:"command"`
	// Namespace is the group, empty for a root target.
	Namespace string `json:"namespace,omitempty"`
	// Doc is the first line of the function's doc comment.
	Doc string `json:"doc,omitempty"`
	// File is where it was found, relative to the definition directory.
	File string `json:"file"`
}

// listJSON is the payload of `lath list --json`.
type listJSON struct {
	// Definition is the directory the targets were discovered in.
	Definition string       `json:"definition"`
	Targets    []targetJSON `json:"targets"`
	// Rejected names exported functions whose signature is not dispatchable.
	// Reported rather than dropped: a function that is ALMOST a target is the
	// confusing case, and a consumer should be able to see it too.
	Rejected []string `json:"rejected,omitempty"`
}

// printTargetsJSON writes the discovered targets as JSON.
func printTargetsJSON(targets []Target, rejected []string) error {
	payload := listJSON{Definition: definitionDir, Rejected: rejected}
	// Never null: a consumer iterating the field should not have to special-case
	// a definition that exposes nothing.
	payload.Targets = make([]targetJSON, 0, len(targets))
	for _, t := range targets {
		payload.Targets = append(payload.Targets, targetJSON{
			Command: t.Command, Namespace: t.Namespace, Doc: t.Doc, File: t.File,
		})
	}
	return writeJSON(payload)
}

// cacheEntryJSON is one compiled definition in the cache.
type cacheEntryJSON struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size_bytes"`
	// Project is the definition directory this was built from, absent for an
	// entry that predates manifests.
	Project string `json:"project,omitempty"`
	Hash    string `json:"hash,omitempty"`
	// ProjectExists reports whether that directory is still there. False means
	// the entry is orphaned and `cache prune` will remove it.
	ProjectExists bool       `json:"project_exists"`
	Targets       []string   `json:"targets,omitempty"`
	Built         *time.Time `json:"built,omitempty"`
	LathVersion   string     `json:"lath_version,omitempty"`
	GoVersion     string     `json:"go_version,omitempty"`
}

// cacheJSON is the payload of `lath cache --json`.
type cacheJSON struct {
	Directory  string           `json:"directory"`
	TotalBytes int64            `json:"total_bytes"`
	Entries    []cacheEntryJSON `json:"entries"`
}

// printCacheJSON writes the cache listing as JSON.
func printCacheJSON(entries []cacheEntry) error {
	payload := cacheJSON{Directory: cacheDir()}
	payload.Entries = make([]cacheEntryJSON, 0, len(entries))
	for _, e := range entries {
		payload.TotalBytes += e.Size
		row := cacheEntryJSON{
			Name: filepath.Base(e.Path), Path: e.Path, Size: e.Size,
			ProjectExists: e.ProjectExists,
		}
		if e.HasManifest {
			built := e.Manifest.Built
			row.Project, row.Hash = e.Manifest.Project, e.Manifest.Hash
			row.Targets, row.Built = e.Manifest.Targets, &built
			row.LathVersion, row.GoVersion = e.Manifest.LathVersion, e.Manifest.GoVersion
		}
		payload.Entries = append(payload.Entries, row)
	}
	return writeJSON(payload)
}

// writeJSON emits v to stdout, indented.
//
// Indented rather than compact: this is read by people at least as often as by
// programs, and `| jq` should not be required to make it legible.
func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
