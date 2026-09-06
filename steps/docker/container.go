package docker

import (
	"context"
	"fmt"
	"strings"
	"time"

	dockerkit "github.com/ubgo/lath/kit/docker"
	"github.com/ubgo/lath/kit/proc"
	"github.com/ubgo/lath/kit/runner"
	"github.com/ubgo/lath/kit/wait"
	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
)

const (
	// containerNameSeparator joins a process name to its replica index.
	containerNameSeparator = "-"
	// DefaultHealthTimeout bounds how long a new container may take to answer.
	DefaultHealthTimeout = 60 * time.Second
	// DefaultRunOnceLabel names a RunOnce step that the caller did not label.
	//
	// Deliberately says what the step DOES rather than what it is usually for:
	// a plan reading "migrate" for a cache warm is a plan that lies.
	DefaultRunOnceLabel = "run-once"
	// DefaultSettle is how long a container must stay up before it counts as
	// healthy when the image declares no healthcheck.
	//
	// Not cosmetic: a container that crashes on boot is running for the instant
	// between `docker run` and its first fatal error, so a check that merely
	// asks "is it running" passes in milliseconds and calls a crash-looping
	// deploy a success.
	DefaultSettle = 5 * time.Second
)

// Process describes one service in a release.
//
// Invariant: Name must be unique within a Start, since it becomes part
// of a container name and a collision would make two replicas fight over one
// identity.
type Process struct {
	// Name identifies the process, e.g. "web" or "worker".
	Name string
	// Cmd is the argv this process runs. Empty uses the image's entrypoint.
	Cmd []string
	// Port is the inbound port, published on loopback. Meaningful only when
	// Route is true.
	Port int
	// Alias is a stable DNS name this process answers to on the network,
	// surviving every deploy.
	//
	// Without one, the only name a proxy can use is the container's, which
	// carries the commit and therefore changes each release: the proxy
	// configuration has to be rewritten every time. With one, that
	// configuration is written once and can live in version control.
	//
	// The trade is a brief split: during the drain window the old and new
	// containers both answer to it. See dockerkit.RunOptions.NetworkAliases.
	Alias string
	// Route marks a process that receives inbound traffic, which puts its
	// containers in common.KeyRouted for whatever points a proxy at the
	// release.
	//
	// A worker that dials out leaves this false: it is still started, health
	// checked and retired, it is just never a destination.
	Route bool
	// Scale is the replica count; zero means defaultScale.
	Scale int
	// Volumes are mounts for this process only.
	Volumes []string
	// StopTimeout overrides the deploy-wide grace for this process, for a
	// worker that must finish an in-flight job rather than be killed part-way.
	StopTimeout time.Duration
	// Options is everything else docker run accepts for THIS process, Env,
	// User, Workdir, Labels, Memory, CPUs, Ports, and the Extra escape hatch.
	//
	// Merged over the deploy-wide settings, so a single worker can be given a
	// memory limit or an extra capability without changing the others. Name,
	// Image, Cmd, Volumes and StopTimeout are owned by the fields above and
	// are overwritten here.
	Options dockerkit.RunOptions
}

// replicas returns the effective replica count, applying defaultScale.
func (p Process) replicas() int {
	if p.Scale <= 0 {
		return defaultScale
	}
	return p.Scale
}

// Validate checks one process's configuration.
func (p Process) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("Name is required")
	}
	// A routed process with no port cannot receive anything, so the
	// configuration says one thing and means another.
	if p.Route && p.Port == 0 {
		return fmt.Errorf("process %q is routed but declares no Port", p.Name)
	}
	return nil
}

// RunOnce runs a one-off command in the new image and waits for it to finish.
//
// Separate from Start because it must COMPLETE, and usually must complete
// first: the archetypal use is a schema migration, where starting servers
// against an un-migrated database produces errors that look like application
// bugs. It is not a migration step though, and was called Migrate until that
// name was noticed for what it is. The mechanism is "run something in the
// image just built, on the network, and fail the deploy if it fails"; a
// migration is one thing a caller does with it, alongside seeding, warming a
// cache, uploading assets to a CDN, and smoke-testing the new image before
// traffic reaches it. Naming the mechanism after one of its uses made the
// others look like they needed a step that does not exist.
//
// Nothing here is language- or framework-specific: Cmd is whatever the image
// can run, `{"app", "migrate"}` or `{"npm", "run", "migrate"}` or
// `{"bin/rails", "db:migrate"}`.
type RunOnce struct {
	// Label names this step in plans, the debugger and logs. Empty uses
	// DefaultRunOnceLabel.
	//
	// Exists because a pipeline may hold several of these and a plan listing
	// "run-once" three times says nothing about which is which.
	Label string
	// Cmd is the command to run inside the image.
	Cmd []string
	// EnvFile is a path ON THE RUNNER whose variables the command receives.
	EnvFile string
	// Network attaches the container, so it can reach the database.
	Network string
	// Timeout bounds the migration. Zero means no limit, some migrations are
	// genuinely long, and killing one part-way is worse than waiting.
	Timeout time.Duration
	// Options is everything else docker run accepts, passed through. Image,
	// Cmd, Network, EnvFile and Remove are owned by the fields above.
	Options dockerkit.RunOptions
	// Runner is where it runs, normally the deploy target.
	Runner runner.Runner
}

// Name returns Label, or DefaultRunOnceLabel when the caller set none.
func (r RunOnce) Name() string {
	if r.Label != "" {
		return r.Label
	}
	return DefaultRunOnceLabel
}

// Docs explains the step in a plan. See pipeline.Documented.
func (r RunOnce) Docs() pipeline.Docs {
	return pipeline.Docs{
		Summary: "run one command in the new image, to completion, before anything serves",
		Detail:  strings.Join(r.Cmd, " "),
	}
}

func (RunOnce) Requires() []pipeline.Key { return []pipeline.Key{common.KeyImage} }
func (RunOnce) Provides() []pipeline.Key { return nil }

// Validate checks the author-supplied configuration.
func (r RunOnce) Validate() error {
	if len(r.Cmd) == 0 {
		return fmt.Errorf("Cmd is required")
	}
	return nil
}

func (r RunOnce) Run(ctx context.Context, s *pipeline.State) error {
	image, err := pipeline.Get[string](s, common.KeyImage)
	if err != nil {
		return fmt.Errorf("%s: %w", r.Name(), err)
	}
	client, done := newClient(r.Runner, s)
	defer done()
	opts := r.Options
	opts.Image = image
	opts.Cmd = r.Cmd
	opts.Network = r.Network
	opts.EnvFile = r.EnvFile
	// Removed on exit: the container is spent once the command finishes, and
	// keeping them accumulates one per deploy forever.
	opts.Remove = true

	if s.DryRun() {
		s.Detailf("would run once %s: %s", client.Where().Describe(),
			proc.CommandLine(dockerkit.Program, opts.RunArgs()...))
		return nil
	}
	s.Detailf("running once %s: %s", client.Where().Describe(),
		proc.CommandLine(dockerkit.Program, opts.RunArgs()...))
	if err := client.Run(ctx, opts, timeoutOpts(r.Timeout)...); err != nil {
		return err
	}
	s.Detailf("%s complete", r.Name())
	return nil
}

// Start launches the containers for this release.
//
// Provides common.KeyContainers so later steps know exactly what was started, rather
// than re-deriving it from configuration and risking a different answer, and
// common.KeyRouted for the subset that takes inbound traffic.
type Start struct {
	// Processes are the services to run.
	Processes []Process
	// EnvFile is a path ON THE RUNNER whose variables every container receives.
	EnvFile string
	// Network attaches every container. Use dockerkit.HostNetwork for the host's.
	Network string
	// Volumes are mounted into every container, in addition to each Process's.
	Volumes []string
	// ExtraHosts are --add-host entries. See dockerkit.RunOptions.
	ExtraHosts []string
	// Options applies to EVERY container, Env, User, Labels, Memory, CPUs and
	// the Extra escape hatch. A Process may add or override its own.
	Options dockerkit.RunOptions
	// NamePrefix distinguishes this release's containers from the previous
	// one's, which is what lets StopPrevious tell them apart.
	NamePrefix string
	// Replace removes an existing container that already has the name this
	// step is about to use, instead of failing.
	//
	// Off by default, because the safe reading of "that name is taken" is that
	// something is running and this step does not know what. It is worth
	// turning on when container names are derived from the commit, which is
	// the normal arrangement: redeploying the SAME commit then collides with
	// itself, and docker's refusal (exit 125, "container name is already in
	// use") ends the deploy. That collision means the running container is
	// this very release, so removing it is a restart rather than a swap.
	//
	// The cost is a gap: the old container is gone before the new one is up.
	// That is acceptable precisely because they are the same release, and it
	// is why this is not the default. A rehearsal, which redeploys one commit
	// over and over, wants it; a production pipeline that always ships a new
	// commit never needs it.
	Replace bool
	// Runner is where they run, normally the deploy target.
	Runner runner.Runner
}

func (Start) Name() string { return "start-processes" }

// Docs explains the step in a plan. See pipeline.Documented.
func (p Start) Docs() pipeline.Docs {
	names := make([]string, 0, len(p.Processes))
	for _, process := range p.Processes {
		names = append(names, fmt.Sprintf("%s x%d", process.Name, process.replicas()))
	}
	return pipeline.Docs{
		Summary: "start this release's containers alongside the previous one",
		Detail:  strings.Join(names, ", "),
	}
}
func (Start) Requires() []pipeline.Key {
	return []pipeline.Key{common.KeyImage, common.KeyEnv}
}
func (Start) Provides() []pipeline.Key {
	return []pipeline.Key{common.KeyContainers, common.KeyRouted}
}

// Validate checks every process and rejects duplicate names.
func (p Start) Validate() error {
	if len(p.Processes) == 0 {
		return fmt.Errorf("at least one Process is required")
	}
	seen := make(map[string]bool, len(p.Processes))
	for _, process := range p.Processes {
		if err := process.Validate(); err != nil {
			return err
		}
		if seen[process.Name] {
			return fmt.Errorf("process %q is declared twice", process.Name)
		}
		seen[process.Name] = true
	}
	return nil
}

func (p Start) Run(ctx context.Context, s *pipeline.State) error {
	image, err := pipeline.Get[string](s, common.KeyImage)
	if err != nil {
		return fmt.Errorf("start-processes: %w", err)
	}
	// Falls back rather than failing: the commit only decorates the name, and
	// a deploy should not stop because a decoration is unavailable.
	commit, _ := pipeline.Get[string](s, common.KeyCommit)
	client, done := newClient(p.Runner, s)
	defer done()

	var started, routed []string
	for _, process := range p.Processes {
		for i := range process.replicas() {
			name := p.containerName(process, i, commit)
			started = append(started, name)
			if process.Route {
				routed = append(routed, name)
			}

			if s.DryRun() {
				// The full argv, not a summary: docker run is the longest and
				// least guessable command a deploy issues, mounts, env file,
				// network, restart policy, stop timeout, and a dry run whose
				// most complex step said only "would start X" could not be
				// used to check it, or to reproduce it by hand.
				s.Detailf("would run %s: %s", client.Where().Describe(),
					proc.CommandLine(dockerkit.Program,
						p.runOptions(name, process, image).RunArgs()...))
				continue
			}
			if p.Replace {
				p.replaceExisting(ctx, s, name, process)
			}
			s.Detailf("starting %s %s", name, client.Where().Describe())
			if err := client.Run(ctx, p.runOptions(name, process, image)); err != nil {
				return err
			}
		}
	}

	pipeline.Set(s, common.KeyContainers, started)
	// Set even when empty, and set under a dry run like everything else: a key
	// a step Provides must exist afterwards or a later step's Requires is a
	// lie, and a rehearsal would exercise a different path than the real run.
	pipeline.Set(s, common.KeyRouted, routed)
	s.Detailf("%d container(s): %s", len(started), strings.Join(started, ", "))
	if len(routed) > 0 {
		s.Detailf("%d routed: %s", len(routed), strings.Join(routed, ", "))
	}
	return nil
}

// replaceExisting stops and removes a container already holding this name.
//
// Stopped first, with the same grace the process would get from StopPrevious:
// `docker rm` refuses a running container, and forcing it with -f would kill
// work the process was told it had time to finish.
//
// Errors are ignored, deliberately. The overwhelmingly common case is that
// there is nothing there, which docker reports as an error, and a genuine
// failure to clear the name surfaces one line later as Run refusing it, with
// docker's own message attached. Reporting "no such container" as a deploy
// failure would be worse than saying nothing.
//
// It runs on its OWN client, with no output attached, for the same reason:
// these two commands are speculative, and a step that prints "Error response
// from daemon: No such container" twice on the happy path teaches its reader
// to ignore errors.
func (p Start) replaceExisting(ctx context.Context, s *pipeline.State, name string, process Process) {
	quiet := dockerkit.On(p.Runner)
	_ = quiet.Stop(ctx, name, process.StopTimeout)
	if err := quiet.Remove(ctx, name); err == nil {
		s.Detailf("replaced the existing %s", name)
	}
}

// containerName builds a name unique to this release, so the previous release's
// containers can coexist until traffic has moved.
func (p Start) containerName(process Process, replica int, commit string) string {
	parts := make([]string, 0, 4)
	if p.NamePrefix != "" {
		parts = append(parts, p.NamePrefix)
	}
	parts = append(parts, process.Name)
	if commit != "" {
		parts = append(parts, commit)
	}
	parts = append(parts, itoa(replica))
	return strings.Join(parts, containerNameSeparator)
}

// runOptions merges the deploy-wide settings with this process's own.
// runOptions merges three layers, most specific last: the deploy-wide
// Options, then this Process's Options, then the fields the step owns.
//
// Layered rather than flattened so a caller can set something once for every
// container and still override it for one, which is the shape a real deploy
// needs, workers want a memory limit the web process does not.
func (p Start) runOptions(name string, process Process, image string) dockerkit.RunOptions {
	o := p.Options
	if process.Options.User != "" {
		o.User = process.Options.User
	}
	if process.Options.Workdir != "" {
		o.Workdir = process.Options.Workdir
	}
	if process.Options.Memory != "" {
		o.Memory = process.Options.Memory
	}
	if process.Options.CPUs != "" {
		o.CPUs = process.Options.CPUs
	}
	if process.Options.Restart != "" {
		o.Restart = process.Options.Restart
	}
	o.Env = mergeMap(p.Options.Env, process.Options.Env)
	o.Labels = mergeMap(p.Options.Labels, process.Options.Labels)
	o.Ports = append(append([]string{}, p.Options.Ports...), process.Options.Ports...)
	o.Extra = append(append([]string{}, p.Options.Extra...), process.Options.Extra...)

	// Owned by the step: derived from pipeline state or from the fields above,
	// so anything a caller put here would be silently discarded.
	o.Name = name
	o.Image = image
	o.Cmd = process.Cmd
	o.Network = p.Network
	o.EnvFile = p.EnvFile
	o.ExtraHosts = append(append([]string{}, p.ExtraHosts...), o.ExtraHosts...)
	o.Volumes = append(append([]string{}, p.Volumes...), process.Volumes...)
	o.Port = process.Port
	if process.Alias != "" {
		o.NetworkAliases = append(o.NetworkAliases, process.Alias)
	}
	o.StopTimeout = process.StopTimeout
	o.Detach = true
	return o
}

// mergeMap returns base overlaid with over, without mutating either.
func mergeMap(base, over map[string]string) map[string]string {
	if len(base) == 0 && len(over) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// StopPrevious removes the containers left from the previous release.
//
// Runs last, after the new containers are serving. Stopping first would mean a
// window with nothing running, which is the difference between a deploy and an
// outage.
type StopPrevious struct {
	// Drain is how long to wait after the new release is healthy before
	// stopping the old one, letting in-flight requests finish.
	Drain time.Duration
	// NamePrefix identifies containers this tool manages.
	//
	// Required: without it the step cannot tell its own containers from
	// anything else on the host, and stopping the wrong one is not fixed by
	// retrying.
	NamePrefix string
	// Grace is how long each container gets to exit. Zero means
	// dockerkit.DefaultStopGrace.
	Grace time.Duration
	// ListExtra passes raw flags to the container listing, e.g. "--all" to
	// include stopped containers or a further --filter.
	ListExtra []string
	// Runner is where they are stopped, normally the deploy target.
	Runner runner.Runner
}

func (StopPrevious) Name() string { return "stop-previous" }

// Docs explains the step in a plan. See pipeline.Documented.
func (p StopPrevious) Docs() pipeline.Docs {
	return pipeline.Docs{
		Summary: "retire the previous release, once the new one is healthy",
		Detail:  fmt.Sprintf("containers named %s*, after a %s drain", p.NamePrefix, p.Drain),
	}
}
func (StopPrevious) Requires() []pipeline.Key { return []pipeline.Key{common.KeyContainers} }
func (StopPrevious) Provides() []pipeline.Key { return nil }

// Replayable reports that re-running this step is indistinguishable from
// running it once. Stops containers older than the current release. Once they are
// stopped there are none left to find, so the repeat does nothing.
func (StopPrevious) Replayable() bool { return true }

// Validate checks the author-supplied configuration.
func (sp StopPrevious) Validate() error {
	if sp.NamePrefix == "" {
		return fmt.Errorf("NamePrefix is required, so this step cannot stop containers it does not own")
	}
	return nil
}

func (sp StopPrevious) Run(ctx context.Context, s *pipeline.State) error {
	current, err := pipeline.Get[[]string](s, common.KeyContainers)
	if err != nil {
		return fmt.Errorf("stop-previous: %w", err)
	}
	client, done := newClient(sp.Runner, s)
	defer done()

	// Scoped by prefix, so nothing else on the host is even considered.
	//
	// This is a READ, and it runs under dry run too: naming the containers a
	// real run would retire is most of what makes the rehearsal worth reading.
	// But a rehearsal must not REQUIRE the machine to be able to answer. A dry
	// run on a laptop with no docker installed, or against a host that is
	// unreachable, is a legitimate thing to want — checking a definition is
	// wired correctly is not the same as being ready to deploy — and failing
	// here would make `lath dry-run` unusable in exactly the situations it is
	// most useful. Found by running the gate on a container with no docker
	// binary, where every other step rehearsed cleanly and this one aborted
	// the pipeline.
	names, err := client.Names(ctx, sp.NamePrefix, sp.ListExtra...)
	if err != nil {
		if !s.DryRun() {
			return err
		}
		// Said out loud rather than swallowed: the listing is missing from
		// this rehearsal, and a reader comparing it against a real run needs
		// to know which part could not be answered.
		s.Detailf("cannot list containers here (%v); a real run would retire any %s* that is not in this release",
			err, sp.NamePrefix)
		return nil
	}
	keep := make(map[string]bool, len(current))
	for _, c := range current {
		keep[c] = true
	}
	var stale []string
	for _, n := range names {
		if !keep[n] {
			stale = append(stale, n)
		}
	}
	if len(stale) == 0 {
		s.Detailf("no previous containers to stop")
		return nil
	}
	if s.DryRun() {
		s.Detailf("would drain %s, then stop: %s", sp.Drain, strings.Join(stale, ", "))
		return nil
	}

	if sp.Drain > 0 {
		s.Detailf("draining %s before stopping %d container(s)", sp.Drain, len(stale))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sp.Drain):
		}
	}
	for _, name := range stale {
		s.Detailf("stopping %s", name)
		if err := client.Stop(ctx, name, sp.Grace); err != nil {
			return err
		}
		if err := client.Remove(ctx, name); err != nil {
			return err
		}
	}
	s.Detailf("stopped %d previous container(s)", len(stale))
	return nil
}

// WaitHealthy blocks until every started container is working.
//
// Requires common.KeyContainers rather than re-reading configuration, so it probes
// exactly what was started.
type WaitHealthy struct {
	// Timeout bounds the wait. Zero means DefaultHealthTimeout.
	Timeout time.Duration
	// Probe is a URL to request, with %s replaced by the container name.
	//
	// Empty uses the image's own HEALTHCHECK when it declares one, and falls
	// back to uptime when it does not. Ordered that way because the image knows
	// best what working means, and uptime knows least.
	Probe string
	// Settle is how long a container must stay up when there is no declared
	// healthcheck. Zero means DefaultSettle.
	Settle time.Duration
	// Runner is where the check runs, normally the deploy target.
	Runner runner.Runner
}

func (WaitHealthy) Name() string { return "wait-healthy" }

// Docs explains the step in a plan. See pipeline.Documented.
func (w WaitHealthy) Docs() pipeline.Docs {
	how := "the image's own HEALTHCHECK, or uptime when it declares none"
	if w.Probe != "" {
		how = w.Probe
	}
	return pipeline.Docs{
		Summary: "wait until the new containers are working",
		Detail:  how,
	}
}
func (WaitHealthy) Requires() []pipeline.Key { return []pipeline.Key{common.KeyContainers} }
func (WaitHealthy) Provides() []pipeline.Key { return nil }

// Replayable reports that re-running this step is indistinguishable from
// running it once. Probes and waits. It observes the containers, never changes
// them.
func (WaitHealthy) Replayable() bool { return true }

func (w WaitHealthy) Run(ctx context.Context, s *pipeline.State) error {
	containers, err := pipeline.Get[[]string](s, common.KeyContainers)
	if err != nil {
		return fmt.Errorf("wait-healthy: %w", err)
	}
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = DefaultHealthTimeout
	}
	if s.DryRun() {
		s.Detailf("would probe %d container(s), timeout %s", len(containers), timeout)
		return nil
	}

	for _, name := range containers {
		s.Detailf("probing %s", name)
		if w.Probe != "" {
			url := strings.ReplaceAll(w.Probe, "%s", name)
			if err := wait.HTTPOK(ctx, url, wait.Timeout(timeout)); err != nil {
				return fmt.Errorf("wait-healthy: %s: %w", name, err)
			}
			continue
		}
		// Deliberately NOT newClient: this polls docker inspect once a
		// second for up to the health timeout, and streaming that would bury
		// every other line in the run under identical status output.
		if err := w.waitSettled(ctx, dockerkit.On(w.Runner), name, timeout); err != nil {
			return err
		}
	}
	s.Detailf("%d container(s) healthy", len(containers))
	return nil
}

// waitSettled polls until the container is working, or fails as soon as it is
// clear it will not be.
//
// Prefers the image's declared healthcheck. That preference exists because
// uptime was not enough: a container running supervisord stays up while EVERY
// process it manages is in FATAL state, so a check that asks only about the
// container reports a dead application as a successful deploy.
//
// Without a healthcheck, three conditions stand in: running and not
// restarting; never restarted since the deploy began; and still true after
// Settle.
func (w WaitHealthy) waitSettled(ctx context.Context, client dockerkit.Client,
	name string, timeout time.Duration) error {

	settle := w.Settle
	if settle <= 0 {
		settle = DefaultSettle
	}
	var upSince time.Time

	err := wait.Until(ctx, func(ctx context.Context) (bool, error) {
		st, err := client.Inspect(ctx, name)
		if err != nil {
			return false, err
		}
		if st.Health != dockerkit.HealthNone {
			switch st.Health {
			case dockerkit.HealthUnhealthy:
				return false, fmt.Errorf("%s reports unhealthy", name)
			case dockerkit.HealthHealthy:
				return true, nil
			default:
				return false, nil
			}
		}
		switch {
		case st.Restarting || st.RestartCount > 0:
			// Permanent: a container restarting under `unless-stopped` will do
			// so forever, so waiting cannot change the answer.
			return false, fmt.Errorf("%s is crash-looping (%d restart(s), last exit %d)",
				name, st.RestartCount, st.ExitCode)
		case !st.Running && st.ExitCode != 0:
			return false, fmt.Errorf("%s exited with code %d", name, st.ExitCode)
		case !st.Running:
			upSince = time.Time{}
			return false, nil
		}
		if upSince.IsZero() {
			upSince = time.Now()
		}
		return time.Since(upSince) >= settle, nil
	}, wait.Timeout(timeout), wait.Interval(time.Second))

	if err != nil {
		return fmt.Errorf("wait-healthy: %s did not stay up: %w", name, err)
	}
	return nil
}

// timeoutOpts turns an optional duration into proc options.
func timeoutOpts(d time.Duration) []proc.Option {
	if d <= 0 {
		return nil
	}
	return []proc.Option{proc.Timeout(d)}
}

// defaultScale is the replica count used when a Process leaves Scale at its
// zero value. One, because "unset" must mean "run it", never "run none of it" ,
// a process silently not starting is the worst possible reading of a zero.
const defaultScale = 1

// itoa avoids importing strconv for one small conversion.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
