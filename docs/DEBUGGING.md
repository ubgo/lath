# Stepping through a pipeline

Run a pipeline one step at a time, pausing after each until you say to go on. Replay a step that just ran, inspect what the steps have produced, or stop.

This is the **how-to**. For why it is built the way it is, see [`DEBUGGER.md`](DEBUGGER.md); for the engine it hangs off, [`pipeline/README.md`](pipeline/README.md).

```
lath tui
```

That is the whole thing. It lists your targets, asks which, starts it, and steps you through it, in this terminal, with no flag.

---

## Just want to watch it run?

`lath tui` is for **stepping**. To simply watch a pipeline work, use the ordinary command:

```
lath run deploy local --apply
```

That behaves exactly like any other terminal command. Every line of output, scrolling, no pauses, no panel, nothing truncated or capped. Use it when you want a log; use `lath tui` when you want to stop between steps.

## The three ways in

### 1. Pick a target

Every target is listed, including ones with no pipeline in them. Those simply **run**, with their output streaming here, `lath tui` behaves like `lath run` when there is nothing to step, and exits with the same status. Nothing is hidden from the list, because whether a target builds a pipeline is decided at run time by the code inside it, not by anything lath can see beforehand.

```
$ lath tui

  targets:

     1) deploy local
     2) deploy plan
     3) deploy run
     4) dev guard
     5) secrets build
     6) secrets plan
     7) secrets push

  run [1-7]: 3
  arguments (e.g. prod --apply, or blank): staging

lath: starting deploy run staging
      output: /var/folders/…/T/lath-sessions/run.log
```

### 2. Name it outright

Skips both prompts. The arguments are exactly what you would have typed after `lath run`.

```
lath tui deploy run staging
lath tui deploy local --apply
lath tui secrets push staging
```

### 3. Start it somewhere else, attach here

For when the run has to happen where this terminal is not, another machine, a different working directory, an existing script.

```
# there
lath run deploy run staging --debug

# here
lath tui
```

With one session waiting, `lath tui` attaches straight to it. With several it asks which.

---

## The screen is shared, by taking turns

When `lath tui` starts the run itself in this terminal, the two never draw at once:

- **while a step runs**. The step owns the screen, so `docker build` renders its live `[+] Building 28.8s (14/28)` table exactly as it would if you had typed the command yourself
- **at every pause**. The panel takes the screen back and draws the step list, the state and the prompt

Nothing overlaps, because at any moment exactly one of them is executing.

This applies only when `lath tui` **launched** the run here. Attaching to a run started elsewhere (`lath run … --debug`) sends that run's output to *its* terminal, so the panel renders it as lines instead. There is nothing here to hand over.

## Getting the raw commands

A dry run prints the exact argv each step would execute, shell-quoted and ready to paste. Strip the prefix and you have a script:

```sh
lath run deploy local | grep 'would run' | sed 's/^ *would run locally: //'
```

No `--apply`, so nothing executes. For a remote environment, `lath run deploy run prod --allow-prod` prints the same for the ssh side.

That currently yields:

```sh
docker build -t ghcr.io/acme/app:0f3747e -f .docker/Dockerfile.prod \
  --build-arg BRANCH=dev --build-arg COMMIT=0f3747e --build-arg ENVIRONMENT=local \
  --label org.opencontainers.image.source=https://github.com/acme/app .

docker run -d --name app-local-api-0f3747e-0 --restart unless-stopped \
  --env-file /tmp/app-local/.env.local \
  -v /tmp/app-local/_keys:/app/_keys:ro \
  --add-host host.docker.internal:host-gateway \
  --stop-timeout 86400 ghcr.io/acme/app:0f3747e

docker image prune -f --filter until=24h
```

Useful for reproducing one step by hand, checking a flag you did not expect, or pasting into an issue. It is also what makes a change to a deploy reviewable as **behaviour** rather than as configuration: diff the output before and after.

⚠️ **Run them from the repository root.** The build context is `.`, so the command is correct only from where lath would have run it.

⚠️ **Three values are computed per run.** `COMMIT` and `BRANCH` come from git, and `BUILD_TIME` is the moment the run started, so a command captured yesterday builds a differently-stamped image today.

### The steps that do not print a command

`ensure-dirs`, `write-env-file` and `write-key-file` describe themselves instead:

```
4/9  would create [/tmp/app-local /tmp/app-local/logs ...]
5/9  would write 2860 bytes to /tmp/app-local/.env.local
```

They go through [`remotefs`](kit/remotefs.md), which runs a composed `sh -c` with the file's content arriving on **stdin** rather than in argv. Printing the shell line would be accurate and still not reproduce them. In effect they are `mkdir -p` and `install -m 600`.

`write-env-file` is also the step carrying your credentials, and its content is deliberately never printed.

## Driving it

At each pause:

| Key | Does | After a failed step |
|---|---|---|
| `n` **or Enter** | run the next step, pause again | not offered, there is no next step |
| `r` | run the step that just ran, **again** | retry it, with no replay confirmation |
| `c` | stop pausing, run to the end | not offered, and it never detaches: see below |
| `s` | show or hide the state pane | same |
| `q` **or Ctrl-D** | abandon the run | give up, reported as the step failure |

Anything else re-prompts. It never guesses, because the only sensible guess would be "carry on", and advancing a deploy because someone leaned on a key is the thing this is arranged to prevent.

```
  acme_api · deploy local --apply

  ✓  1/14  resolve-env         0s
        env=local, 0 required credential(s) present
  ✓  2/14  resolve-commit      40ms
        commit=0f3747e branch=dev
  ▸  3/14  build-image

  state
    branch       dev
    commit       0f3747e
    env          local

  n next · r rerun · c continue · q quit · s state ▸
```

`c` (continue) means "stop asking me between steps that work". It is not a detach: a later failure still pauses, because that is the moment the pause is worth the most.

---

## Rerunning

**`r` replays a step. It does not undo one.**

State is snapshotted before each step and restored on a replay, so the step sees the inputs it originally saw, but nothing can un-`docker run` a container or un-write a file on a server.

Steps therefore declare whether they are safe to replay. Read-only and idempotent ones replay immediately:

```
▸ rerun 2/14 resolve-commit          (replays, no prompt)
```

Ones that have not declared it ask first, naming what already happened:

```
  ⚠ start-processes is not marked replayable.
    it already reported: 1 container(s): acme-local-api-0f3747e-0
    running it again may repeat whatever it did.

  rerun anyway? [y/N] ▸
```

Of the fourteen shipped steps, twelve declare themselves replayable. Two deliberately do not:

| Step | Why not |
|---|---|
| `start-processes` | starts **another** container |
| `run-once` | runs an arbitrary caller-supplied command |

### Declaring it on your own steps

```go
// A named step: implement the optional interface.
func (Fetch) Replayable() bool { return true }

// An inline step: set the field.
pipeline.Func{
    Label:      "resolve-config",
    Idempotent: true,
    Do:         func(ctx context.Context, s *pipeline.State) error { … },
}
```

A step that says nothing is treated as **unsafe**, which is the honest default: wrongly assuming safe costs a duplicate container, wrongly assuming unsafe costs one keystroke.

---

## When a step fails

Under a debugger a failure **pauses** instead of ending the run. Fix whatever broke, on the box, in your environment, wherever, and press `r`.

```
  ✗ 10/14  pull-image          FAILED
        i/o timeout reaching ghcr.io

  step 10 failed. Fix whatever broke and retry it, or give up.

  r retry this step · q give up · s state ▸ r
```

**The prompt shrinks.** There is no next step to advance to and nothing to continue, so `n`, `c` and a bare Enter are not offered and are not accepted: they re-prompt. This is deliberate. After pressing `n` at nine steps in a row, the tenth `n` is muscle memory, and at a failure it used to end the run the operator meant to retry. Two keys remain, and both mean what they say: `r` retries the step, `q` gives up.

Retrying a failed step never asks for the replay confirmation above, even for a step that is not marked replayable. The step did not finish, so there is nothing to repeat.

Whichever you press, the run reports **the step failure** as its cause. Declining to retry is not the reason a deploy failed, and a log saying "abandoned" is not the one anybody investigates.

**Without a debugger nothing changes**: a failing step ends the run as it always has. The pause exists only while someone is watching.

---

## The state pane

Shows what the steps have produced so far. The values flowing between them.

```
  state
    branch       dev
    commit       0f3747e
    containers   [acme-local-api-0f3747e-0]
    env          local
    image        ghcr.io/acme/acme_api:0f3747e
```

**Credentials cannot leak here.** Values render through `fmt`, and `secret.Value` closes every formatting path it has: `String`, `GoString`, `Format`, `MarshalJSON`, `MarshalText` and `LogValue`. A secret in pipeline state shows as its type and length, never its contents. There is a test asserting the plaintext appears nowhere in the bytes on the wire.

Toggle it with `s`. The final state is shown once more when the run ends, because there is no pause after the last step.

---

## What your definitions need

**Nothing.** Not a line, not an import, not a `go.mod` entry.

```go
// .lath/deploy/targets.go — unchanged, and steppable
st := pipeline.NewState(mode, pipeline.NewTextReporter(os.Stdout))
return p.Run(ctx, st)
```

Your own terminal keeps scrolling exactly as before; the panel receives the same output in parallel. See [`pipeline/README.md`](pipeline/README.md#reportersource--output-reaching-a-client) for how.

The one requirement is that the definition already requires the `pipeline` module. A definition of plain shell-replacement tasks gets no debugger code generated for it at all, and pays nothing for the feature.

---

## Recipes

### Rehearse a deploy locally, watching each step

```
lath tui deploy local --apply
```

Step 3 is the docker build and takes minutes. The panel shows `attached, 14 steps. Running…` while it works.

### Watch a dry run without touching anything

```
lath tui deploy run staging
```

No `--apply`, so every step reports what it *would* do. Safe to explore.

### Retry a flaky registry pull without restarting a 3-minute build

Step through to `pull-image`. When it fails, fix the network, press `r`. The image built at step 3 is still in state; nothing is rebuilt.

### Run something that has no pipeline

```
lath tui deploy plan staging
```

`deploy plan` prints the step list and returns. It never builds a `Pipeline`, so there is nothing to step. Its output streams as it happens, exactly as `lath run` would, and the exit status is passed through. The panel appears only if a pipeline starts.

### Stop stepping halfway

Press `c`. The rest runs without pausing, output still streaming to the panel.

### Step a run happening on another machine

```
ssh box 'cd /srv/app && lath run deploy run prod --debug --allow-prod'
```

…then `lath tui` on that machine. The socket is unix-domain and scoped by filesystem permissions, so it is not reachable across the network, attach from where the run is.

### Give yourself longer to attach

```
lath run deploy local --apply --debug=10m
```

---

## Flags

| Flag | Means |
|---|---|
| `--debug` | make this run steppable; wait up to 2 minutes to be attached to |
| `--debug=10m` | the same, with a different wait |

`--debug` is consumed by lath and never reaches your target, so a target that defines its own flags is unaffected.

---

## When it does not work

| What you see | What happened |
|---|---|
| `--debug needs a terminal: nothing here could attach` | The run was piped or redirected, or is in CI. Refused **before any step runs**, so a deploy can never block on a keystroke nobody can send. |
| `the run ended before this could attach` | The run gave up waiting, or failed immediately. Start it again and attach while it still says it is waiting. |
| The target ran and printed, but no panel appeared | It built no pipeline, so there was nothing to step. Its output streamed live and its exit status passed through. Not an error. |
| `no targets in ./.lath` | Not in a project with a definition. |
| `session ended: EOF` mid-run | The run died, killed, or crashed. |
| The panel disappears and docker's own output takes over | Intended. When `lath tui` launched the run into this terminal, a running step **owns the screen**, so docker draws its real `[+] Building` table instead of a flat log. The panel returns at the next pause. |
| A step sits with no new output | Normal, `docker build` prints nothing for the whole of a Go compile. The spinner turns and the elapsed clock advances, so a working step is distinguishable from a hung one at a glance. |

**A failure to attach is a failed run.** If you asked to supervise and did not get to, the run exits non-zero and says so rather than completing quietly. Likewise a client that disconnects mid-run aborts it: losing the supervisor is not consent to proceed.

---

## Sessions

Live runs advertise themselves under `$TMPDIR/lath-sessions/`, keyed by **project and PID**, so two projects running `deploy local`, or one project stepped twice, never collide.

```
$ lath tui

  waiting sessions:

    1) acme_api     deploy local --apply     12s ago
    2) billing-api    deploy run staging        3s ago

  attach [1-2]:
```

A crashed run leaves files behind; the next listing sweeps them, using the process **start time** as well as the PID so a recycled PID cannot make a dead session look live. The directory is owner-only. It carries a control channel into a running deploy, and filesystem permissions are the only thing scoping who may attach.

Attach to a specific one by id: `lath tui 74afdd90-66317`.

**One client per session.** There is no safe rule for whose keystroke wins on a deploy, so the listener closes as soon as one client attaches.

---

## Driving it without the panel

The transport is one JSON object per line over a unix socket, chosen so that when the panel itself is what is broken, a session can be driven by hand:

```
$ nc -U /var/folders/…/T/lath-sessions/74afdd90-66317.sock
{"t":"plan","pipeline":"deploy-local","steps":[…]}
{"t":"start","n":1,"total":14,"name":"resolve-env"}
{"t":"detail","msg":"env=local, 0 required credential(s) present"}
{"t":"done","n":1,"total":14,"name":"resolve-env","took":"0s"}
{"t":"paused","n":1,"total":14,"name":"resolve-env","next":"resolve-commit","replayable":true}
{"cmd":"next","ack":1}      ← type this, press Enter
```

Or in Go, with [`pipeline/debug`](pipeline/debug.md):

```go
cli, err := debug.Dial(socket)
if err != nil {
    return err
}
defer cli.Close()

for {
    e, err := cli.Next()
    if err != nil {
        return err
    }
    if e.Kind == debug.KindPaused {
        if err := cli.Send(pipeline.ActNext, e.N); err != nil {
            return err
        }
    }
    if e.Kind == debug.KindFinished {
        return nil
    }
}
```

⚠️ **Every control must carry the `ack` of the pause it answers.** Without it, queued keystrokes become the answers to later pauses and the pipeline advances unsupervised, on a deploy, that is how containers start that nobody asked for.

---

## In tests

`pipeline.FakeDebugger` drives a pipeline through pauses with no socket, no terminal and no filesystem:

```go
d := &pipeline.FakeDebugger{Script: []pipeline.Action{
    pipeline.ActNext,
    pipeline.ActRerun,
    pipeline.ActNext,
}}
st := pipeline.NewState(pipeline.ModeExecute, nil, pipeline.Debug(d))
if err := p.Run(ctx, st); err != nil {
    t.Fatal(err)
}

d.Steps()       // ["one" "two" "two"] — execution order, replays included
d.Pauses()      // every Pause: position, next, state, replayable, error
d.FinalState()  // what the run produced
```

Running out of script is an error rather than an implicit continue. A pipeline that quietly ran past the end of its script asserts nothing.

---

## What is deliberately absent

| | Why |
|---|---|
| **`back`**, step backwards | It would have to claim side effects were undone. In a tool whose point is that a deploy behaves the way the plan said, a lie is worse than a missing feature. |
| **`skip`** a step | Breaks the wiring `Validate` proved; needs a real refusal path first. |
| **Breakpoints** | `n` plus `c` covers it until pipelines get long. |
| **Attaching over the network** | A unix socket is scoped by filesystem permissions. TCP is a remote-control channel for deploys and needs authentication designed, not bolted on. |
| **One panel, several sessions** | A different feature. Run two. |
