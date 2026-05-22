#!/usr/bin/env bash
# Unit/integration tests for the claude-sidecar wrapper script.
# Stubs `docker` via PATH so tests don't actually call the daemon.
#
# Each test runs in a fresh tmpdir with a curated $HOME and $PWD.
# Run: bash scripts/test/wrapper_test.sh
set -u

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WRAPPER="$REPO_ROOT/scripts/claude-sidecar"

PASSED=0
FAILED=0

red()   { printf '\033[31m%s\033[0m' "$*"; }
green() { printf '\033[32m%s\033[0m' "$*"; }

# ---- assertions ------------------------------------------------------------

fail() {
    printf '  %s %s\n' "$(red FAIL:)" "$*" >&2
    FAILED=$((FAILED + 1))
}

assert_eq() {
    local got="$1" want="$2" msg="${3:-equality}"
    if [ "$got" = "$want" ]; then return 0; fi
    fail "$msg: got '$got' want '$want'"
}

assert_contains() {
    local haystack="$1" needle="$2" msg="${3:-contains}"
    case "$haystack" in
        *"$needle"*) return 0 ;;
    esac
    fail "$msg: '$haystack' does not contain '$needle'"
}

# ---- per-test sandbox ------------------------------------------------------

# setup_sandbox creates a tmpdir with:
#   ./project/   — the user's project (becomes PWD inside the test)
#   ./home/      — a synthetic HOME
#   ./bin/docker — a stub docker that records its argv + stdin to ./docker.log
# Tests set up config.yaml + shadow files, then run the wrapper.
setup_sandbox() {
    SANDBOX="$(mktemp -d -t claude-sidecar-wrapper-test.XXXXXX)"
    mkdir -p "$SANDBOX/project" "$SANDBOX/home/.claude-sidecar" "$SANDBOX/bin"

    cat > "$SANDBOX/bin/docker" <<'STUB'
#!/usr/bin/env bash
# Stub docker: log argv (one per line, prefixed) and stdin (if any) to docker.log.
{
    printf 'ARGV: '
    for a in "$@"; do printf '%s\0' "$a"; done
    printf '\n'
    if [ ! -t 0 ]; then
        printf 'STDIN_BEGIN\n'
        cat
        printf '\nSTDIN_END\n'
    fi
} >> "$DOCKER_LOG"
# If the caller is `docker run ...`, emit a representative gen-overlay stdout
# so wrapper code that captures it has something to read.
if [ "${1:-}" = "run" ]; then
    echo "services: { stub: { image: stubbed } }"
fi
exit 0
STUB
    chmod +x "$SANDBOX/bin/docker"
}

teardown_sandbox() {
    [ -n "${SANDBOX:-}" ] && rm -rf "$SANDBOX"
}

# run_wrapper invokes the wrapper script inside the sandbox.
# Sets PATH so the stub docker is found first, HOME to the sandbox home,
# and runs from the sandbox project dir.
run_wrapper() {
    DOCKER_LOG="$SANDBOX/docker.log"
    : > "$DOCKER_LOG"
    (
        export PATH="$SANDBOX/bin:$PATH"
        export HOME="$SANDBOX/home"
        export DOCKER_LOG
        cd "$SANDBOX/project" && bash "$WRAPPER" "$@"
    )
}

docker_log() { cat "$SANDBOX/docker.log" 2>/dev/null || true; }

# Extract the captured docker stdin from the log (the bytes between
# STDIN_BEGIN and STDIN_END).
docker_stdin() {
    awk '/^STDIN_BEGIN$/{flag=1; next} /^STDIN_END$/{flag=0} flag' "$SANDBOX/docker.log"
}

# ---- tests -----------------------------------------------------------------

# Each test is a function named test_*. The runner discovers and invokes them.

test_gen_overlay_invokes_docker_with_configured_image() {
    setup_sandbox
    cat > "$SANDBOX/home/.claude-sidecar/config.yaml" <<'YAML'
image: ghcr.io/example/claude-sidecar:test
host_network: false
YAML

    run_wrapper gen-overlay >/dev/null 2>&1
    rc=$?
    assert_eq "$rc" "0" "wrapper exit code"

    log="$(docker_log)"
    assert_contains "$log" "ghcr.io/example/claude-sidecar:test" "docker invoked with configured image"
    assert_contains "$log" "bridge" "docker invoked with bridge command"
    assert_contains "$log" "gen-overlay" "docker invoked with gen-overlay subcommand"

    teardown_sandbox
}

# ---- runner ----------------------------------------------------------------

run_all() {
    local tests t before
    tests="$(declare -F | awk '/^declare -f test_/{print $3}')"
    for t in $tests; do
        printf '%s\n' "$t"
        before="$FAILED"
        "$t"
        if [ "$FAILED" -eq "$before" ]; then
            PASSED=$((PASSED + 1))
        fi
    done
    echo
    echo "$(green PASSED): $PASSED"
    echo "FAILED: $FAILED"
    [ "$FAILED" -eq 0 ]
}

run_all
