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

# Seed Claude auth + config into the persistent home volume on first start.
# Copies the read-only seed mounts (/seed/*) into ~/.claude only if absent, so a
# token refreshed inside the container is never clobbered on restart. Idempotent.
SEED_DIR="/seed"
CLAUDE_HOME="/home/claude"
seed_claude_config() {
    mkdir -p "$CLAUDE_HOME/.claude"

    # Full credential blob (claudeAiOauth + mcpOAuth). Copy only if missing so
    # in-place OAuth refreshes living in the volume survive container restarts.
    if [ ! -f "$CLAUDE_HOME/.claude/.credentials.json" ] && [ -f "$SEED_DIR/credentials.json" ]; then
        log "Seeding credentials into $CLAUDE_HOME/.claude/.credentials.json"
        cp "$SEED_DIR/credentials.json" "$CLAUDE_HOME/.claude/.credentials.json"
    fi

    # Global config (hasCompletedOnboarding, oauthAccount, mcpServers). Copy only if missing.
    if [ ! -f "$CLAUDE_HOME/.claude.json" ] && [ -f "$SEED_DIR/claude.json" ]; then
        log "Seeding global config into $CLAUDE_HOME/.claude.json"
        cp "$SEED_DIR/claude.json" "$CLAUDE_HOME/.claude.json"
    fi

    # Ensure the claude user owns its home and the credential file is private.
    chown -R "$CONTAINER_USER:$CONTAINER_USER" "$CLAUDE_HOME/.claude" "$CLAUDE_HOME/.claude.json" 2>/dev/null || true
    chmod 600 "$CLAUDE_HOME/.claude/.credentials.json" 2>/dev/null || true
}

# Initialize firewall if not already done
# This runs as root and only runs once per container lifecycle
init_firewall() {
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

    # Adjust claude user UID/GID if PUID/PGID env vars are set
    adjust_user_ids

    # Seed auth + config into the persistent home volume (must run after UID adjust)
    seed_claude_config

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
