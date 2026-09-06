//go:build !unix

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// handOff runs the compiled pipeline as a child process.
//
// Platforms without exec semantics (Windows) cannot replace the process image,
// so the child's exit code is propagated explicitly. Signal forwarding is left
// to the OS's console-control behaviour, which differs from the Unix path, the
// build tags exist so that difference is visible rather than emulated badly.
//
// Invariant: on success this never returns; it exits with the child's code.
// binarySuffix is what an executable file is called on this platform.
//
// Windows requires it, and omitting it was a real bug rather than a cosmetic
// one: `go build -o <name>` produces <name>.exe, the runner then tried to exec
// <name>, and the run died with "executable file not found in %PATH%" — the
// definition had compiled perfectly and could not be started.
const binarySuffix = ".exe"

func handOff(binPath string, args []string) error {
	cmd := exec.Command(binPath, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	env, err := definitionEnv()
	if err != nil {
		return err
	}
	cmd.Env = env
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}
	if err != nil {
		return fmt.Errorf("run %s: %w", binPath, err)
	}
	os.Exit(exitOK)
	return nil
}
