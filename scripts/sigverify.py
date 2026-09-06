#!/usr/bin/env python3
"""Verify that every function signature written in the reference docs matches the real one.

Why this exists, given docverify already runs: docverify checks NAMES, in both
directions, and cannot see that a documented signature has drifted from the
one that exists. That is the quieter half of doc rot — the symbol is real, the
prose around it is right, and the snippet a reader copies does not compile
because a parameter was added, removed or renamed since it was written.

Found on its first run: pipeline.NewState documented without its
`opts ...StateOption` parameter, which is the one carrying Debug(d), so the
documented constructor made per-State debugger attachment look impossible.
Plus five signatures writing `ctx` with no type at all.

WHAT IT DELIBERATELY IGNORES, because a gate that reports noise gets ignored:

  * receiver names — `func (h *Hasher)` against `func (t *Hasher)`. A caller
    cannot see a receiver name, so a difference is not drift;
  * named returns — `(err error)` against `error`. Identical at every call
    site, and forcing docs to carry the name would make them harder to read
    for no gain;
  * lines ending in `{`, which are implementation examples showing how to
    write a step, not claims about this API;
  * methods on a type the API does not export, which is a caller's own type in
    an example — `func (b Build) Run(...)` in "how to write a step".

SCOPE is docverify's: docs/kit, docs/steps, docs/pipeline. The design
documents name identifiers that were considered and rejected; checking them
would report their entire point as an error.
"""
# Files are opened as UTF-8 explicitly, never with the platform default:
# Python uses the locale encoding, which is cp1252 on Windows, and every
# document here contains characters it cannot decode. The gate died on an em
# dash the first time it ran there.
import glob
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# Each module's own directory of reference docs. A module with no doc directory
# is a module nobody can read, which docverify already refuses.
MODULES = {"kit": "docs/kit", "steps": "docs/steps", "pipeline": "docs/pipeline"}

# A fenced Go block in markdown. Docs state signatures inside these and nowhere
# else, so anything outside one is prose and not a claim about the API.
GO_BLOCK = re.compile(r"```go\n(.*?)```", re.S)

# func (recv Type) Name(...) — receiver optional, name must be exported.
#
# The receiver NAME is optional too, because this same pattern is applied
# before and after normalise(), which erases it. Requiring it silently skipped
# every method in the docs: the first version of this script reported 113
# signatures clean while checking only plain functions, and proved it by
# passing a break test that deleted a parameter from Client.Images.
SIGNATURE = re.compile(r"^func (?:\((?:\w+ )?(\*?\w+)\) )?([A-Z]\w*)")


def normalise(sig: str) -> str:
    """Reduce a signature to what a CALLER can observe.

    Receiver name and named returns are erased for the reasons in the module
    docstring: both are invisible at a call site, so a difference in either is
    a difference the reader can never encounter.
    """
    sig = re.sub(r"\s+", " ", sig).strip()
    sig = re.sub(r"\s*//.*$", "", sig).strip()
    sig = re.sub(r"^func \(\w+ (\*?\w+)\)", r"func (\1)", sig)
    # A single named return: `(err error)` -> `error`. Multiple named returns
    # are left alone, since the names then carry real meaning about order.
    sig = re.sub(r"\) \(\w+ ([\w\.\*\[\]]+)\)$", r") \1", sig)
    return sig


def api(module: str) -> dict[str, list[str]]:
    """Every exported function the module really has, keyed by name."""
    base = os.path.join(ROOT, module)
    packages = [""] if module == "pipeline" else []
    packages += sorted(
        d for d in os.listdir(base)
        # internal/ is unimportable from outside the module, so it has no
        # reference page and no signatures anyone can call.
        if os.path.isdir(os.path.join(base, d)) and not d.startswith(".") and d != "internal"
    )

    found: dict[str, list[str]] = {}
    for package in packages:
        result = subprocess.run(
            ["go", "doc", "-all", "./" + package if package else "."],
            cwd=base, capture_output=True, text=True, encoding="utf-8",
        )
        if result.returncode != 0:
            print(f"sigverify: go doc failed for {module}/{package}:\n{result.stderr}", file=sys.stderr)
            sys.exit(2)
        for line in result.stdout.splitlines():
            line = line.strip()
            if not line.startswith("func "):
                continue
            match = SIGNATURE.match(line)
            if match:
                found.setdefault(match.group(2), []).append(normalise(line))
    return found


def receiver_types(signatures: dict[str, list[str]]) -> set[str]:
    """The types this API hangs methods on, so an example's own type is skipped."""
    types = set()
    for group in signatures.values():
        for sig in group:
            match = SIGNATURE.match(sig)
            if match and match.group(1):
                types.add(match.group(1).lstrip("*"))
    return types


def main() -> int:
    drifted = []
    checked = 0

    for module, docdir in MODULES.items():
        real = api(module)
        types = receiver_types(real)

        for path in sorted(glob.glob(os.path.join(ROOT, docdir, "*.md"))):
            with open(path, encoding="utf-8") as f:
                content = f.read()
            for block in GO_BLOCK.findall(content):
                for line in block.splitlines():
                    stripped = re.sub(r"\s+", " ", line).strip()
                    if not stripped.startswith("func ") or stripped.endswith("{"):
                        continue
                    documented = normalise(stripped)
                    match = SIGNATURE.match(documented)
                    if not match:
                        continue
                    recv, name = match.group(1), match.group(2)
                    if recv and recv.lstrip("*") not in types:
                        continue  # a caller's own type, in an example
                    if name not in real:
                        continue  # docverify's half of the problem
                    candidates = real[name]
                    if recv:
                        candidates = [c for c in candidates if SIGNATURE.match(c).group(1) == recv]
                        if not candidates:
                            continue
                    checked += 1
                    if documented not in candidates:
                        drifted.append((os.path.relpath(path, ROOT), documented, candidates))

    if not drifted:
        print(f"OK — {checked} documented signature(s) match the API")
        return 0

    for path, documented, candidates in drifted:
        print(f"\n{path}")
        print(f"  documented: {documented}")
        for candidate in candidates:
            print(f"  actual:     {candidate}")
    print(f"\n{len(drifted)} of {checked} documented signature(s) have drifted")
    return 1


if __name__ == "__main__":
    sys.exit(main())
