// Package docker drives the docker CLI.
//
// Plain functions over a runner.Runner. No pipeline, no framework, nothing
// from lath beyond the kit. Any Go program can import it to build an image,
// start a container, or reclaim disk, whether or not it uses a task runner.
//
// Every operation takes a Runner, so the same call builds locally or on a
// deploy target with no change but which Runner it was given.
package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ubgo/lath/kit/proc"
	"github.com/ubgo/lath/kit/runner"
)

// Program is the CLI these functions drive. Named once so a drop-in
// replacement, podman, nerdctl, is one edit rather than a search.
const Program = "docker"

// HostNetwork is docker's name for sharing the host's network stack.
const HostNetwork = "host"

// The subcommands this package drives. Named because args[0] is no longer just
// a word on a command line: it decides whether a command gets the operator's
// terminal or is captured (see progressCommands). A literal in the builder and
// a matching literal in that map is a decision spread across two places, and
// the failure mode of a typo is not a crash but a command that quietly stops
// being captured.
const (
	cmdBuild   = "build"
	cmdBuildx  = "buildx"
	cmdPush    = "push"
	cmdPull    = "pull"
	cmdRun     = "run"
	cmdStop    = "stop"
	cmdRemove  = "rm"
	cmdPS      = "ps"
	cmdInspect = "inspect"
	cmdLogin   = "login"
	cmdPrune   = "prune"
	cmdImage   = "image"
	cmdList    = "ls"
)

// flagFormat selects docker's Go-template output. Named because every use of
// it is paired with one of the templates below, and the pairing is what makes
// the output parseable.
const flagFormat = "--format"

// Docker's own output templates. WIRE FORMATS, not Go values: each is
// evaluated by docker's template engine, and the parsing beneath each call
// depends on exactly this shape. Named so a reader can see at a glance which
// field a listing is asking for, and so changing one is a deliberate edit
// rather than a typo in a string literal.
const (
	formatTags  = "{{.Tag}}"
	formatNames = "{{.Names}}"
)

// tagSeparator divides a repository from its tag in an image reference.
//
// A single character with an outsized trap attached: a registry host may
// carry a port, so a reference can hold TWO of these. Splitting from the left
// yields the port number as the tag, which is why TagOf splits from the right
// and why nothing should do this by hand.
const tagSeparator = ":"

// Reference joins a repository and a tag into the name docker knows an image
// by.
//
// Trivial enough to inline, which is exactly why it is here: it was already
// inlined in three places across two repositories, and each one had to know
// the separator. One definition means the format has one owner, and TagOf
// below is its provable inverse.
//
// An empty tag yields the bare repository, which docker reads as :latest. That
// is a real thing to want, and never what a deploy wants: see Build, which
// tags by commit precisely so "which image is running" stays answerable.
func Reference(repository, tag string) string {
	if tag == "" {
		return repository
	}
	return repository + tagSeparator + tag
}

// TagOf extracts the tag from an image reference, or an empty string when it
// carries none.
//
// Splits from the RIGHT, because a registry host may include a port,
// "registry:5000/acme/app:a3f1c2d", and the leftmost colon belongs to the
// port. Getting this backwards reports a port number as the running version,
// which is the kind of wrong answer that gets acted on.
func TagOf(reference string) string {
	i := strings.LastIndex(reference, tagSeparator)
	if i < 0 {
		return ""
	}
	// A colon that precedes a path separator belongs to a registry port, not
	// to a tag: "registry:5000/acme/app" is untagged.
	if strings.Contains(reference[i+1:], "/") {
		return ""
	}
	return reference[i+1:]
}

// Client is docker on one machine.
type Client struct {
	where    runner.Runner
	out      io.Writer
	terminal *os.File
}

// On returns a Client that runs on r. A nil Runner means this machine.
func On(r runner.Runner) Client { return Client{where: runner.OrLocal(r)} }

// WithOutput returns a Client that streams every command's output to w as it
// happens, in addition to capturing it for error messages.
//
// Why it is off by default: a library that writes to a stream nobody asked for
// cannot be embedded. Why it exists at all: without it a two-minute build
// prints nothing, which is indistinguishable from a hang and impossible to
// debug from. The caller decides where the noise goes, a terminal, a log
// file, a pipeline's reporter, and this package never assumes.
//
// Capturing continues regardless, so an error still quotes what docker said.
// See proc.Capture, which composes with proc.Out for exactly this.
func (c Client) WithOutput(w io.Writer) Client {
	c.out, c.terminal = w, nil
	return c
}

// WithTerminal connects docker's output DIRECTLY to a terminal, so it renders
// the output it reserves for one.
//
// BuildKit draws a live, self-updating progress table on a TTY and falls back
// to a flat line-per-event log otherwise; docker pull draws progress bars.
// Handing it a pipe. Which is what any wrapping io.Writer produces, including
// the MultiWriter that capturing adds, silently downgrades all of that, and
// the result looks worse than running the same command by hand.
//
// The cost is real and is the reason this is separate from WithOutput: for the
// subcommands that get the terminal, **output is NOT captured**, because
// capturing is precisely what would reintroduce the pipe, so an error from one
// of them cannot quote what docker said. That is an acceptable trade only when
// the operator is watching the terminal it went to. Which is the one situation
// this exists for.
//
// It therefore applies only to the subcommands that draw progress; see
// progressCommands. Everything else is captured and echoed to f as usual, so
// its errors still quote docker and the output this package parses still
// arrives.
func (c Client) WithTerminal(f *os.File) Client {
	c.terminal, c.out = f, nil
	return c
}

// streamTo is where a CAPTURED command's output should also be echoed, or nil
// for none. Exactly one of the two fields is ever set; see WithOutput and
// WithTerminal.
func (c Client) streamTo() io.Writer {
	if c.terminal != nil {
		return c.terminal
	}
	if c.out != nil {
		return c.out
	}
	return nil
}

// Where reports the runner, for messages that need to name the machine.
func (c Client) Where() runner.Runner { return c.where }

// Available reports whether the docker CLI can be found. Local only: a remote
// answer needs a round trip, which callers should make deliberately.
func Available() bool { return proc.Exists(Program) }

// progressCommands are the subcommands that draw something worth a real
// terminal: BuildKit's live table, and the layer progress bars of a transfer.
// Every other subcommand either prints a line or two or prints output this
// package PARSES, and handing those a terminal costs the capture that error
// messages and parsing both depend on for nothing in return.
//
// Keyed on the subcommand rather than on the calling method so a new method
// gets the safe treatment by default: forgetting to add a name here yields a
// captured command, while the reverse would yield an unreadable one.
var progressCommands = map[string]bool{
	cmdBuild:  true,
	cmdBuildx: true,
	cmdPush:   true,
	cmdPull:   true,
}

// run executes a docker subcommand and turns a non-zero exit into an error.
//
// The terminal, when one was given, applies ONLY to the progress-drawing
// subcommands. This is not an optimisation. `docker run` that exits 125 says
// why on stderr, and in terminal mode that text goes straight to the screen
// and is never captured, so the error reads "exit 125 after 55ms:" with
// nothing after the colon. Worse, docker inspect and docker ps are parsed from
// captured stdout, which in terminal mode is empty, so they quietly answer
// "no such container". Both were real: the first cost an operator a failed
// deploy with no message to act on.
func (c Client) run(ctx context.Context, what string, args []string, opts ...proc.Option) (proc.Result, error) {
	if c.terminal != nil && len(args) > 0 && progressCommands[args[0]] {
		// Deliberately WITHOUT Capture: os/exec passes an *os.File straight to
		// the child, and any wrapper, a MultiWriter included, turns that
		// into a pipe, which is what makes docker drop to its plain output.
		// See WithTerminal.
		opts = append(opts, proc.Out(c.terminal))
	} else {
		opts = append(opts, proc.Capture())
		// In terminal mode the terminal is also where the operator is looking,
		// so a captured command still streams there: it just goes through a
		// pipe, which these subcommands do not care about.
		if w := c.streamTo(); w != nil {
			opts = append(opts, proc.Out(w))
		}
	}
	r, err := c.where.Run(ctx, Program, args, opts...)
	return r, runner.Check(what, c.where, r, err)
}

// ── images ───────────────────────────────────────────────────────────────

// BuildOptions describes an image build.
type BuildOptions struct {
	// Tag is the full image reference, including tag.
	Tag string
	// Context is the build context directory.
	Context string
	// Dockerfile is the recipe path, relative to Context. Empty uses docker's
	// default.
	Dockerfile string
	// Platforms builds for other architectures, e.g. "linux/amd64". Empty
	// builds for the host's. Cross-building needs buildx and an emulator, so
	// an empty value is the fast path and setting it is a deliberate cost.
	Platforms []string
	// Args are --build-arg values.
	//
	// Never put a credential here: build arguments are recorded in the image's
	// history and readable by anyone who can pull it.
	Args map[string]string
	// Target builds a specific stage of a multi-stage Dockerfile.
	Target string
	// Labels are --label values, sorted for a stable command line.
	Labels map[string]string
	// NoCache rebuilds every layer.
	NoCache bool
	// Pull refreshes the base image even when a local copy exists.
	Pull bool
	// Extra passes raw flags through, inserted before the build context.
	//
	// Deliberate, and the same escape hatch ssh.Host carries. An exhaustive
	// struct would need a field per docker flag and would still be missing the
	// one somebody needs the day they need --secret, --cache-from or --ssh.
	// Without it a caller abandons this package the first time it does not
	// fit, losing every guarantee it provides along with the gap.
	Extra []string
}

// BuildArgs renders the docker command line for these options.
//
// Exported so a caller can show exactly what will run, and so it can be
// asserted in a test without docker installed, which is where the flags that
// review misses actually get checked.
func (o BuildOptions) BuildArgs() []string {
	var args []string
	if len(o.Platforms) > 0 {
		// buildx is required for multi-platform. Plain `docker build` cannot
		// do it and fails with a message that does not say so.
		args = append(args, cmdBuildx, cmdBuild, "--platform", strings.Join(o.Platforms, ","))
	} else {
		args = append(args, cmdBuild)
	}
	args = append(args, "-t", o.Tag)
	if o.Dockerfile != "" {
		args = append(args, "-f", o.Dockerfile)
	}
	for _, k := range sortedKeys(o.Args) {
		args = append(args, "--build-arg", k+"="+o.Args[k])
	}
	for _, k := range sortedKeys(o.Labels) {
		args = append(args, "--label", k+"="+o.Labels[k])
	}
	if o.Target != "" {
		args = append(args, "--target", o.Target)
	}
	if o.NoCache {
		args = append(args, "--no-cache")
	}
	if o.Pull {
		args = append(args, "--pull")
	}
	args = append(args, o.Extra...)
	// The context is always last: docker takes it as the sole positional
	// argument, so anything appended after it becomes a second one.
	return append(args, o.Context)
}

// Build builds an image.
func (c Client) Build(ctx context.Context, o BuildOptions) error {
	_, err := c.run(ctx, Program+" "+cmdBuild, o.BuildArgs())
	return err
}

// Push publishes an image to its registry. extra passes raw flags through,
// e.g. "--all-tags" or "--quiet".
func (c Client) Push(ctx context.Context, image string, extra ...string) error {
	_, err := c.run(ctx, Program+" "+cmdPush, append(append([]string{cmdPush}, extra...), image))
	return err
}

// Pull fetches an image. extra passes raw flags through, e.g.
// "--platform=linux/amd64".
func (c Client) Pull(ctx context.Context, image string, extra ...string) error {
	_, err := c.run(ctx, Program+" "+cmdPull, append(append([]string{cmdPull}, extra...), image))
	return err
}

// Images lists the tags of one repository present on this machine, newest
// first.
//
// The question it answers is "what could I run without pulling", which for a
// deploy is "what could I roll back to". The registry can answer a fuller
// version of it, at the cost of a credential and a round trip; the machine
// answers the one that matters, because an image that is here will start
// whatever the registry currently thinks.
//
// Untagged images are skipped: they cannot be run by name, so offering them as
// candidates would be offering something that does not work.
func (c Client) Images(ctx context.Context, repository string) ([]string, error) {
	// Sorted by creation and formatted to tags alone, so the caller gets an
	// answer rather than a table to parse.
	args := []string{cmdImage, cmdList, repository, flagFormat, formatTags}
	r, err := c.run(ctx, Program+" "+cmdImage+" "+cmdList+" "+repository, args)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(r.Stdout), "\n") {
		tag := strings.TrimSpace(line)
		if tag == "" || tag == untaggedTag {
			continue
		}
		out = append(out, tag)
	}
	return out, nil
}

// untaggedTag is what docker prints for an image with no tag. Skipped by
// Images: a name that cannot be run is not a candidate.
const untaggedTag = "<none>"

// Errors RemoveImage distinguishes, so a caller sweeping old versions can tell
// the three outcomes apart instead of matching on docker's prose.
//
// Both are ordinary results of a retention sweep rather than faults: an image
// already gone is the sweep's own goal reached early, and an image still in
// use is the safety property that stops a sweep deleting what is serving.
var (
	// ErrNoSuchImage reports a reference docker does not have. Removing it
	// twice is not a failure, which is what lets a sweep be re-run after it
	// was interrupted.
	ErrNoSuchImage = errors.New("docker: no such image")
	// ErrImageInUse reports an image a container still holds. Docker refuses
	// the removal, and that refusal is worth catching rather than forcing: the
	// container holding it is usually the release currently serving.
	ErrImageInUse = errors.New("docker: image is used by a container")
)

// Docker's own refusal text, matched to classify a removal. WIRE FORMAT: these
// are strings docker prints, not values this package chooses, which is why
// they are pinned here with a test rather than inlined at the comparison.
const (
	stderrNoSuchImage = "no such image"
	stderrImageInUse  = "is being used by"
)

// RemoveImage deletes one image from a machine.
//
// Why it exists: Prune reclaims only DANGLING images, so tagged ones
// accumulate forever. That accumulation is deliberate, it is what makes a
// rollback possible, but it is not free, and nothing else in this package can
// end it. Removing a specific reference is the mechanism a retention policy
// needs; the policy itself, how many to keep and which, belongs to the caller
// that knows what its tags mean.
//
// Invariant: never forces. Docker refuses to remove an image a container
// holds, and that refusal is the only thing standing between a retention
// sweep and the release currently serving. A caller that genuinely wants the
// force flag can pass it through extra and owns the consequence.
//
// Errors are classified: ErrNoSuchImage when it is already gone, ErrImageInUse
// when a container holds it. Both let a sweep continue rather than abort.
func (c Client) RemoveImage(ctx context.Context, reference string, extra ...string) error {
	args := append([]string{cmdImage, cmdRemove, reference}, extra...)
	r, err := c.run(ctx, Program+" "+cmdImage+" "+cmdRemove+" "+reference, args)
	if err == nil {
		return nil
	}
	// Classified from docker's own output rather than the exit code, which is
	// 1 for every one of these.
	switch said := strings.ToLower(string(r.Stderr)); {
	case strings.Contains(said, stderrNoSuchImage):
		return fmt.Errorf("%s: %w", reference, ErrNoSuchImage)
	case strings.Contains(said, stderrImageInUse):
		return fmt.Errorf("%s: %w", reference, ErrImageInUse)
	}
	return err
}

// PruneOptions describes what to reclaim.
type PruneOptions struct {
	// Target is what to prune: image, volume, network, container, or system.
	// Empty means image.
	Target string
	// All removes everything unused, not only dangling. For images that is the
	// difference between reclaiming a few layers and reclaiming every image no
	// container currently references, much more space, and a much slower next
	// build.
	All bool
	// Filter are --filter expressions, e.g. "until=24h" or "label!=keep".
	Filter []string
	// Interactive drops the -f flag, letting docker ask for confirmation.
	//
	// Off by default, and the default is the safe one: with no -f docker reads
	// a confirmation from stdin, and in a pipeline nothing is attached to it,
	// so the command hangs rather than failing. Set this only when a human is
	// watching a terminal.
	Interactive bool
	// Extra passes raw flags through.
	Extra []string
}

// What Prune can reclaim. Named because these are a closed set that a caller
// picks from, and because the step layer labels itself from the chosen target:
// a literal at either site is a label that silently disagrees with the command
// actually issued.
const (
	PruneImages     = "image"
	PruneVolumes    = "volume"
	PruneNetworks   = "network"
	PruneContainers = "container"
	// PruneSystem reclaims everything the others do, plus the build cache.
	PruneSystem = "system"
	// DefaultPruneTarget is what an empty PruneOptions.Target means. Images,
	// because dangling images are what a deploy actually accumulates.
	DefaultPruneTarget = PruneImages
)

// PruneTargets is the canonical set, so a caller validating input and this
// package agree about what is accepted.
var PruneTargets = []string{PruneImages, PruneVolumes, PruneNetworks, PruneContainers, PruneSystem}

// PruneArgs renders the docker command line for these options.
func (o PruneOptions) PruneArgs() []string {
	target := o.Target
	if target == "" {
		target = DefaultPruneTarget
	}
	args := []string{target, cmdPrune}
	if !o.Interactive {
		args = append(args, "-f")
	}
	if o.All {
		args = append(args, "--all")
	}
	for _, f := range o.Filter {
		args = append(args, "--filter", f)
	}
	return append(args, o.Extra...)
}

// Prune reclaims disk, returning docker's summary line.
//
// Hosts run out of disk from accumulated images long before anything else, and
// the resulting failure looks like a build problem rather than a housekeeping
// one.
func (c Client) Prune(ctx context.Context, o PruneOptions) (string, error) {
	r, err := c.run(ctx, Program+" "+o.Target+" "+cmdPrune, o.PruneArgs())
	if err != nil {
		return "", err
	}
	return lastLine(string(r.Stdout)), nil
}

// sortedKeys returns a map's keys in order, so a generated command line is
// identical between runs. An unstable order makes two equivalent builds look
// different in a log and defeats any cache keyed on the command.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// lastLine returns the final non-empty line, which is where docker puts its
// summary.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return "nothing to reclaim"
}
