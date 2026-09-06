#!/usr/bin/env python3
"""Verify that every pkg.Symbol named in the reference docs actually exists.

Why this exists: documentation drifts silently. A renamed function leaves the
old name in prose, and nothing fails — the next reader copies a snippet that
does not compile. This extracts the real exported surface with `go doc -all`
and checks every `pkg.Symbol` mention against it.

SCOPE — deliberately only docs/kit/ and docs/steps/. Those describe what
exists, so a name that is not in the API is a defect. The design documents
(PRIMITIVES*.md) are preserved proposals: they name identifiers that were
considered and renamed or rejected, and each carries a mapping table saying so.
Checking them would report their entire point as an error.
"""
import glob
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULES = ("kit", "steps", "pipeline")
SCOPED_DOCS = ("docs/kit", "docs/steps", "docs/pipeline")

# Documentation drifts in TWO directions, and only checking one is how a gate
# reports success while the docs rot:
#
#   1. a documented symbol that no longer exists — a snippet that will not
#      compile, caught by checking docs against the API;
#   2. a symbol that exists and is documented NOWHERE — a primitive nobody can
#      discover, caught by checking the API against the docs.
#
# The second is the quieter failure: nothing looks wrong, the feature simply
# does not exist as far as a reader is concerned.


# The kit index opens by stating how many packages it documents. A number in
# prose is a claim nobody re-checks: this one was written "Twenty" when there
# were twenty-four, and "Twenty-three" when there were twenty-seven. Checking it
# costs one regex and removes the whole class.
KIT_COUNT = re.compile(r"^(\d+) packages that any Go program", re.M)


def kit_count_is_honest() -> list[str]:
    """The count the kit index claims, against the packages that exist."""
    index = os.path.join(ROOT, "docs", "kit", "README.md")
    kit = os.path.join(ROOT, "kit")
    actual = len([
        d for d in os.listdir(kit)
        if os.path.isdir(os.path.join(kit, d)) and not d.startswith(".")
    ])
    with open(index) as f:
        match = KIT_COUNT.search(f.read())
    if not match:
        return [f"docs/kit/README.md no longer states a package count (expected {actual})"]
    claimed = int(match.group(1))
    if claimed != actual:
        return [f"docs/kit/README.md claims {claimed} packages; there are {actual}"]
    return []


def exported(module: str, pkg: str) -> set[str]:
    """Every exported name in one package, including grouped const/var blocks."""
    out = subprocess.run(
        ["go", "doc", "-all", "./" + pkg if pkg else "."],
        cwd=os.path.join(ROOT, module), capture_output=True, text=True,
    ).stdout
    syms = set(re.findall(r"^(?:func|type|var|const)\s+([A-Z]\w*)", out, re.M))
    # Grouped blocks are indented: `Name Type = value`, `Name = value`, or a
    # bare `Name` in a const block. Missing these produced false positives on
    # every typed enum in the codebase.
    syms |= set(re.findall(r"^\s+([A-Z]\w*)\s+[\w.\[\]*]+\s*=", out, re.M))
    syms |= set(re.findall(r"^\s+([A-Z]\w*)\s*=", out, re.M))
    syms |= set(re.findall(r"^\t([A-Z]\w*)\s", out, re.M))
    return syms


def toplevel(module: str, pkg: str) -> set[str]:
    """Package-level exported symbols — the API a caller can import.

    Deliberately excludes struct fields, which `go doc -all` prints indented
    inside their type: a field is documented by its type's section, and
    demanding a separate mention for each would flag a hundred fields nobody
    would write a paragraph about.
    """
    out = subprocess.run(
        ["go", "doc", "-all", "./" + pkg if pkg else "."],
        cwd=os.path.join(ROOT, module), capture_output=True, text=True,
    ).stdout
    syms = set(re.findall(r"^(?:func|type)\s+([A-Z]\w*)", out, re.M))
    syms |= set(re.findall(r"^(?:var|const)\s+([A-Z]\w*)", out, re.M))
    # Grouped const/var blocks sit one tab in.
    syms |= set(re.findall(r"^\t([A-Z]\w*)\s+[\w.\[\]*]*\s*=", out, re.M))
    return syms


def undocumented(api_by_pkg: dict[str, set[str]]) -> list[str]:
    """Exported symbols mentioned nowhere in the docs."""
    prose = "\n".join(
        open(p).read()
        for p in glob.glob(os.path.join(ROOT, "docs", "**", "*.md"), recursive=True)
    )
    out = []
    for pkg, syms in sorted(api_by_pkg.items()):
        for s in sorted(syms):
            if not re.search(rf"\b{re.escape(s)}\b", prose):
                out.append(f"{pkg}.{s}")
    return out


def go_files(d: str) -> bool:
    """Whether a directory holds a Go package of its own."""
    return any(f.endswith(".go") and not f.endswith("_test.go") for f in os.listdir(d))


def main() -> int:
    api: dict[str, set[str]] = {}
    for module in MODULES:
        # The module root is scanned unconditionally, NOT only when it has no
        # subdirectories. An earlier version used the subdirectory list as an
        # either/or, and adding pipeline/debug silently stopped the pipeline
        # package itself from being checked at all — the docs for it went
        # unverified while the gate still reported success, which is the exact
        # failure this script exists to prevent.
        if go_files(os.path.join(ROOT, module)):
            api.setdefault(module, set()).update(exported(module, ""))
        for d in sorted(glob.glob(os.path.join(ROOT, module, "*/"))):
            pkg = os.path.basename(d.rstrip("/"))
            api.setdefault(pkg, set()).update(exported(module, pkg))

    # Direction 2: every exported symbol is mentioned somewhere.
    toplevel_api: dict[str, set[str]] = {}
    for module in MODULES:
        if go_files(os.path.join(ROOT, module)):
            toplevel_api.setdefault(module, set()).update(toplevel(module, ""))
        for d in sorted(glob.glob(os.path.join(ROOT, module, "*/"))):
            pkg = os.path.basename(d.rstrip("/"))
            toplevel_api.setdefault(pkg, set()).update(toplevel(module, pkg))

    # Direction 1: every documented symbol exists.
    bad = set()
    for scope in SCOPED_DOCS:
        for path in glob.glob(os.path.join(ROOT, scope, "**", "*.md"), recursive=True):
            for n, line in enumerate(open(path), 1):
                for pkg, sym in re.findall(r"\b([a-z][a-z0-9]*)\.([A-Z]\w*)", line):
                    if pkg in api and sym not in api[pkg]:
                        bad.add(f"{os.path.relpath(path, ROOT)}:{n}  {pkg}.{sym}")
    missing = undocumented(toplevel_api)
    counts = kit_count_is_honest()
    if counts:
        print("a number the docs state is no longer true:")
        print("\n".join("  " + c for c in counts))
    if bad or missing or counts:
        if bad:
            print("documented but does not exist:")
            print("\n".join("  " + b for b in sorted(bad)))
        if missing:
            print("exists but documented nowhere:")
            print("\n".join("  " + m for m in missing))
        print(f"\n{len(bad)} stale, {len(missing)} undocumented, {len(counts)} miscounted",
              file=sys.stderr)
        return 1
    print(f"OK — docs and API agree both ways ({len(api)} packages, "
          f"{sum(len(v) for v in toplevel_api.values())} exported symbols)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
