#!/bin/sh
# shellcheck shell=dash
# Claude Container Entrypoint
# ===========================
# Persistent container entrypoint that keeps the container alive.
# Claude is started manually via: docker compose exec claude claude
#
# The container starts as root to initialize the firewall, then drops
# to the 'claude' user for all subsequent operations. The firewall
# persists for the lifetime of the container.

set -e

# Configuration
FIREWALL_INIT_SCRIPT="/scripts/init-firewall.sh"
FIREWALL_MARKER="/tmp/.firewall_initialized"
CONTAINER_USER="claude"

# Log to stderr for visibility
log() {
    echo "[entrypoint] $1" >&2
}

# Adjust claude user's UID/GID based on PUID/PGID environment variables
# This allows the container to match the host user's UID/GID for proper
# file ownership in mounted volumes across different platforms
adjust_user_ids() {
    # Skip if not running as root
    if [ "$(id -u)" -ne 0 ]; then
        return 0
    fi

    # Get current UID/GID
    CURRENT_UID=$(id -u "$CONTAINER_USER")
    CURRENT_GID=$(id -g "$CONTAINER_USER")

    # Adjust GID if PGID is set and different from current
    if [ -n "$PGID" ] && [ "$PGID" != "$CURRENT_GID" ]; then
        log "Adjusting claude group GID from $CURRENT_GID to $PGID"
        sed -i "s/^claude:x:${CURRENT_GID}:/claude:x:${PGID}:/" /etc/group
    fi

    # Adjust UID if PUID is set and different from current
    if [ -n "$PUID" ] && [ "$PUID" != "$CURRENT_UID" ]; then
        log "Adjusting claude user UID from $CURRENT_UID to $PUID"
        sed -i "s/^claude:x:${CURRENT_UID}:/claude:x:${PUID}:/" /etc/passwd
    fi

    # Fix ownership of home directory if either was changed
    if { [ -n "$PUID" ] && [ "$PUID" != "$CURRENT_UID" ]; } || \
       { [ -n "$PGID" ] && [ "$PGID" != "$CURRENT_GID" ]; }; then
        chown -R "$CONTAINER_USER:$CONTAINER_USER" /home/claude
    fi
}

# Initialize firewall if not already done
# This runs as root and only runs once per container lifecycle
init_firewall() {
    # Skip unless explicitly enabled. Critical safety: if the container shares
    # the host's network namespace (network_mode: host), iptables rules here
    # mutate the HOST's OUTPUT chain and will blackhole host traffic.
    if [ "${ENABLE_FIREWALL:-0}" != "1" ]; then
        log "Firewall disabled (ENABLE_FIREWALL != 1); skipping"
        return 0
    fi

    # Skip if firewall already initialized (marker file exists)
    if [ -f "$FIREWALL_MARKER" ]; then
        log "Firewall already initialized (skipping)"
        return 0
    fi

    # Check if we have the firewall script
    if [ ! -x "$FIREWALL_INIT_SCRIPT" ]; then
        log "Warning: Firewall init script not found or not executable"
        return 0
    fi

    # Check if we're root (required for firewall setup)
    if [ "$(id -u)" -ne 0 ]; then
        log "Warning: Not running as root, skipping firewall initialization"
        return 0
    fi

    log "Initializing firewall..."
    if "$FIREWALL_INIT_SCRIPT"; then
        # Create marker file to prevent re-initialization
        touch "$FIREWALL_MARKER"
        log "Firewall initialization complete"
    else
        log "Warning: Firewall initialization failed (continuing without firewall)"
    fi
}

# Initialize wrapper symlinks
# Creates symlinks in /scripts/wrappers that point to the dispatcher
# This must run after firewall init and before dropping to claude user
init_wrappers() {
    log "Initializing command wrappers..."
    if bridge --init-wrappers /scripts/wrappers 2>&1; then
        log "Wrapper initialization complete"
    else
        log "Warning: Wrapper initialization failed"
    fi
}

# Promote credentials from the host bind-mount seed into the volume on first
# start. The host file is bind-mounted at /run/seed/.credentials.json (read-only
# seed) instead of directly at the destination because single-file bind mounts
# lock the inode against rename() — which breaks Claude's atomic write when
# refreshing tokens or completing MCP OAuth flows (DCR client info gets lost).
# Once the file lives inside the volume, atomic writes work normally.
seed_credentials() {
    SEED="/run/seed/.credentials.json"
    DEST="/home/claude/.claude/.credentials.json"

    [ -f "$SEED" ] || { log "No seed credentials at $SEED; skipping"; return 0; }

    if [ -f "$DEST" ]; then
        log "Credentials already present in volume; not re-seeding (run scripts/sync-creds.sh on host to push fresh creds into a running container)"
        return 0
    fi

    log "Seeding credentials from $SEED into volume"
    mkdir -p "$(dirname "$DEST")"
    cp "$SEED" "$DEST"
    chown "$CONTAINER_USER:$CONTAINER_USER" "$DEST"
    chmod 600 "$DEST"
}

# Verify that every path listed in <workspace>/.sidecar/shadow is actually
# /dev/null-mounted (file shadows) or tmpfs-mounted (directory shadows, when
# the shadow entry has a trailing '/'). This is defense-in-depth against
# someone bringing the container up via `docker compose up` directly,
# bypassing the host wrapper that generates compose.sidecar.yml. If the
# wrapper ran, every listed path is shadowed and this is a no-op; if it
# didn't, sensitive files would be readable and we refuse to drop into Claude.
shadow_safety_check() {
    # The workspace is the dir mounted at the same path as the container's
    # working_dir. We can't know it from entrypoint env, so probe by reading
    # the actual SIDECAR_CONFIG_DIR env (set by gen-overlay to <workspace>/.sidecar).
    SIDECAR_DIR="${SIDECAR_CONFIG_DIR:-}"
    [ -n "$SIDECAR_DIR" ] || return 0
    SHADOW_FILE="$SIDECAR_DIR/shadow"
    [ -f "$SHADOW_FILE" ] || return 0  # nothing declared, nothing to verify
    WORKSPACE="${SIDECAR_DIR%/.sidecar}"

    VIOLATIONS=""
    while IFS= read -r line || [ -n "$line" ]; do
        # strip comments + skip empty
        line="${line%%#*}"
        case "$line" in *[!\ \	]*) ;; *) continue ;; esac
        # trim
        line=$(printf '%s' "$line" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')
        case "$line" in */)
            # directory shadow: expect a tmpfs mount
            target="$WORKSPACE/${line%/}"
            if ! grep -qE "[[:space:]]${target}[[:space:]]+tmpfs[[:space:]]" /proc/self/mounts 2>/dev/null; then
                VIOLATIONS="$VIOLATIONS  $line (expected tmpfs at $target)
"
            fi
            ;;
        *)
            # file shadow: expect a character device (/dev/null bind-mount)
            target="$WORKSPACE/$line"
            if [ ! -c "$target" ]; then
                VIOLATIONS="$VIOLATIONS  $line (expected /dev/null shadow at $target)
"
            fi
            ;;
        esac
    done < "$SHADOW_FILE"

    if [ -n "$VIOLATIONS" ]; then
        log "ERROR: shadow safety check failed — these sensitive paths are NOT shadowed:"
        printf '%s' "$VIOLATIONS" >&2
        log "Bring the container up via the host wrapper: 'claude-sidecar up'"
        return 1
    fi
    return 0
}

# Run a command as the claude user (if we're currently root)
run_as_user() {
    if [ "$(id -u)" -eq 0 ]; then
        exec su -s /bin/sh "$CONTAINER_USER" -c "$*"
    else
        exec "$@"
    fi
}

# Run Claude CLI
# The wrapper at /scripts/wrappers/claude handles:
# - CLAUDE_YOLO flag injection
# - Dropping to non-root user
run_claude() {
    log "Starting Claude CLI..."
    exec claude "$@"
}

# Main entry point
main() {
    # Initialize firewall on container start (runs once as root)
    init_firewall

    # Initialize wrapper symlinks (runs once after firewall)
    init_wrappers

    # Adjust claude user UID/GID if PUID/PGID env vars are set
    adjust_user_ids

    # Promote credentials seed into the volume (no-op if already present)
    seed_credentials

    # Verify shadow declarations were applied (defense-in-depth against
    # someone running `docker compose up` directly, bypassing the host
    # wrapper that generates compose.sidecar.yml). Runs unconditionally at
    # container start — `docker compose exec` bypasses the entrypoint, so
    # this is the only place to enforce. Fail-closed: if any declared shadow
    # is missing, refuse to start. The user sees a clear error from
    # `docker compose up`.
    if ! shadow_safety_check; then
        exit 1
    fi

    # If arguments provided and first arg is "claude", run Claude
    if [ "$1" = "claude" ]; then
        shift
        run_claude "$@"
    # If any other command is provided, execute it as claude user
    elif [ $# -gt 0 ]; then
        run_as_user "$@"
    # Default: keep container alive for interactive exec sessions
    else
        log "Container started in persistent mode. Run Claude with:"
        log "  docker compose exec claude claude"
        # Keep container alive (can run as root, no security concern)
        exec tail -f /dev/null
    fi
}

main "$@"
