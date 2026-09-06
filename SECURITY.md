# Security policy

## Reporting a vulnerability

Email **khanakia@gmail.com** with the details. Please do not open a public issue for a security problem — a deploy tool holds credentials, and an issue is world-readable the moment it is filed.

Include what you need to describe the problem: the version or commit, what an attacker can do, and the smallest reproduction you have. A rough report is more useful than no report; do not wait until you have a polished one.

You will get an acknowledgement within a few days. If the report is valid, you will be told when a fix lands and credited in the release notes unless you would rather not be.

## Supported versions

Nothing is tagged yet, so the supported version is the default branch. Once releases begin, the most recent minor version of each module gets fixes.

## What this project handles that is worth your attention

These are the parts where a bug has security consequences, and where a report is especially welcome:

- **`kit/secret`** — values that must never reach a log, a plan, or an error message.
- **`kit/ssh`** and **`kit/runner`** — every remote command is quoted before it reaches a login shell on the far side. An escape here is remote code execution on a deploy target.
- **`kit/archive`** — extraction refuses paths that escape the destination, and links whose targets do so.
- **`kit/download`** and **`kit/checksum`** — artifacts are verified before they land.
- **`pipeline`** — `plan` and `dry-run` must never produce a side effect, and a credential must never reach plan output.

## What is out of scope

- A definition that a project wrote itself. lath runs your Go code; what that code does with a credential is yours.
- The `docker`, `git`, `ssh` and `caddy` programs this project drives. Report those upstream.
