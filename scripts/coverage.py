#!/usr/bin/env python3
"""Measure test coverage across every module, and keep the README badge honest.

Why a script rather than `go test -cover`: this repository is four modules plus
a definition module that is deliberately OUTSIDE the workspace, so no single go
command sees all of it. A number that quietly omits a module is worse than no
number, because it reads as a measurement of the whole.

The total is STATEMENT-WEIGHTED, not an average of percentages. A 100% package
with nine statements and a 50% package with nine hundred do not average to 75%
in any sense a reader would accept.

The number moves slightly between platforms, and that is honest rather than a
bug: tests that need a tool skip where it is absent — `ruby` for the Homebrew
formula check, `sha256sum` for the manifest interop check, `git` for the
publish tests. The badge records whatever `--badge` last measured; CI enforces
the FLOOR rather than the exact figure, because pinning it would fail every
time a runner image changed which tools it ships.

Modes:
  (default)   measure and print
  --min N     exit non-zero below N percent — the CI floor
  --badge     rewrite the README's coverage badge to the measured number
  --check     exit non-zero if the README badge disagrees with reality
"""
import argparse
import os
import re
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# Every module, and how to reach its packages. The definition module is listed
# with GOWORK=off because it is outside the workspace by design: `task
# isolation` exists to prove that, and a coverage run that pulled it in would
# quietly undo the property being protected.
TARGETS = [
    ("workspace", ROOT, ["github.com/ubgo/lath/..."], {}),
    ("definition", os.path.join(ROOT, "example", ".lath"), ["./..."], {"GOWORK": "off"}),
]

# Packages with no test files report "no test files" rather than 0%, and
# counting them as 0 would punish a package that is pure type declarations.
# They are reported separately instead, so the number stays honest in both
# directions: nothing is hidden, nothing is scored that cannot be tested.
NO_TESTS = re.compile(r"^\?\s+(\S+)\s+\[no test files\]")
COVERAGE = re.compile(r"^(?:ok|FAIL)\s+(\S+)\s+.*?coverage: ([\d.]+)% of statements")

# A coverage profile line: file:startLine.col,endLine.col numStatements count
PROFILE_LINE = re.compile(r"^(.+):\d+\.\d+,\d+\.\d+ (\d+) (\d+)$")

# The badge this script owns. Matched on the shields URL rather than on the
# surrounding markup, so restyling the header does not break the rewriter, and
# tightly enough that no other badge in the README is touched.
BADGE = re.compile(r'<img src="https://img\.shields\.io/badge/coverage-[\d.]+%25-\w+" alt="[^"]*">')

# One colour, the same one every other badge in this family uses.
#
# Deliberately NOT a per-threshold palette: a row of badges in five different
# colours reads as decoration, and the reader learns nothing from a green that
# is green because 88 is above some line they cannot see. What stops the badge
# being reassuringly green at 40% is not its colour, it is `task cover:check`,
# which fails below the floor — so the number can never sit here unchallenged.
HOUSE_COLOUR = "2ea44f"


def measure(verbose: bool) -> tuple[float, list[tuple[str, float]], list[str]]:
    """Run every module's tests with coverage, returning (total, per-package, untested)."""
    covered = 0
    total = 0
    packages: list[tuple[str, float]] = []
    untested: list[str] = []

    for name, cwd, patterns, extra_env in TARGETS:
        with tempfile.NamedTemporaryFile(suffix=".out", delete=False) as profile:
            path = profile.name
        try:
            env = {**os.environ, **extra_env}
            result = subprocess.run(
                ["go", "test", "-coverprofile=" + path, "-covermode=set", *patterns],
                cwd=cwd, env=env, capture_output=True, text=True,
            )
            if verbose or result.returncode != 0:
                sys.stdout.write(result.stdout)
                sys.stderr.write(result.stderr)
            if result.returncode != 0:
                print(f"coverage: tests failed in the {name} modules", file=sys.stderr)
                sys.exit(2)

            for line in result.stdout.splitlines():
                if match := COVERAGE.match(line):
                    packages.append((match.group(1), float(match.group(2))))
                elif match := NO_TESTS.match(line):
                    untested.append(match.group(1))

            # Statements are counted from the PROFILE, not from the printed
            # percentages: the percentages are per package and cannot be
            # combined without their weights.
            if os.path.exists(path):
                with open(path) as f:
                    for line in f:
                        if match := PROFILE_LINE.match(line.strip()):
                            statements, count = int(match.group(2)), int(match.group(3))
                            total += statements
                            if count > 0:
                                covered += statements
        finally:
            if os.path.exists(path):
                os.unlink(path)

    percent = (covered / total * 100) if total else 0.0
    return percent, sorted(packages), sorted(untested)


def badge_markdown(percent: float) -> str:
    """The badge element, with alt text stating what the number measures.

    "88.2%" alone is not an accessible label and not an informative one: this
    is statement coverage across every module, including the definition module
    outside the workspace, which is exactly the thing a bare percentage in a
    Go repository is usually NOT measuring.
    """
    return (f'<img src="https://img.shields.io/badge/coverage-{percent:.1f}%25-{HOUSE_COLOUR}" '
            f'alt="Statement coverage across every module: {percent:.1f}%">')


def readme_badge() -> str | None:
    """The coverage badge currently in the README, or None if there is none."""
    with open(os.path.join(ROOT, "README.md")) as f:
        match = BADGE.search(f.read())
    return match.group(0) if match else None


def write_badge(percent: float) -> bool:
    """Rewrite the README's coverage badge. Returns whether anything changed."""
    path = os.path.join(ROOT, "README.md")
    with open(path) as f:
        content = f.read()
    updated, count = BADGE.subn(badge_markdown(percent), content)
    if count == 0:
        print("coverage: the README has no coverage badge to update", file=sys.stderr)
        sys.exit(2)
    if updated == content:
        return False
    with open(path, "w") as f:
        f.write(updated)
    return True


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--min", type=float, default=None, help="fail below this percentage")
    parser.add_argument("--badge", action="store_true", help="rewrite the README badge")
    parser.add_argument("--check", action="store_true", help="fail if the README badge is stale")
    parser.add_argument("--verbose", action="store_true", help="show go test output")
    args = parser.parse_args()

    percent, packages, untested = measure(args.verbose)

    width = max((len(name) for name, _ in packages), default=0)
    for name, pkg_percent in packages:
        print(f"  {name.replace('github.com/ubgo/lath/', ''):<{width}}  {pkg_percent:5.1f}%")
    for name in untested:
        # Named rather than silently omitted: a package with no tests is a
        # fact about this repository, and hiding it is how it stays true.
        print(f"  {name.replace('github.com/ubgo/lath/', ''):<{width}}      —  (no test files)")
    print(f"\n  total (statement-weighted): {percent:.1f}%")

    failed = False
    if args.badge:
        if write_badge(percent):
            print(f"  README badge updated to {percent:.1f}%")
        else:
            print("  README badge already correct")
    elif args.check:
        current, expected = readme_badge(), badge_markdown(percent)
        if current != expected:
            print(f"\ncoverage: the README badge is stale\n  says: {current}\n  is:   {expected}",
                  file=sys.stderr)
            failed = True

    if args.min is not None and percent < args.min:
        print(f"\ncoverage: {percent:.1f}% is below the {args.min:.1f}% floor", file=sys.stderr)
        failed = True

    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
