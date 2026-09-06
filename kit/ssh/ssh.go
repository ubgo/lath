// Package ssh runs commands on another machine.
//
// Tier 1 despite shelling out to an external program, on the same footing as
// proc's use of pgrep: ssh is a protocol with one near-universal client, and
// the alternative is every project rewriting remote execution.
//
// The package deliberately carries no authentication logic. Keys, agents and
// known_hosts are ssh's own configuration, and duplicating any of it here
// would mean two places to get security wrong.
package ssh

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ubgo/lath/kit/proc"
)

const (
	sshProgram = "ssh"
	scpProgram = "scp"
	// batchModeFlag makes ssh fail instead of prompting. Essential, not
	// optional: a password prompt in an automated task hangs the job until
	// something kills it, with nothing in the log to explain the silence.
	batchModeFlag = "BatchMode=yes"
	// defaultConnectTimeout bounds the handshake only. The command itself is
	// bounded by the context or proc.Timeout.
	defaultConnectTimeout = 10 * time.Second
)

// ErrUnreachable reports that the host did not answer.
var ErrUnreachable = errors.New("ssh: host did not answer")

// ErrNoClient reports that no ssh client is installed.
var ErrNoClient = errors.New("ssh: no ssh client found")

// Host identifies a machine and how to reach it.
//
// Invariant: Addr is required; everything else falls back to ssh's own
// configuration, so a host already described in ~/.ssh/config needs only its
// alias here.
type Host struct {
	Addr string
	User string
	// Port is 0 for ssh's default, so the zero Host is meaningful.
	Port int
	// KeyFile is an identity file. Empty means the agent or ssh's config.
	KeyFile string
	// ConnectTimeout bounds the handshake. Zero means defaultConnectTimeout.
	ConnectTimeout time.Duration
	// Extra passes raw flags through.
	//
	// Deliberate, and not an admission of a leaky abstraction. An exhaustive
	// Host struct would need a field per ssh flag and would still be missing
	// the one someone needs the day they need ProxyJump. Without an escape
	// hatch a caller abandons the package wholesale the first time it does not
	// fit, taking its BatchMode and timeout defaults with them.
	Extra []string
}

// String renders a host as user@addr:port for logs.
func (h Host) String() string {
	s := h.Addr
	if h.User != "" {
		s = h.User + "@" + s
	}
	if h.Port != 0 {
		s += ":" + strconv.Itoa(h.Port)
	}
	return s
}

// args builds the ssh flags for this host, without the command.
func (h Host) args() []string {
	timeout := h.ConnectTimeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	out := []string{
		"-o", batchModeFlag,
		"-o", "ConnectTimeout=" + strconv.Itoa(int(timeout.Seconds())),
	}
	if h.Port != 0 {
		out = append(out, "-p", strconv.Itoa(h.Port))
	}
	if h.KeyFile != "" {
		out = append(out, "-i", h.KeyFile)
	}
	out = append(out, h.Extra...)
	return append(out, h.target())
}

func (h Host) target() string {
	if h.User == "" {
		return h.Addr
	}
	return h.User + "@" + h.Addr
}

// Run executes a command on the host.
//
// Accepts proc.Option rather than a parallel option set, so Timeout, Out,
// Capture and the rest mean the same thing locally and remotely.
//
// The command is passed to the remote login shell as one string, ssh offers
// no other calling convention. A caller interpolating a value into it is
// building a shell command and must quote accordingly; Quote is provided.
func Run(ctx context.Context, h Host, command string, opts ...proc.Option) (proc.Result, error) {
	if h.Addr == "" {
		return proc.Result{}, errors.New("ssh: Host.Addr is required")
	}
	if !proc.Exists(sshProgram) {
		return proc.Result{}, fmt.Errorf("%w: install one to run remote commands", ErrNoClient)
	}
	return proc.Run(ctx, sshProgram, append(h.args(), command), opts...)
}

// Output runs a command and returns its trimmed stdout, treating a non-zero
// exit as an error. The same split proc.Output makes.
func Output(ctx context.Context, h Host, command string, opts ...proc.Option) (string, error) {
	if h.Addr == "" {
		return "", errors.New("ssh: Host.Addr is required")
	}
	if !proc.Exists(sshProgram) {
		return "", fmt.Errorf("%w: install one to run remote commands", ErrNoClient)
	}
	return proc.Output(ctx, sshProgram, append(h.args(), command), opts...)
}

// Copy transfers a local file to the host.
func Copy(ctx context.Context, h Host, localPath, remotePath string, opts ...proc.Option) error {
	if h.Addr == "" {
		return errors.New("ssh: Host.Addr is required")
	}
	if !proc.Exists(scpProgram) {
		return fmt.Errorf("%w: %s is required to copy files", ErrNoClient, scpProgram)
	}

	args := []string{"-o", batchModeFlag}
	if h.Port != 0 {
		// scp spells the port -P, where ssh spells it -p. Getting this wrong
		// silently copies to the default port.
		args = append(args, "-P", strconv.Itoa(h.Port))
	}
	if h.KeyFile != "" {
		args = append(args, "-i", h.KeyFile)
	}
	args = append(args, h.Extra...)
	args = append(args, localPath, h.target()+":"+remotePath)

	r, err := proc.Run(ctx, scpProgram, args, append(opts, proc.Capture())...)
	if err != nil {
		return err
	}
	if !r.OK() {
		return fmt.Errorf("ssh: copying to %s: %s: %s", h, r, strings.TrimSpace(string(r.Stderr)))
	}
	return nil
}

// Reachable reports whether the host accepts a connection and runs a command.
//
// Runs `true` rather than merely opening a socket, so the answer covers
// authentication as well as reachability. A host that accepts TCP but refuses
// the key is not usable, and reporting it as reachable would send the caller
// looking in the wrong place.
func Reachable(ctx context.Context, h Host, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	r, err := Run(ctx, h, "true", proc.Capture())
	if err != nil {
		return fmt.Errorf("ssh: %s: %w", h, errors.Join(err, ErrUnreachable))
	}
	if !r.OK() {
		return fmt.Errorf("ssh: %s: %s: %s: %w",
			h, r, strings.TrimSpace(string(r.Stderr)), ErrUnreachable)
	}
	return nil
}

// Quote renders a string as a single-quoted shell word, safe to interpolate
// into a command.
//
// Provided because Run's argument reaches a remote shell, and a caller building
// one from a path or a variable needs a correct way to do it. Single quotes
// with the '\” escape is the only form POSIX shells treat literally
// throughout.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
