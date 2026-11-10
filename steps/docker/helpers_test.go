package docker_test

import (
	"fmt"

	"github.com/ubgo/lath/pipeline"
	"github.com/ubgo/lath/steps/common"
)

// newState returns a State in the given mode with the supplied keys preset.
func newState(mode pipeline.Mode, preset map[pipeline.Key]any) *pipeline.State {
	s := pipeline.NewState(mode, nil)
	for k, v := range preset {
		switch typed := v.(type) {
		case string:
			pipeline.Set(s, k, typed)
		case []string:
			pipeline.Set(s, k, typed)
		default:
			panic(fmt.Sprintf("unsupported preset type %T", v))
		}
	}
	return s
}

// deployState is the state a mid-pipeline step expects to find.
func deployState(mode pipeline.Mode) *pipeline.State {
	return newState(mode, map[pipeline.Key]any{
		common.KeyCommit:   "a3f1c2d",
		common.KeyRevision: testRevision,
		common.KeyEnv:      "prod",
		common.KeyImage:    "ghcr.io/acme/app:a3f1c2d",
	})
}

// testRevision is the full commit deployState carries, the one a3f1c2d
// abbreviates.
const testRevision = "a3f1c2d0f1e2d3c4b5a69788f9e0d1c2b3a49586"
