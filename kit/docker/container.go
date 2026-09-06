package docker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ubgo/lath/kit/proc"
)

// DefaultStopGrace is how long a container is given to exit on SIGTERM before
// docker kills it. Ten seconds is docker's own default, restated so the value
// is a decision rather than an inheritance.
const DefaultStopGrace = 10 * time.Second

// RunOptions describes a container to start.
type RunOptions struct {
	// Name is the container name.
	Name string
	// Image is what to run.
	Image string
	// Cmd overrides the image's command. Empty uses the entrypoint.
	Cmd []string
	// Network attaches the container. Use HostNetwork to share the host's.
	Network string
	// NetworkAliases are extra DNS names the container answers to on that
	// network.
	//
	// The reason to want one: a container named after the commit it runs is
	// unaddressable by anything written down in advance, because the name
	// changes every release. An alias gives it a STABLE name, so a proxy
	// configuration, or another service, can be written once.
	//
	// Requires a user-defined Network: docker refuses the run on the default
	// bridge and host networking has no DNS to add a name to. An alias given
	// without one is dropped, so a pipeline written for a real stack still
	// rehearses locally.
	//
	// The cost, and it is real: during a rolling deploy the old and new
	// containers both answer to the alias, and docker's DNS returns them in
	// turn, so traffic splits across two releases until the old one stops.
	// Where that is unacceptable, address the container by its exact name and
	// rewrite the configuration each deploy instead.
	NetworkAliases []string
	// EnvFile is a path ON THE RUNNER whose variables the container receives.
	EnvFile string
	// Volumes are -v specifications, e.g. "/srv/app/logs:/app/logs".
	//
	// Host paths must already exist with the right ownership: docker creates a
	// missing one owned by root, and a container running as a non-root user
	// then cannot write to it.
	Volumes []string
	// ExtraHosts are --add-host entries, e.g.
	// "host.docker.internal:host-gateway". Needed on Linux, where that name
	// does not exist by default.
	ExtraHosts []string
	// Port is published on loopback when set.
	//
	// Loopback rather than all interfaces: a container bound to 0.0.0.0 is
	// reachable from the internet regardless of any firewall the proxy sits
	// behind, which is a way to expose a service nobody meant to expose.
	Port int
	// Restart is docker's restart policy. Empty means "unless-stopped", which
	// brings a crashed container back but leaves a deliberately stopped one
	// stopped across a host reboot.
	Restart string
	// StopTimeout is recorded ON the container, so `docker stop` honours it
	// even when run by a human who does not know this process needs longer.
	StopTimeout time.Duration
	// Detach runs the container in the background. Almost always true; false
	// is for a one-off whose output the caller wants.
	Detach bool
	// Remove deletes the container when it exits, for one-off commands, where
	// otherwise one spent container accumulates per run forever.
	Remove bool
	// Env are individual -e values, applied AFTER EnvFile so a caller can
	// override one variable without rewriting the file.
	Env map[string]string
	// User runs the container as a specific uid[:gid] or name.
	User string
	// Workdir sets the working directory inside the container.
	Workdir string
	// Labels are --label values, sorted for a stable command line.
	Labels map[string]string
	// Memory and CPUs are resource limits, e.g. "512m" and "1.5". Empty means
	// unlimited, which is docker's default and rarely what a shared host wants.
	Memory string
	CPUs   string
	// Ports are additional publish specifications, e.g. "127.0.0.1:8080:80".
	// Port is the common single case; this is for anything else.
	Ports []string
	// Extra passes raw flags through, inserted before the image.
	//
	// The same escape hatch ssh.Host carries, for the flag nobody anticipated:
	// --cap-add, --security-opt, --tmpfs, --gpus, --health-cmd, --dns.
	Extra []string
}

// RunArgs renders the docker command line for these options.
//
// Exported so a caller can display exactly what will run, and so the flags can
// be asserted in a test without docker installed.
func (o RunOptions) RunArgs() []string {
	args := []string{cmdRun}
	if o.Detach {
		args = append(args, "-d")
	}
	if o.Remove {
		args = append(args, "--rm")
	}
	if o.Name != "" {
		args = append(args, "--name", o.Name)
	}
	if o.Detach {
		restart := o.Restart
		if restart == "" {
			restart = "unless-stopped"
		}
		args = append(args, "--restart", restart)
	}
	// Aliases need a user-defined network. Docker REFUSES the run otherwise,
	// with "network-scoped aliases are only supported for user-defined
	// networks", and the same is true of host networking, which has no DNS of
	// its own to add a name to.
	//
	// Dropped rather than passed through, because the situation this arises in
	// is a local rehearsal of a pipeline written for a real stack: failing it
	// over a name that nothing on this machine would ever look up turns a
	// working rehearsal into a puzzle. Where the network exists, the alias is
	// emitted and works.
	if o.Network != "" && o.Network != HostNetwork {
		for _, alias := range o.NetworkAliases {
			args = append(args, "--network-alias", alias)
		}
	}
	if o.Network != "" {
		args = append(args, "--network", o.Network)
	}
	if o.EnvFile != "" {
		args = append(args, "--env-file", o.EnvFile)
	}
	for _, v := range o.Volumes {
		args = append(args, "-v", v)
	}
	for _, h := range o.ExtraHosts {
		args = append(args, "--add-host", h)
	}
	// Publishing is meaningless with host networking, the container already
	// shares the host's stack, and docker rejects the flag alongside it.
	if o.Port != 0 && o.Network != HostNetwork {
		args = append(args, "-p", fmt.Sprintf("127.0.0.1::%d", o.Port))
	}
	for _, p := range o.Ports {
		args = append(args, "-p", p)
	}
	// After EnvFile, so an explicit value wins over the file's.
	for _, k := range sortedKeys(o.Env) {
		args = append(args, "-e", k+"="+o.Env[k])
	}
	for _, k := range sortedKeys(o.Labels) {
		args = append(args, "--label", k+"="+o.Labels[k])
	}
	if o.User != "" {
		args = append(args, "--user", o.User)
	}
	if o.Workdir != "" {
		args = append(args, "--workdir", o.Workdir)
	}
	if o.Memory != "" {
		args = append(args, "--memory", o.Memory)
	}
	if o.CPUs != "" {
		args = append(args, "--cpus", o.CPUs)
	}
	if o.StopTimeout > 0 {
		args = append(args, "--stop-timeout", strconv.Itoa(int(o.StopTimeout.Seconds())))
	}
	args = append(args, o.Extra...)
	// The image and its command are always last: everything after the image is
	// the container's argv, not docker's.
	args = append(args, o.Image)
	return append(args, o.Cmd...)
}

// Run starts a container.
func (c Client) Run(ctx context.Context, o RunOptions, opts ...proc.Option) error {
	_, err := c.run(ctx, Program+" "+cmdRun+" "+o.Name, o.RunArgs(), opts...)
	return err
}

// InspectField returns one Go-template field from docker inspect, for anything
// State does not model.
//
// The escape hatch for inspection: State covers what a deploy needs, and a
// caller wanting .Config.Image or .NetworkSettings.IPAddress should not have
// to fork this package to get it.
func (c Client) InspectField(ctx context.Context, name, format string) (string, error) {
	r, err := c.run(ctx, Program+" "+cmdInspect+" "+name, []string{cmdInspect, flagFormat, format, name})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(r.Stdout)), nil
}

// Stop sends SIGTERM and waits up to grace before docker kills the container.
//
// Paired with Remove rather than using `docker rm -f`, which sends SIGKILL
// immediately and accepts no timeout, so a process gets no chance to finish
// an in-flight request. That is the entire point of a drain window.
func (c Client) Stop(ctx context.Context, name string, grace time.Duration) error {
	if grace <= 0 {
		grace = DefaultStopGrace
	}
	_, err := c.run(ctx, Program+" "+cmdStop+" "+name,
		[]string{cmdStop, "--time", strconv.Itoa(int(grace.Seconds())), name})
	return err
}

// Remove deletes a stopped container.
func (c Client) Remove(ctx context.Context, name string) error {
	_, err := c.run(ctx, Program+" "+cmdRemove+" "+name, []string{cmdRemove, name})
	return err
}

// Names lists running containers whose name contains match. An empty match
// lists everything, which is rarely what a deploy wants.
//
// extra passes raw flags through, "--all" to include stopped containers, or
// further --filter expressions.
func (c Client) Names(ctx context.Context, match string, extra ...string) ([]string, error) {
	args := []string{cmdPS, flagFormat, formatNames}
	if match != "" {
		args = append(args, "--filter", "name="+match)
	}
	args = append(args, extra...)
	r, err := c.run(ctx, Program+" "+cmdPS, args)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(r.Stdout), "\n") {
		if n := strings.TrimSpace(line); n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

// Health is docker's healthcheck verdict for a container.
type Health string

// The verdicts docker reports. HealthNone means the image declares no check.
const (
	HealthNone      Health = "none"
	HealthStarting  Health = "starting"
	HealthHealthy   Health = "healthy"
	HealthUnhealthy Health = "unhealthy"
)

// HealthValues is the canonical list, so a caller switching on a verdict can
// verify it covers every case from one place.
var HealthValues = []Health{HealthNone, HealthStarting, HealthHealthy, HealthUnhealthy}

// State is what one inspect reports about a container.
type State struct {
	Running      bool
	Restarting   bool
	RestartCount int
	ExitCode     int
	// Health is the image's own verdict, or HealthNone when it declares no
	// healthcheck.
	//
	// The most valuable field here, and the reason State exists rather than a
	// bare Running bool. A container running supervisord stays up while every
	// process it manages is dead: the container is running, nothing inside it
	// is, and only a declared healthcheck can tell the difference.
	Health Health
}

// inspectFormat asks for exactly the fields State needs, space separated, so
// one call answers the whole question.
// inspectTrue is how docker's --format template renders a true boolean.
//
// A constant rather than a literal at the comparison because it is a WIRE
// FORMAT, not a Go value: it is whatever docker's template engine prints, and
// the two comparisons below must agree about it. Go's own "true" happens to
// match, which is exactly why an inline literal would look like a language
// constant and be silently wrong if docker ever changed.
const inspectTrue = "true"

const inspectFormat = "{{.State.Running}} {{.State.Restarting}} {{.RestartCount}} {{.State.ExitCode}} " +
	"{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}"

// Inspect reports a container's state.
func (c Client) Inspect(ctx context.Context, name string) (State, error) {
	r, err := c.run(ctx, Program+" "+cmdInspect+" "+name, []string{cmdInspect, flagFormat, inspectFormat, name})
	if err != nil {
		return State{}, err
	}

	var st State
	var running, restarting, health string
	if _, err := fmt.Sscan(strings.TrimSpace(string(r.Stdout)),
		&running, &restarting, &st.RestartCount, &st.ExitCode, &health); err != nil {
		return State{}, fmt.Errorf("parsing docker inspect for %s: %w", name, err)
	}
	st.Running = running == inspectTrue
	st.Restarting = restarting == inspectTrue
	st.Health = Health(health)
	return st, nil
}

// Login authenticates to a registry.
//
// The password is passed on STDIN, never as an argument: an argument is visible
// in the process list to every user on the host.
func (c Client) Login(ctx context.Context, registry, username, password string) error {
	_, err := c.run(ctx, Program+" "+cmdLogin+" "+registry,
		[]string{cmdLogin, registry, "-u", username, "--password-stdin"},
		proc.Stdin(strings.NewReader(password)))
	return err
}

// AuthFailure reports whether an error is a credential problem rather than a
// transient one.
//
// Worth distinguishing because the two look identical from outside, and
// retrying bad credentials ten times turns an instant, obvious failure into a
// minute of noise ending in the same message.
func AuthFailure(stderr []byte) bool {
	lower := strings.ToLower(string(stderr))
	for _, marker := range []string{
		"unauthorized", "authentication required", "denied", "forbidden",
		"invalid username or password",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
