#!/bin/sh
# Extract host credentials into ./.credentials.json for the claude service.
#
# Why this exists:
# compose.yaml bind-mounts ./.credentials.json into the container. If the host
# file is missing, Docker silently creates a *directory* at that path and
# Claude's auth state becomes unreadable. Run this before the first
# `docker compose up`.
#
# macOS: pulls from Keychain ("Claude Code-credentials" generic password)
# Linux: copies ~/.claude/.credentials.json
#
# Usage: scripts/sync-creds.sh

set -e

TARGET="${TARGET:-./.credentials.json}"
SHARED_VOLUME="${SHARED_VOLUME:-claude-sidecar-home}"

log() { echo "[sync-creds] $*" >&2; }
die() { log "ERROR: $*"; exit 1; }

# Ensure the shared claude-home volume exists. compose.yaml declares it as
# `external: true`, so it must exist before `docker compose up`. One global
# volume means: log in once, install plugins once, configure MCP once — works
# in every project that uses claude-sidecar.
if ! docker volume inspect "$SHARED_VOLUME" >/dev/null 2>&1; then
    log "Creating shared volume '$SHARED_VOLUME' (one-time)"
    docker volume create "$SHARED_VOLUME" >/dev/null
fi

case "$(uname -s)" in
    Darwin)
        if ! command -v security >/dev/null 2>&1; then
            die "security command not found (macOS only tool)"
        fi
        log "Extracting credentials from macOS Keychain..."
        security find-generic-password -s "Claude Code-credentials" -w 2>/dev/null > "$TARGET" \
            || die "No 'Claude Code-credentials' item in Keychain. Run Claude on the host once to log in."
        ;;
    Linux)
        SRC="${HOME}/.claude/.credentials.json"
        [ -f "$SRC" ] || die "Not found: $SRC. Run Claude on the host once to log in."
        log "Copying $SRC -> $TARGET"
        cp "$SRC" "$TARGET"
        ;;
    *)
        die "Unsupported OS: $(uname -s)"
        ;;
esac

chmod 600 "$TARGET"
log "Wrote $TARGET ($(wc -c <"$TARGET" | tr -d ' ') bytes)"

# Push into any running claude services so they pick up new creds without a
# restart. The destination is a regular file in the volume (the bind-mount is
# at /run/seed/, not at the live path), so docker cp + chown works cleanly.
for svc in claude claude-host; do
    docker compose cp "$TARGET" "${svc}:/home/claude/.claude/.credentials.json" >/dev/null 2>&1 \
        && docker compose exec -T -u root "$svc" chown claude:claude /home/claude/.claude/.credentials.json >/dev/null 2>&1 \
        && log "pushed into running '$svc' container"
done
