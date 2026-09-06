# Primitive plan, retiring acme_api's deployment machinery

**This was a PLAN. Most of it shipped, under different names.** The proposal is kept as written, because the reasoning behind each primitive is still the reasoning in the code. What follows is the map from the names proposed here to the names that exist, read it before taking any identifier on this page literally.

| Proposed here | What shipped | Where |
|---|---|---|
| `secret.Resolve` | `secret.Sync` over a `secret.Store` interface. A backend is supplied by the caller, so the kit names no vendor | [`kit/secret`](kit/secret.md) |
| `secret.Require` | `common.ResolveEnv{Secrets: …}`, which fails before the build, plus `secret.FromEnv` for a single value | [`steps`](steps/README.md) |
| `secret.Materialize` | `common.PutFile{Secret: …}`. The value goes from memory to the target without touching either disk | [`steps`](steps/README.md) |
| `secret.File` | the same `PutFile`, with `Owner` and `Mode`. One primitive covered both cases | [`kit/remotefs`](kit/remotefs.md) |
| `secret.Diff` | `secret.Sync(…, secret.DryRun())`, returning a `Report` | [`kit/secret`](kit/secret.md) |
| `host.Inspect` | **not built**, `plan` plus `docker inspect` covered the need |, |

Two things changed shape during implementation, and both are worth knowing. **The secret primitives grew a `Store` interface** rather than talking to a backend directly, because a package that knows what GitHub is cannot be used by a project that does not; the GitHub adapter is ~75 lines and lives in the project, not the kit, and **`secret.File` stopped being separate**: a file-shaped secret and a value-shaped one differ only by mode and owner, which are parameters, not a second primitive.

For what exists today, read the [architecture](ARCHITECTURE.md) and the [kit reference](kit/README.md).

What primitives lath needs in order to delete most of the custom deployment layer in `acme_api`, and what each one retires.

---

## 1 · What exists today

Measured, not estimated:

| Artifact | Lines | What it is |
|---|---:|---|
| `apps/scripts/internal/pkg/secrets_build/secrets_build.go` | 741 | parses `.env.{env}`, splits GitHub-only vars from app vars, base64-encodes both the filtered env and every key file, creates GitHub Environments, pushes/reads/cleans secrets, renders the deploy workflow |
| `apps/scripts/internal/pkg/secrets_build/README.md` | 211 | documents the above |
| `apps/scripts/main.go` | 300 | nine `gh:*` cobra commands driving it |
| `.github/workflows/deploy.prod.yml` | 300 | **generated** |
| `.github/workflows/deploy.staging.yml` | 300 | **generated** |
| `.github/workflows/deploy.yml.gotmpl` | 306 | the template both are generated from |
| `shell/deploy.sh` | 53 | dead, still targets a previous project |
| `.docker/entrypoint.sh` | 24 | migrate-once, then hand off to supervisor |
| `.docker/supervisord.prod.conf` | 107 | five programs in one container |
| `docs/DEPLOY_GUIDE.md` + `docs/deploy/*.md` | 953 | how to operate all of it |
| **Total** | **≈ 4,000** | |

Plus: two `.env.{env}` files carrying a `GITHUB_SECRET_` naming convention, seven files in `_keys/`, and four `gh:*` Taskfile entries.

## 2 · Why most of it exists

Nearly all of `secrets_build` solves exactly one problem: **GitHub Actions can only receive data through GitHub Secrets.**

That single constraint produces the whole chain. A `GITHUB_SECRET_` prefix convention to mark which vars are for the runner rather than the app, base64 encoding because a secret is a flat string and the payload is a file, `ENV_B64` because the app's entire environment has to arrive as one value, a GitHub Environment per deploy target to hold them, push/read/clean commands to keep them in sync, and a generated workflow because the thing consuming them is YAML.

None of that is about deploying. It is about **moving data into a runner.**

A binary that runs anywhere reads its configuration directly, and the entire chain has nothing to do. This is not a port, most of these 4,000 lines have no counterpart on the other side.

<!-- The one thing that genuinely must survive is a place to keep secrets. See §6, decision 1. -->

## 3 · Primitives

Grouped by what they retire. Names are provisional.

### 3.1 Secrets, retires ≈ 1,250 lines

| Primitive | In → Out | Notes |
|---|---|---|
| `secret.Resolve` | environment → name/value map | reads a backend directly; replaces the entire push/pull sync cycle |
| `secret.Require` | required names → ok/err | fails **before** the image is built rather than at container start, today a missing secret surfaces on boot |
| `secret.Materialize` | values → env available to the container | replaces `ENV_B64` decode → write → `chmod 600` |
| `secret.File` | file-shaped secret → path on host, correct uid + mode | replaces base64-encode → GitHub secret → decode over SSH → `chmod` → `chown 1000` for each of the seven `_keys/` files |
| `secret.Diff` | declared vs live → differences | no counterpart today; the current env is an opaque base64 blob nobody can inspect |

Retires: `secrets_build.go` (741), its README (211), the nine `gh:*` commands (≈ 300), the `GITHUB_SECRET_` prefix convention, `.secrets.{env}` files, and per-file `*_B64` secrets.

<!-- secret.Require is the highest-value item on this page relative to its size:
     it converts a class of boot-time failure into a pre-build check. -->

### 3.2 Host, retires the setup that reruns on every deploy

| Primitive | In → Out | Notes |
|---|---|---|
| `host.Connect` | host → session | one connection reused by every later primitive |
| `host.Exec` | command → output, exit code | |
| `host.CopyFile` | local → remote, uid + mode | |
| `host.EnsureDirs` | directory spec → provisioned | idempotent, and **records that it ran** |
| `host.Provisioned` | host → yes/no | the state that makes setup a one-time step |

Retires: roughly sixty lines of the generated workflow. Creating the deploy directory, the `sudo` fallback for a first deploy, `chown 1000:1000` on logs, keys, artifacts and uploads, all of it is setup, and all of it re-executes on every deploy because the current pipeline is stateless and has no memory of the host.

### 3.3 Image transport, retires the retry loops

| Primitive | In → Out | Notes |
|---|---|---|
| `image.Build` | context + dockerfile → image | **built** (describes only) |
| `image.Save` / `image.Load` | image ↔ tar stream | **the registry-free path**: stream over the SSH session already open |
| `image.Push` / `image.Pull` | image ↔ registry | for when a registry is wanted |
| `image.Prune` | retention policy → freed space | must keep enough history to roll back to |

Retires: ten retry attempts around `docker login`, ten more around `docker pull`, and `docker image prune -f` with no retention policy. The retries exist because a fresh host IP gets throttled by the registry; `image.Save`/`Load` removes the dependency rather than retrying around it.

### 3.4 Process model, retires 131 lines, **optional**

| Primitive | In → Out | Notes |
|---|---|---|
| `container.Run` | process spec → running container | one per process |
| `container.Stop` / `Remove` | | |
| `container.List` | host → what is running | the "what is actually deployed" answer nothing gives today |

Retires `entrypoint.sh` (24) and `supervisord.prod.conf` (107) **only if** the five processes are split into five containers. That is a separate decision (§6, decision 2) and should not be bundled with the rest.

⚠️ Splitting has a prerequisite. `entrypoint.sh` runs `acme migrate` before supervisord. Five containers means five concurrent migrations on boot. A `RunOnce` step running the migration must already be in the pipeline, which it is in the lab, and the image entrypoint must accept a process command instead of migrating.

### 3.5 Traffic, new capability, not a retirement

| Primitive | In → Out | Notes |
|---|---|---|
| `proxy.ReadUpstreams` | proxy → current upstreams | |
| `proxy.SetUpstreams` | new upstreams → applied | edits the **existing** shared proxy; we never bind :80/:443 |
| `container.Drain` | old containers → quiesced | |

Nothing to retire here, today there is no zero-downtime deploy at all. The current sequence is `docker stop` → `docker run` → `sleep 5`, which has downtime on every deploy and no health gate. These primitives add a capability rather than replacing one.

### 3.6 Introspection, retires most of the documentation

| Primitive | In → Out | Notes |
|---|---|---|
| `plan` | pipeline → step list | **built** |
| `secret.Diff` | see §3.1 | |
| `host.Inspect` | host → what is running, at what version | |

Retires a large share of the 953 lines of deploy documentation. Most of it describes what the pipeline does, in prose, because there is no way to ask. A definition that reads top-to-bottom plus a `plan` command that prints the real sequence replaces the description; what remains is the part worth writing by hand, why the box is set up the way it is.

## 4 · Retirement map

| acme_api artifact | Lines | Replaced by | Fate |
|---|---:|---|---|
| `secrets_build.go` | 741 | `secret.Resolve` + `secret.Require` | **deleted** |
| `secrets_build/README.md` | 211 |, | **deleted** |
| `gh:*` commands in `main.go` | ≈300 | `secret.Diff`, `secret.Materialize` | **deleted** |
| `deploy.prod.yml` (generated) | 300 | the definition + an 8-line workflow | **deleted** |
| `deploy.staging.yml` (generated) | 300 | same definition, different environment | **deleted** |
| `deploy.yml.gotmpl` | 306 |. The generator has nothing left to generate | **deleted** |
| `shell/deploy.sh` | 53 |, | **deleted** (already dead) |
| `entrypoint.sh` | 24 | `RunOnce` step + `container.Run` | deleted **only if** processes split |
| `supervisord.prod.conf` | 107 | `container.Run` per process | deleted **only if** processes split |
| deploy documentation | 953 | `plan` + a readable definition | mostly deleted; the box's own history stays |
| `.env.{env}` GitHub-only vars |, | the definition (hosts, paths, container names are config, not secrets) | **deleted** |
| `_keys/*.creds` |, | `secret.File` | files stay; the base64→GitHub→decode chain goes |

Roughly **3,700 of 4,000 lines retired**, of which about 1,250 exist solely to feed a CI runner.

## 5 · What stays

Not everything in there is machinery worth removing:

- **`.docker/Dockerfile.prod`**. A good multi-stage build. Every comment in it records a real lesson. Untouched.
- **PKL config**, orthogonal. It reads `env:VAR`, so it does not care who set the variable.
- **`_keys/` contents**. The credentials themselves are still needed; only their transport changes.
- **`.githooks` secret scanner**, unrelated, and more important once secrets live in more places.
- **The migration itself**, `acme migrate` is unchanged; it just gets invoked by a step instead of an entrypoint.

## 6 · Decisions needed before building

Each one changes which primitives get built.

| # | Decision | Why it blocks |
|---|---|---|
| 1 | **Where do secrets live?** An encrypted file committed to the repo, a hosted secret manager, or GitHub Secrets read through its API. | Determines `secret.Resolve`'s backend and whether a laptop deploy is possible at all. An encrypted file is the only option that needs no service and works identically in all three places. |
| 2 | **Split the five processes, or keep supervisord?** | Decides whether §3.4 is built now. Keeping supervisord makes the first migration a pure deploy-mechanics change with zero process-model risk. |
| 3 | **Registry or SSH stream by default?** | Decides whether `image.Push`/`Pull` or `Save`/`Load` is the primary path, and whether a registry stays a hard dependency. |
| 4 | **Does the deploy still pass through CI by default?** | See the warning below. |

<!-- Decision 4 is the one most likely to be discovered late. -->

⚠️ **Something is lost that nobody has costed yet.** GitHub Environments are not only a secret store. They carry **required-reviewer protection rules**. Today, a production deploy can be gated on an approval because it runs inside a GitHub Environment. A binary that can deploy from a laptop bypasses that entirely.

If prod approval matters, one of these has to be true: the deploy still runs through CI for production (and the binary's portability is a debugging convenience rather than the normal path), or approval moves somewhere else. A check against an external gate before traffic moves. Deciding this after the migration means discovering that production lost its approval gate.

The same applies to the two other things GitHub Environments provide as a side effect: **one-deploy-at-a-time** (a concurrency group) and **an audit trail** of who deployed what. Both currently exist only because deploys are CI-only.

## 7 · Order

Ordered by value against risk. Each stage is independently useful, and stopping after any of them leaves the repository in a better state than before.

| Stage | Build | Retires | Risk |
|---|---|---|---:|
| 1 | `secret.Resolve` · `Require` · `Diff` | `secrets_build.go`, its README, the `gh:*` commands | **none**. Nothing about how deploys run changes |
| 2 | `host.Connect` · `Exec` · `EnsureDirs` · `Provisioned` | the setup block that reruns every deploy | low |
| 3 | `image.Save` · `Load` · `Build` | the twenty retry attempts | low |
| 4 | `container.Run` · `Stop` · `List` | the `eval`'d `docker run` string | medium |
| 5 | deploy pipeline, static upstream | both generated workflows + the template | medium |
| 6 | `proxy.*` · `container.Drain` | nothing, adds zero-downtime | medium |
| 7 | split processes | `entrypoint.sh`, `supervisord.prod.conf` | **highest**, do last, alone |

Stage 1 first is deliberate: it removes the largest single artifact, it is useful the day it ships even if nothing else follows, and it carries no deploy risk at all because no deploy behaviour changes. It also forces the environment model into existence, which every later stage depends on.

Stage 7 last and alone is equally deliberate. It is the only stage that changes how the application runs rather than how it is delivered.
