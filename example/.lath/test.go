// Package main is this repository's deploy definition.
//
// Every EXPORTED function below is a command. Writing one is declaring it,
// there is no registration list, no switch, and no main(). lath discovers
// them by parsing this directory and generates the dispatcher.
//
// Supported shapes:
//
//	func Name()
//	func Name() error
//	func Name(ctx context.Context) error
//	func Name(args ...string) error
//	func Name(ctx context.Context, args ...string) error
//
// The first sentence of each doc comment becomes its help text in `lath -l`.
//
// This is a nested module, deliberately outside the parent go.work, so its
// dependencies never enter the application's module graph. The editor still
// type-checks it fully. Gopls resolves from the module cache, and vendoring
// only duplicates what is already there.
package main

import "fmt"

func Hello() {
	fmt.Println("hello")
}
