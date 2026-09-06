package main

import (
	"context"
	"fmt"

	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
)

// NotifySlack announces a deploy to a channel.
//
// Why it lives in this repository rather than in the tool: it is specific to
// how this team works, and baking every team's notification preferences into
// the shared step library is how a small tool becomes a platform. It satisfies
// pipeline.Step, so it composes with the shipped primitives exactly as they
// compose with each other. No shell-out escape hatch required.
//
// Placed AFTER wait-healthy and BEFORE the traffic switch on purpose: announcing a
// deploy that then fails its health gate is worse than announcing nothing.
type NotifySlack struct {
	// Channel is the destination, including the leading '#'.
	Channel string
}

func (NotifySlack) Name() string { return "notify-slack" }

// Requires lists both keys Run reads. Under-reporting here would defeat
// Validate and reintroduce the failure it exists to prevent.
func (NotifySlack) Requires() []pipeline.Key {
	return []pipeline.Key{common.KeyImage, common.KeyEnv}
}

func (NotifySlack) Provides() []pipeline.Key { return nil }

func (n NotifySlack) Run(_ context.Context, s *pipeline.State) error {
	if n.Channel == "" {
		return fmt.Errorf("notify-slack: Channel is required")
	}
	image, err := pipeline.Get[string](s, common.KeyImage)
	if err != nil {
		return fmt.Errorf("notify-slack: %w", err)
	}
	env, err := pipeline.Get[string](s, common.KeyEnv)
	if err != nil {
		return fmt.Errorf("notify-slack: %w", err)
	}
	// A dry run must not post: the message is a real side effect visible to
	// other people, which is exactly what dry run promises to withhold.
	if s.DryRun() {
		s.Detailf("would post to %s: deploying %s to %s", n.Channel, image, env)
		return nil
	}
	s.Detailf("%s: deploying %s to %s", n.Channel, image, env)
	return nil
}
