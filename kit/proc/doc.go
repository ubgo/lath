// Package proc runs programs.
//
// It exists because the standard library's os/exec answers a different
// question than a task does. exec.Cmd models "start a process and read its
// streams"; a task asks "run this, did it work, and how long did it take" ,
// and getting from one to the other costs the same thirty lines every time,
// including two type assertions to learn something a shell reports in seven
// characters.
//
// The distinction this package exists to preserve, above all others: a process
// that EXITED non-zero and a process that was KILLED BY A SIGNAL are different
// events. Conflating them turns a supervisor into an infinite loop, because a
// stop request looks exactly like a crash.
//
// Every blocking call takes a context and honours it. Nothing here holds
// package-level state, and nothing outside the standard library is imported.
package proc
