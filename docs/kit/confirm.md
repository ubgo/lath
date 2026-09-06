# kit/confirm

A gate for dangerous actions that never hangs in CI.

```go
import "github.com/ubgo/lath/kit/confirm"
```

## Sharing the affirmative set

```go
if confirm.IsAffirmative(answer) { … }
```

For a caller that already **has** the answer. A panel with its own reader, a form, a protocol message. Use it instead of hand-rolling `== "y" || == "yes"`, which is how a codebase ends up accepting different words in different prompts.

Case and surrounding space are ignored; anything unrecognised is no, the same default `Yes` applies.

## API

```go
func Yes(ctx context.Context, prompt string, opts ...Option) (bool, error)
func Phrase(ctx context.Context, prompt, required string, opts ...Option) error

func In(r io.Reader) Option      // default os.Stdin
func Out(w io.Writer) Option     // default os.Stderr
func Assume(yes bool) Option     // answer without asking
```

```go
ok, err := confirm.Yes(ctx, "Deploy to production?")

// For something irreversible, where a reflexive "y" is too cheap
err = confirm.Phrase(ctx, "This will delete the database.", "prod")

// A --yes flag passes on a decision already made
err = confirm.Phrase(ctx, "…", "prod", confirm.Assume(*yesFlag))
```

## The mechanism that earns it a place

⚠️ **It is not the prompt. It is the terminal check.** A prompt that blocks forever in CI is worse than no prompt: the job hangs until a global timeout with nothing in the log explaining the silence. Without a terminal, both functions return `ErrNotInteractive` at once.

`Assume` is how a caller says the decision was already made on the command line, so an automated path never needs a fake stdin.

## Answer handling

`Yes` accepts `y`, `Y`, `yes`, `YES`, with surrounding whitespace. **Everything else is no**, including a bare Enter and EOF. An empty answer, a stray newline from a pipe, or a reflexive Enter must never be the thing that authorises a destructive action. The prompt shows `[y/N]` to say so.

`Phrase` compares **exactly**: no trimming beyond the line ending, no case folding. `PROD` does not pass for `prod`. The point is that the operator typed precisely this, which forces them to look at what they are affecting. The prompt names the required string, or the gate is unpassable rather than deliberate.

**Ctrl-D is a decline, not an error.** An immediate EOF means the operator did not confirm, and since both default to no, returning the empty line produces exactly that. Reporting a read error instead would surface a deliberate refusal as a malfunction.

The prompt goes to **stderr**, so it never contaminates output a caller is piping.

## Errors

| | |
|---|---|
| `ErrDeclined` | the operator said no, or typed the wrong phrase |
| `ErrNotInteractive` | no terminal, and no `Assume` |
