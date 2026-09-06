# The image `task test:linux` runs the gate inside.
#
# Why an image rather than `docker run golang:1.24` with a long shell command:
# the gate needs three things the stock image lacks — python3 for the
# documentation checks, git for the tests that drive a real repository, and
# task itself, because the whole point is to run THE gate rather than a
# hand-copied list of its steps that will drift from it.
#
# Pinned to the toolchain the modules declare, so a failure here is a Linux
# difference rather than a Go version difference.
FROM golang:1.24

# python3 for scripts/docverify.py, sigverify.py and coverage.py; git for the
# tests that init a repository and push to a bare remote; ca-certificates so
# `go install` can reach the proxy.
#
# Deliberately NOT ruby, though one test uses it: kit/brew pipes its rendered
# formula through `ruby -c`. That check is worth having and it already runs —
# macOS ships ruby, and so does GitHub's ubuntu runner — but rendering a
# formula is pure string building with no platform behaviour in it, so running
# it a third time here proves nothing this image exists to prove. Measured:
# identical coverage with and without, and 30 MB lighter without.
#
# The rule this follows: install a tool here only when its ABSENCE would let a
# LINUX-SPECIFIC difference through. That is why python3 and git are here, and
# why the image runs as non-root — those change what the tests can observe.
RUN apt-get update \
    && apt-get install -y --no-install-recommends python3 git ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# The same version CI installs. A different one here would mean the local Linux
# run and the CI run could disagree about the gate itself.
ARG TASK_VERSION=v3.44.0
RUN go install github.com/go-task/task/v3/cmd/task@${TASK_VERSION}

# A NON-ROOT user, because root is allowed to do the things several tests exist
# to prove are refused: reading a file with mode 0000, writing into a
# directory with no write bit. Those tests skip under root — that is correct,
# they cannot assert anything there — so a run as root silently checks less
# than the same run on a developer's machine, which defeats the point of this
# image. Six tests come back with this line.
RUN useradd --create-home --uid 1000 lath \
    && mkdir -p /home/lath/.cache/go-build /home/lath/go/pkg/mod \
    && chown -R lath:lath /home/lath /go

# git refuses to operate in a directory owned by another user, which is exactly
# what a bind-mounted host checkout looks like from inside the container. Set
# for the user that will actually run, not for root.
USER lath
RUN git config --global --add safe.directory '*'

# Caches live under the user's home so a named volume mounted there inherits
# its ownership; a volume mounted on a root-owned path would be unwritable.
ENV GOCACHE=/home/lath/.cache/go-build \
    GOMODCACHE=/home/lath/go/pkg/mod \
    GOFLAGS=-buildvcs=false

WORKDIR /src
