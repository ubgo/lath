package docker

import "testing"

// TestEverySubcommandIsClassified is the drift guard for the one decision that
// is now keyed on a subcommand name: whether a command draws on the operator's
// terminal or is captured. A new subcommand added to this package without a
// line here is a command whose capture behaviour nobody chose.
//
// The table restates the classification independently of progressCommands, so
// the two must agree; asserting the map against itself would prove nothing.
func TestEverySubcommandIsClassified(t *testing.T) {
	t.Parallel()
	// draws reports whether the subcommand renders progress worth a real
	// terminal. Everything false is captured, so its errors quote docker and
	// the output this package parses actually arrives.
	classified := map[string]bool{
		cmdBuild:   true,
		cmdBuildx:  true,
		cmdPush:    true,
		cmdPull:    true,
		cmdRun:     false,
		cmdStop:    false,
		cmdRemove:  false,
		cmdPS:      false,
		cmdInspect: false,
		cmdLogin:   false,
		cmdPrune:   false,
	}
	for name, draws := range classified {
		if progressCommands[name] != draws {
			t.Errorf("subcommand %q: progressCommands says %v, the classification says %v",
				name, progressCommands[name], draws)
		}
	}
	for name := range progressCommands {
		if _, ok := classified[name]; !ok {
			t.Errorf("progressCommands names %q, which no subcommand constant declares", name)
		}
	}
}
