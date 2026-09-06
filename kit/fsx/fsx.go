// Package fsx holds the file operations with traps in them.
//
// Deliberately small. EnsureDir is absent because it is os.MkdirAll; the test
// for inclusion is whether a function hides a trap, not whether it shortens a
// name.
package fsx

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrSameFile reports a copy whose source and destination are the same file.
//
// Reported rather than quietly skipped: reaching it means two paths that were
// meant to differ resolved to one, and the caller needs to know that. Before
// this check the open-with-truncate destroyed the source before reading it.
var ErrSameFile = errors.New("fsx: source and destination are the same file")

// tempFilePattern names the temporary file WriteAtomic renames from. The
// leading dot keeps a crashed run's leftover out of ordinary listings.
const tempFilePattern = ".tmp-*"

// WriteAtomic writes data to path so a reader never sees it half-written.
//
// The trap: os.WriteFile truncates first and then writes, so a reader arriving
// mid-call sees an empty or partial file, and an interrupted write destroys
// the old contents with nothing to show for it. Writing to a temporary file
// and renaming makes the switch atomic. The reader sees either the old file
// or the new one.
//
// The temporary file is created in the SAME directory on purpose: rename is
// only atomic within one filesystem, and /tmp is frequently a different one.
func WriteAtomic(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, tempFilePattern)
	if err != nil {
		return fmt.Errorf("fsx: creating a temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Removed on every failure path. Without this a full disk or a permission
	// error leaves a .tmp-* file behind on every attempt.
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("fsx: writing %s: %w", tmpName, err)
	}
	// Chmod before the rename so the file never exists at its final path with
	// the wrong mode, however briefly.
	if err = tmp.Chmod(perm); err != nil {
		return fmt.Errorf("fsx: chmod %s: %w", tmpName, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("fsx: closing %s: %w", tmpName, err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		// Windows refuses to replace a destination carrying the read-only
		// attribute, which is what Go's Chmod sets for any mode without a
		// write bit. So a file written with 0400 — a certificate, a key, a
		// config nobody should edit — could be created and then never
		// atomically updated again, failing with "Access is denied" while the
		// caller holds every permission it needs.
		//
		// Only attempted when the destination is genuinely non-writable, so a
		// real permission failure is still reported as one. On unix the rename
		// succeeds first time and this never runs.
		if !clearReadOnly(path) {
			return fmt.Errorf("fsx: renaming to %s: %w", path, err)
		}
		if err = os.Rename(tmpName, path); err != nil {
			return fmt.Errorf("fsx: renaming to %s: %w", path, err)
		}
	}
	return nil
}

// clearReadOnly makes an existing path writable, reporting whether it changed
// anything. Used only to retry a rename that a read-only destination refused;
// the file is about to be replaced, so nothing is lost by relaxing it.
func clearReadOnly(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode().Perm()&0o200 != 0 {
		return false
	}
	return os.Chmod(path, info.Mode().Perm()|0o200) == nil
}

// CopyFile copies src to dst, preserving the source's mode.
//
// Streamed rather than read-then-write so a large file does not have to fit in
// memory.
func CopyFile(src, dst string) error { return CopyFileMode(src, dst, 0) }

// CopyFileMode copies at an explicit destination mode. A zero perm keeps the
// source's, which is what CopyFile does.
//
// Exists because "the same mode as the source" is not always right: a config
// copied out of a repository is 0644 there and must be 0600 where it lands.
func CopyFileMode(src, dst string, perm os.FileMode) (err error) {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("fsx: %s: %w", src, err)
	}
	if perm == 0 {
		perm = info.Mode()
	}
	// Checked BEFORE opening the destination: O_TRUNC below empties the file,
	// so if dst is src the content is gone before the copy reads a byte.
	// Hardlinks and symlinks to the same inode count, which is why this is
	// os.SameFile and not a string comparison of the paths.
	if dstInfo, statErr := os.Stat(dst); statErr == nil && os.SameFile(info, dstInfo) {
		return fmt.Errorf("fsx: %s and %s: %w", src, dst, ErrSameFile)
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("fsx: %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("fsx: %s: %w", dst, err)
	}
	// Close on the named return: a deferred plain Close discards the flush
	// error, so a failed final write reports success.
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("fsx: closing %s: %w", dst, cerr)
		}
	}()

	if _, err = io.Copy(out, in); err != nil {
		return fmt.Errorf("fsx: copying %s to %s: %w", src, dst, err)
	}
	return nil
}

// Exists reports whether a path exists.
//
// Returns an error for anything other than not-exist, a permission failure is
// not the same as absence, and collapsing the two makes a task report "the
// file is missing" when the truth is "you may not look".
func Exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("fsx: %s: %w", path, err)
}
