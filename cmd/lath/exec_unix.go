//go:build unix

package main

import (
	"fmt"
	"syscall"
)

// handOff replaces this process with the compiled pipeline.
//
// Why exec rather than spawn-and-wait on Unix: the pipeline binary inherits the
// terminal, the signal disposition, and the exit status directly. A wrapper
// process in between would have to forward SIGINT correctly to avoid leaving a
// half-finished deploy running after Ctrl-C, and getting that wrong is a
// well-known source of orphaned work.
//
// Invariant: on success this never returns.
func handOff(binPath string, args []string) error {
	argv := append([]string{binPath}, args...)
	env, err := definitionEnv()
	if err != nil {
		return err
	}
	if err := syscall.Exec(binPath, argv, env); err != nil {
		return fmt.Errorf("exec %s: %w", binPath, err)
	}
	return nil
}
