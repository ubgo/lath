package proc

import "os"

// selfPID reports this process's id. A function rather than a call inline so
// tests can reason about the exclusion in Find without reaching for os.
func selfPID() int { return os.Getpid() }
