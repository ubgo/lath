// Package out writes task output.
//
// Small on purpose. Tee is absent because it is io.MultiWriter, and a shorter
// name for a stdlib call is a rename, not a primitive.
package out

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// File modes for a log and the directory holding it.
const (
	logFilePerm = 0o644
	logDirPerm  = 0o755
)

// LogFile opens path for appending, creating parent directories.
//
// Three lines of ceremony collapsed, MkdirAll, OpenFile with the right flag
// triple, and a Close that reports its error instead of discarding it. That
// last one matters: a deferred `f.Close()` throws away the flush error, so a
// full disk silently truncates the log a task was written to produce.
func LogFile(path string) (io.WriteCloser, error) {
	return LogFileMode(path, logFilePerm, logDirPerm)
}

// LogFileMode opens a log at explicit modes.
//
// The escape hatch for a log that must not be world-readable, one carrying
// request bodies or tokens, or one a group must be able to read.
func LogFileMode(path string, filePerm, dirPerm os.FileMode) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return nil, fmt.Errorf("out: creating %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, filePerm)
	if err != nil {
		return nil, fmt.Errorf("out: opening %s: %w", path, err)
	}
	return &logFile{File: f, path: path}, nil
}

// logFile wraps os.File so Close names the path it failed on.
type logFile struct {
	*os.File
	path string
}

func (l *logFile) Close() error {
	if err := l.File.Close(); err != nil {
		return fmt.Errorf("out: closing %s: %w", l.path, err)
	}
	return nil
}

// Prefix wraps a writer so every line written is prefixed.
//
// Line-oriented rather than write-oriented: a single Write may carry several
// lines or half of one, and prefixing per Write would produce output that
// looks right in tests and wrong the moment a process writes in fragments.
func Prefix(w io.Writer, prefix string) io.Writer {
	return &prefixWriter{w: w, prefix: []byte(prefix), atLineStart: true}
}

type prefixWriter struct {
	w           io.Writer
	prefix      []byte
	atLineStart bool
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	// The count returned must be len(b), not the bytes actually written, or
	// callers see a short-write error for output that arrived intact.
	consumed := len(b)
	for len(b) > 0 {
		if p.atLineStart {
			if _, err := p.w.Write(p.prefix); err != nil {
				return 0, err
			}
			p.atLineStart = false
		}
		idx := bytes.IndexByte(b, '\n')
		if idx < 0 {
			if _, err := p.w.Write(b); err != nil {
				return 0, err
			}
			return consumed, nil
		}
		if _, err := p.w.Write(b[:idx+1]); err != nil {
			return 0, err
		}
		p.atLineStart = true
		b = b[idx+1:]
	}
	return consumed, nil
}
