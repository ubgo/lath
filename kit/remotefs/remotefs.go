// Package remotefs performs filesystem operations wherever a Runner points.
//
// The same call writes a file on this machine or on a deploy target, with no
// difference but which Runner it was given.
//
// Why not scp or a local write plus a copy: the content is usually a credential
// rather than a file that already exists, and staging it on disk first would
// put a secret in a temporary file. The exact thing kit/secret exists to
// prevent. Here the bytes travel over the runner's stdin and never touch the
// caller's disk.
package remotefs

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/ubgo/lath/kit/proc"
	"github.com/ubgo/lath/kit/runner"
)

const (
	// Shell is the program these operations run through. POSIX sh, so the same
	// command works on any target. The portable choice, in the same way proc
	// reaches for pgrep and lock for ps.
	Shell = "sh"
	// dirMode is applied to directories this package creates.
	dirMode = 0o755
)

// The two modes WriteFile chooses between, exported for the same reason
// LocalMode is: a caller deciding a mode itself, a step, a definition, must
// be able to mean exactly what this package means by "sensitive", rather than
// writing 0600 somewhere else and hoping the two never diverge.
const (
	// SensitiveMode is owner-only: anything carrying a credential.
	SensitiveMode os.FileMode = 0o600
	// PublicMode is the ordinary mode for content that is not a secret.
	PublicMode os.FileMode = 0o644
)

// WriteFile writes content to path on r, creating parent directories.
//
// The mode follows sensitive: owner-only when true, 0644 when false. Use
// WriteFileMode for anything else.
//
// Invariant: content is base64'd in transit, so newlines, quotes and binary
// data survive a remote shell unharmed. And the umask is set BEFORE the
// redirect, so a sensitive file is never briefly readable between creation and
// a chmod.
func WriteFile(ctx context.Context, r runner.Runner, path, content string, sensitive bool) error {
	mode := PublicMode
	if sensitive {
		mode = SensitiveMode
	}
	return WriteFileMode(ctx, r, path, content, mode)
}

// WriteFileMode writes content at an explicit mode.
//
// The escape hatch for the file that is neither 0600 nor 0644, a script that
// must be executable, a socket directory a group must reach. The umask is
// derived from the mode rather than chosen from two constants, so any mode is
// expressible.
func WriteFileMode(ctx context.Context, r runner.Runner, path, content string, mode os.FileMode) error {
	return WriteFileOwner(ctx, r, path, content, mode, "")
}

// WriteFileOwner writes content at an explicit mode and chowns it to owner
// ("uid:gid"). An empty owner leaves ownership alone and is exactly
// WriteFileMode.
//
// Why it exists: mode alone is not enough for a file another user must read.
// A 0600 credential written by the deploy user and then mounted into a
// container running as a different uid is unreadable inside that container,
// and the failure surfaces as an application error, "no such credential" ,
// rather than as a permission problem, which is a genuinely hard trail to
// follow. Setting the owner at write time is the only moment the file is
// guaranteed to exist and to still be private.
//
// Invariant: the chown happens while the file is already at its final mode, so
// it is never both world-readable and owned by the target user. Like
// MkdirAll's, the chown is best-effort. A host where this user cannot chown,
// or a file that already has the right owner, must not fail the caller.
func WriteFileOwner(ctx context.Context, r runner.Runner, path, content string, mode os.FileMode, owner string) error {
	where := runner.OrLocal(r)

	// The umask is the complement of the requested mode: a file is created
	// 0666 & ^umask, so ^mode yields exactly mode.
	umask := fmt.Sprintf("%03o", (^mode)&0o777)
	// mkdir runs BEFORE the umask is narrowed, and that ordering is the whole
	// subtlety here. A umask of 177, the complement of 0600, strips the
	// execute bit from anything it creates, so a directory made under it
	// cannot be entered and the redirect that follows fails with a permission
	// error on a path that was just created.
	//
	// The umask still applies to the file, which is what matters: it is set
	// before the redirect, so the file is never briefly readable between
	// creation and a chmod.
	script := fmt.Sprintf("mkdir -p -- %s && umask %s && base64 -d > %s",
		Quote(Dir(path)), umask, Quote(path))
	if owner != "" {
		script += fmt.Sprintf(" && { chown %s -- %s 2>/dev/null || true; }",
			Quote(owner), Quote(path))
	}

	// The bytes appear only on stdin, never in argv, so they stay out of the
	// process list and any shell history.
	res, err := where.Run(ctx, Shell, []string{"-c", script},
		proc.Stdin(strings.NewReader(base64.StdEncoding.EncodeToString([]byte(content)))),
		proc.Capture())
	return runner.Check("write "+path, where, res, err)
}

// MkdirAll creates directories on r, optionally chowning them to owner
// ("uid:gid"). Directories get 0755; use MkdirAllMode for anything else.
//
// Ownership matters because a container running as a non-root user cannot write
// to a host directory owned by root, and the failure surfaces as an application
// error rather than a permission problem.
//
// The chown is best-effort: a directory that already has the right owner, on a
// host where this user cannot chown, must not fail the caller.
func MkdirAll(ctx context.Context, r runner.Runner, owner string, paths ...string) error {
	return MkdirAllMode(ctx, r, owner, 0, paths...)
}

// MkdirAllMode creates directories at an explicit mode. A zero mode uses
// mkdir's default.
func MkdirAllMode(ctx context.Context, r runner.Runner, owner string, mode os.FileMode, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	where := runner.OrLocal(r)

	quoted := make([]string, 0, len(paths))
	for _, p := range paths {
		quoted = append(quoted, Quote(p))
	}
	joined := strings.Join(quoted, " ")

	script := "mkdir -p"
	if mode != 0 {
		script += fmt.Sprintf(" -m %03o", mode&0o777)
	}
	script += " -- " + joined
	if owner != "" {
		script += fmt.Sprintf(" && { chown -R %s -- %s 2>/dev/null || true; }", Quote(owner), joined)
	}
	res, err := where.Run(ctx, Shell, []string{"-c", script}, proc.Capture())
	return runner.Check("mkdir", where, res, err)
}

// ReadFile returns a file's contents from r.
func ReadFile(ctx context.Context, r runner.Runner, path string) ([]byte, error) {
	where := runner.OrLocal(r)
	// base64 on the far side, so binary content survives the transport.
	res, err := where.Run(ctx, Shell, []string{"-c", "base64 < " + Quote(path)}, proc.Capture())
	if err := runner.Check("read "+path, where, res, err); err != nil {
		return nil, err
	}
	decoded, decodeErr := base64.StdEncoding.DecodeString(
		strings.ReplaceAll(strings.TrimSpace(string(res.Stdout)), "\n", ""))
	if decodeErr != nil {
		return nil, fmt.Errorf("remotefs: decoding %s: %w", path, decodeErr)
	}
	return decoded, nil
}

// Exists reports whether a path exists on r.
func Exists(ctx context.Context, r runner.Runner, path string) (bool, error) {
	where := runner.OrLocal(r)
	res, err := where.Run(ctx, Shell, []string{"-c", "test -e " + Quote(path)}, proc.Capture())
	if err != nil {
		return false, fmt.Errorf("remotefs: %w", err)
	}
	// A non-zero exit means "absent", not "failed": test reports the answer
	// through its status, so treating that as an error would make every
	// negative result a failure.
	return res.OK(), nil
}

// Quote renders s as a single-quoted shell word.
//
// Every path interpolated into a command goes through it. A deploy path is
// usually boring, but it comes from configuration, and a space or a quote in
// one would re-split the command on the far side.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Dir returns the parent of a slash-separated path.
//
// Not filepath.Dir: the target may be Linux while this runs on macOS or
// Windows, so the separator is the remote's, not this machine's.
func Dir(path string) string {
	i := strings.LastIndex(path, "/")
	if i <= 0 {
		return "."
	}
	return path[:i]
}

// LocalMode is the mode a locally-created directory gets, exported so a caller
// mixing remotefs with os can stay consistent.
const LocalMode os.FileMode = dirMode
