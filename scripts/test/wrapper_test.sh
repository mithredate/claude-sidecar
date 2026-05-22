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
# Stub docker: log each argv on its own line (prefixed ARG:) and the stdin
# block (if any) between STDIN_BEGIN and STDIN_END markers.
{
    for a in "$@"; do printf 'ARG: %s\n' "$a"; done
    if [ ! -t 0 ]; then
        printf 'STDIN_BEGIN\n'
        cat
        printf '\nSTDIN_END\n'
    fi
    printf 'INVOCATION_END\n'
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

test_spec_carries_current_project_shadow_paths() {
    setup_sandbox
    cat > "$SANDBOX/home/.claude-sidecar/config.yaml" <<'YAML'
image: img
host_network: false
YAML
    mkdir -p "$SANDBOX/project/.sidecar"
    cat > "$SANDBOX/project/.sidecar/shadow" <<'EOF'
.env
.credentials.json
# this is a comment, should be skipped

secrets/
EOF

    run_wrapper gen-overlay >/dev/null 2>&1
    spec="$(docker_stdin)"
    assert_contains "$spec" ".env" "spec includes .env shadow"
    assert_contains "$spec" ".credentials.json" "spec includes .credentials.json shadow"
    assert_contains "$spec" "secrets/" "spec includes secrets/ dir shadow"
    # Comment line should be filtered:
    case "$spec" in *"# this is a comment"*) fail "comments should be filtered from shadow paths" ;; esac

    teardown_sandbox
}

test_spec_carries_extra_mounts_and_their_shadows() {
    setup_sandbox
    cat > "$SANDBOX/home/.claude-sidecar/config.yaml" <<YAML
image: img
host_network: false
extra_mounts:
  - $SANDBOX/mobile
YAML
    mkdir -p "$SANDBOX/mobile/.sidecar"
    printf 'firebase.json\nsecrets/\n' > "$SANDBOX/mobile/.sidecar/shadow"

    run_wrapper gen-overlay >/dev/null 2>&1
    spec="$(docker_stdin)"

    assert_contains "$spec" "host_path: $SANDBOX/mobile" "spec lists extra-mount host path"
    assert_contains "$spec" "name: mobile" "spec sets extra-mount name from basename"
    assert_contains "$spec" "firebase.json" "spec includes extra-mount shadow file"
    assert_contains "$spec" "secrets/" "spec includes extra-mount dir shadow"

    teardown_sandbox
}

test_exec_calls_docker_compose_exec_against_claude_sidecar() {
    setup_sandbox
    cat > "$SANDBOX/home/.claude-sidecar/config.yaml" <<'YAML'
image: img
host_network: false
YAML
    # An overlay file must exist for `exec` (no implicit `up`).
    touch "$SANDBOX/project/compose.sidecar.yml"

    run_wrapper exec "ls" "-la" >/dev/null 2>&1
    rc=$?
    assert_eq "$rc" "0" "exit code"
    log="$(docker_log)"
    assert_contains "$log" "compose" "docker compose invoked"
    assert_contains "$log" "exec" "compose exec subcommand"
    assert_contains "$log" "claude-sidecar" "exec target service name"
    assert_contains "$log" "ls" "user command passed through"

    teardown_sandbox
}

test_bare_invocation_runs_claude_in_container() {
    setup_sandbox
    cat > "$SANDBOX/home/.claude-sidecar/config.yaml" <<'YAML'
image: img
host_network: false
YAML
    touch "$SANDBOX/project/compose.sidecar.yml"

    run_wrapper >/dev/null 2>&1
    rc=$?
    assert_eq "$rc" "0" "exit code for bare invocation"
    log="$(docker_log)"
    assert_contains "$log" "exec" "compose exec is invoked"
    # The bare invocation should run `claude` in the claude-sidecar service:
    assert_contains "$log" "claude-sidecar" "service name claude-sidecar"
    # `claude` appears as the command (also as the service name's prefix, but
    # the standalone arg must be present too — count occurrences indirectly):
    # The bare invocation must pass 'claude' as the *command* arg to
    # `docker compose exec claude-sidecar claude`. Look for a standalone arg
    # line that is exactly 'ARG: claude'.
    if ! grep -qxF 'ARG: claude' "$SANDBOX/docker.log"; then
        fail "expected 'claude' arg to be passed; full log:
$log"
    fi

    teardown_sandbox
}

test_exec_warns_on_shadow_drift() {
    setup_sandbox
    cat > "$SANDBOX/home/.claude-sidecar/config.yaml" <<'YAML'
image: img
host_network: false
YAML
    mkdir -p "$SANDBOX/project/.sidecar" "$SANDBOX/home/.claude-sidecar/state"
    echo ".env" > "$SANDBOX/project/.sidecar/shadow"
    # Stored hash that doesn't match what compute_shadow_hash will produce.
    printf 'bogus-prior-hash\n' > "$SANDBOX/home/.claude-sidecar/state/project.sha"
    touch "$SANDBOX/project/compose.sidecar.yml"

    out="$(run_wrapper exec "ls" 2>&1)"
    rc=$?
    assert_eq "$rc" "0" "exec still proceeds on drift"
    assert_contains "$out" "drift" "warning mentions drift"

    teardown_sandbox
}

test_exec_silent_when_no_drift() {
    setup_sandbox
    cat > "$SANDBOX/home/.claude-sidecar/config.yaml" <<'YAML'
image: img
host_network: false
YAML
    mkdir -p "$SANDBOX/project/.sidecar" "$SANDBOX/home/.claude-sidecar/state"
    echo ".env" > "$SANDBOX/project/.sidecar/shadow"
    touch "$SANDBOX/project/compose.sidecar.yml"

    # Compute the expected hash using the SAME bash function the wrapper uses,
    # then store it as the prior hash so drift detection sees a match.
    expected="$(
        (cd "$SANDBOX/project" && {
            cat ".sidecar/shadow" 2>/dev/null
        } | shasum -a 256 | awk '{print $1}')
    )"
    echo "$expected" > "$SANDBOX/home/.claude-sidecar/state/project.sha"

    err="$(run_wrapper exec "ls" 2>&1 >/dev/null)"
    case "$err" in
        *drift*) fail "should be silent when no drift; got stderr: $err" ;;
    esac

    teardown_sandbox
}

test_up_writes_overlay_file_and_calls_compose_up() {
    setup_sandbox
    cat > "$SANDBOX/home/.claude-sidecar/config.yaml" <<'YAML'
image: img
host_network: false
YAML

    run_wrapper up >/dev/null 2>&1
    rc=$?
    assert_eq "$rc" "0" "exit code"

    if [ ! -f "$SANDBOX/project/compose.sidecar.yml" ]; then
        fail "expected $SANDBOX/project/compose.sidecar.yml to exist"
    fi
    log="$(docker_log)"
    assert_contains "$log" "compose" "docker invoked with compose"
    assert_contains "$log" "compose.sidecar.yml" "compose given the overlay file via -f"
    assert_contains "$log" "up" "compose subcommand is up"

    teardown_sandbox
}

test_up_records_drift_hash() {
    setup_sandbox
    cat > "$SANDBOX/home/.claude-sidecar/config.yaml" <<'YAML'
image: img
host_network: false
YAML
    mkdir -p "$SANDBOX/project/.sidecar"
    echo ".env" > "$SANDBOX/project/.sidecar/shadow"

    run_wrapper up >/dev/null 2>&1

    hash_file="$SANDBOX/home/.claude-sidecar/state/project.sha"
    if [ ! -f "$hash_file" ]; then
        fail "expected hash file at $hash_file"
    fi

    teardown_sandbox
}

test_up_merges_compose_sidecar_local_when_present() {
    setup_sandbox
    cat > "$SANDBOX/home/.claude-sidecar/config.yaml" <<'YAML'
image: img
host_network: false
YAML
    touch "$SANDBOX/project/compose.sidecar-local.yml"

    run_wrapper up >/dev/null 2>&1
    log="$(docker_log)"
    assert_contains "$log" "compose.sidecar-local.yml" "compose given the local override via -f"

    teardown_sandbox
}

test_spec_propagates_host_network_true() {
    setup_sandbox
    cat > "$SANDBOX/home/.claude-sidecar/config.yaml" <<'YAML'
image: img
host_network: true
YAML

    run_wrapper gen-overlay >/dev/null 2>&1
    spec="$(docker_stdin)"
    assert_contains "$spec" "host_network: true" "spec carries host_network: true"

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
