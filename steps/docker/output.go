package docker

import (
	dockerkit "github.com/ubgo/lath/kit/docker"
	"github.com/ubgo/lath/kit/runner"
	"github.com/ubgo/lath/pipeline"
)

// client returns a docker client whose output flows into the run's report.
//
// Every step in this package builds its client through here rather than
// calling dockerkit.On directly, so no step can accidentally be the silent
// one. Before this existed `docker build` printed nothing for two minutes,
// which is indistinguishable from a hang; a step that forgot to opt in would
// reintroduce exactly that, and nothing would fail to say so.
//
// The returned closer MUST be called, deferring it is the intended shape ,
// or a final line without a trailing newline is lost.
func newClient(r runner.Runner, s *pipeline.State) (dockerkit.Client, func()) {
	// A local run whose output goes straight to a terminal gets the terminal,
	// so docker renders the live progress table it reserves for one instead of
	// a flat log. See pipeline.State.Passthrough for why capturing the output
	// is what would prevent that, and dockerkit.WithTerminal for the trade.
	//
	// Remote steps are excluded: the output arrives over ssh, so there is no
	// local terminal on the far end to render into, and the bytes have to come
	// back as lines either way.
	if f := s.Passthrough(); f != nil && runner.OrLocal(r).Describe() == localDescription {
		return dockerkit.On(r).WithTerminal(f), func() {}
	}
	out := s.Output()
	return dockerkit.On(r).WithOutput(out), func() { _ = out.Close() }
}

// localDescription is what a local runner calls itself. Compared rather than
// type-asserted so a caller's own local-equivalent runner behaves the same.
var localDescription = runner.Local{}.Describe()
