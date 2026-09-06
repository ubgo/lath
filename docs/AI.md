# Working on a deploy with an agent

lath was not designed for AI. It turned out to suit it, for one reason worth stating plainly: **an agent editing a lath definition can check its own work, and an agent editing YAML cannot.**

Everything below follows from that.

---

## The problem with an agent editing YAML

A deploy defined in YAML has no verifier. An agent can produce something plausible, correct indentation, real-looking keys, a sensible order, and nothing disagrees with it until the deploy runs. In production. Once.

That is the worst possible loop for a machine, because plausibility is exactly what a language model is good at and correctness is what it needs feedback on. The agent cannot tell a good change from one that merely reads well, so neither can you.

## What a lath definition offers instead

Four checks, escalating in cost, every one runnable on a laptop and machine-readable:

| Check | Catches | Cost |
|---|---|---|
| `go build` | wrong types, misspelled fields, `Timeout: "60s"` is a compile error | instant |
| `Validate()` | wiring: step 6 reads `env`, nothing provides it | before step 1 |
| `lath run <target>` (no `--apply`) | the literal `docker run …` argv each step would execute | seconds |
| `lath run deploy local --apply` | the real thing, every step, against local Docker | minutes |

The fourth is the one that matters. An agent that can rehearse a whole deploy before proposing it is doing engineering; one that cannot is guessing with more confidence than it has earned.

---

## Custom steps are first-class, and that is the point

With a YAML deploy tool an agent is limited to what the tool models. Meet something it does not. A proxy with an unusual admin API, a migration that must inspect a table first, a vendor webhook, and the only escape is a shell blob inside a string: untyped, untested, unverifiable.

Here the escape hatch is the language:

```go
pipeline.Func{
    Label:      "warm-cache",
    Needs:      []pipeline.Key{common.KeyContainers},
    Idempotent: true,
    Do: func(ctx context.Context, s *pipeline.State) error {
        names, err := pipeline.Get[[]string](s, common.KeyContainers)
        if err != nil {
            return err
        }
        s.Detailf("warming %d container(s)", len(names))
        return nil
    },
}
```

That value is subject to the same machinery as every shipped step: `Needs` is checked by `Validate` before anything runs, the closure is type-checked, it honours dry run, it is unit-testable with `FakeDebugger`, and `Idempotent` declares whether replaying it is safe.

⚠️ **`Idempotent` defaults to false, and that default is doing real work here.** A step an agent just wrote is precisely the case where nobody has thought about whether running it twice is safe. Unmarked, a replay asks first.

---

## The repair loop

Debugging and custom code compound. This is the loop that YAML cannot offer:

```
agent adds warm-cache
  → lath tui deploy local --apply
  → steps to 10/14, warm-cache fails
  → reads the error and the state pane: commit, image, containers
  → edits the step
  → presses r
  → replays THAT step against the same inputs
```

The build at step 3 took two minutes. **The retry costs nothing**, because `rerun` restores the state snapshot rather than restarting the run.

That difference is why deploy code rots elsewhere: if iterating on step 10 means paying for steps 1–9 each time, nobody iterates, and the deploy becomes the part of the system no one touches.

The agent does not need the terminal either. `debug.Client` is deliberately UI-free. The panel, a test and a script all drive a session through the same type, so an agent can attach to a live run, read `paused` events, and answer `next` / `rerun` / `quit` itself. See [`pipeline/debug.md`](pipeline/debug.md).

---

## Machine-readable output

```sh
lath list --json     # every target, its namespace, doc, and source file
lath cache --json    # what is compiled, for which project, and when
```

`pipeline.PlanEntry` also carries JSON tags, so a definition can emit its own plan without lath's help.

⚠️ **`list --json` deliberately omits the Go identifier, package and signature.** Those describe how lath dispatches, and putting them in output would make an implementation detail a promise to every consumer. What is emitted is what a caller needs: the command, a description, and where it came from. Exported functions that are *almost* targets appear under `rejected` rather than being dropped. A function that looks like a command and is not is the confusing case, and a consumer should see it too.

---

## The autonomy boundary

The guards lath already has were built for humans and happen to be the right shape for agents:

| An agent may | Why |
|---|---|
| run `deploy local --apply` freely | it touches only local Docker |
| run any target's dry run | it executes nothing |
| attach to a session and step it | a human started the run |
| **propose** a production deploy as a printed argv diff | the change is reviewable as behaviour, not config |

| An agent should not | Guard already in place |
|---|---|
| run `--apply` against production unattended | `--allow-prod` is opt-in per invocation |
| deploy from CI without supervision | `--debug` refuses without a terminal, before any step runs |
| keep going after losing its operator | a disconnected debug client aborts the run |

**Credentials cannot leak into a transcript an agent reads.** `secret.Value` closes `String`, `GoString`, `Format`, `MarshalJSON`, `MarshalText` and `LogValue`; `Reveal()` is the only door, and it is named to be greppable. The state pane an agent inspects renders through `fmt`, so a secret shows as its type and length. There is a test asserting the plaintext appears nowhere in the bytes on the wire.

---

## How this compares

Honest about where each one wins.

| | Shell scripts | GitHub Actions (YAML) | [Kamal](https://kamal-deploy.org/) | lath |
|---|---|---|---|---|
| Runs on a laptop | yes | **no**, needs a runner | yes | yes |
| Runs in CI | yes | yes | yes | yes, CI installs it and runs one command |
| Mistakes caught before running | none | none | schema only | **compile + wiring, before step 1** |
| Rehearse the real deploy locally | rarely | no | yes | yes |
| Step through it | no | no | no | **yes, `lath tui`** |
| Retry one step without redoing the rest | no | no | no | **yes, state is snapshotted** |
| Custom logic | native | shell in a string | hooks | **a typed value in the same list** |
| Machine-readable output | none | logs | some | `--json` |
| Zero-downtime deploy out of the box | no | no | **yes** | assemble it yourself |
| Opinionated defaults | no | no | **yes** | no |
| Maturity |, | very | **high** | early |

**Use Kamal if your deploy is ordinary.** It is mature, opinionated, and hands you a working zero-downtime deploy. lath hands you a task runner whose tasks are Go plus the primitives, and expects you to assemble the pipeline. That is worse when the shape is standard and better when it is not: when the ordering is peculiar to you, when a step must talk to something no deploy tool models, or when you want the whole thing verifiable before you trust it.

And deploying is only one task. The same runner replaces the rest of a `scripts/` directory.
