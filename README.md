# Claude Sidecar

[![Build and Publish Docker Image](https://github.com/mithredate/claude-sidecar/actions/workflows/docker-publish.yml/badge.svg)](https://github.com/mithredate/claude-sidecar/actions/workflows/docker-publish.yml)

A Docker image that runs Claude Code in a sandboxed container, with a host-side wrapper that generates the per-project Compose overlay from declarative inputs. **One global volume** (auth, plugins, marketplaces, MCP) is shared across every project. **Sensitive files** are shadowed at the kernel mount layer with a fail-closed safety check at container start. **Cross-project** Claude sessions work out of the box via `extra_mounts`.

## Quick Start

**Automated setup** (recommended):

```bash
claude
/plugin add marketplace mithredate/claude-codex
/plugin install development@claude-codex
# Then type: install claude-sidecar
```

This installs:
- `~/.local/bin/claude-sidecar` — the host wrapper
- `~/.claude-sidecar/config.yaml` — user-level config (image, extra mounts, host-network mode)
- `.sidecar/shadow` in the current project — sensitive paths to hide from Claude (committed)
- Shared Docker volume `claude-sidecar-home` — auth, plugins, MCP, session history

**Run Claude**:

```bash
claude-sidecar up        # Generate compose.sidecar.yml and `docker compose up -d`
claude-sidecar           # Drop into Claude (drift-checks shadows, then exec's into the container)
claude-sidecar exec ...  # Run any command in the claude-sidecar container
```

## How It Works

For each project you `claude-sidecar up`, the wrapper:

1. Reads `~/.claude-sidecar/config.yaml` (image, host-network mode, extra mounts).
2. Reads `.sidecar/shadow` in the current project AND in every extra-mount repo.
3. Assembles an OverlaySpec and pipes it to `bridge gen-overlay` inside the image (one-shot `docker run`).
4. Writes the output to `compose.sidecar.yml` (gitignored).
5. Runs `docker compose -f compose.yml -f compose.sidecar.yml [-f compose.sidecar-local.yml] up -d`.

The generated overlay carries the `claude-sidecar` and `claude-sidecar-proxy` services, the project bind-mount, the credentials seed, the shared volume reference, and every shadow mount (file → `/dev/null`, directory → `tmpfs`). **The user's `compose.yml` is never touched.**

Inside the container, the bridge routes per-project commands (`go`, `npm`, etc.) to sidecar containers via `docker exec`, falling through to native execution when a command isn't in `.sidecar/bridge.yaml`.

## Configuration

### Bridge (`bridge.yaml`)

Create `.sidecar/bridge.yaml` to map commands to containers. See [`examples/claude-bridge.yaml`](examples/claude-bridge.yaml) for the full schema with path mapping.

Minimal example:

```yaml
version: "1"
default_container: app

containers:
  app: myproject-app-1
  php: myproject-php-1

commands:
  php:
    container: php
    exec: php
    workdir: /var/www/html
```

### Network Firewall

The container includes an optional firewall that whitelists allowed domains using `iptables` + `ipset`. Requires `NET_ADMIN` and `NET_RAW` capabilities.

**Default allowed**: GitHub (dynamic IPs), npm, Anthropic APIs.

**Customize**: Copy [`.sidecar/allowed-domains.txt.example`](.sidecar/allowed-domains.txt.example) to `.sidecar/allowed-domains.txt` and add your domains.

**Disable**: Remove the `cap_add` section from your compose file.

### User UID/GID

Default UID/GID is 1000 (Linux). Override at runtime (recommended):

```bash
docker compose run -e PUID=$(id -u) -e PGID=$(id -g) claude
```

Or at build time:

```bash
docker compose build --build-arg CLAUDE_UID=501 --build-arg CLAUDE_GID=501
```

## Cross-project sessions

You can `@../other-repo` and read/edit code across repos in one Claude session. Add the sibling to your user config:

```yaml
# ~/.claude-sidecar/config.yaml
image: ghcr.io/mithredate/claude-sidecar:latest
host_network: false
extra_mounts:
  - /Users/me/projects/mobile
  - /Users/me/projects/scripts
```

Each extra-mounted repo's `.sidecar/shadow` is automatically respected — every Claude session that mounts that repo gets the same shadow declarations the team committed there. After editing the config or any `.sidecar/shadow`, run `claude-sidecar up` to apply.

## Sensitive files (`.sidecar/shadow`)

Each repo declares its sensitive paths in a plain text file at `.sidecar/shadow`, committed with the repo:

```
# .sidecar/shadow
.env
.credentials.json
service-account.json
secrets/        # trailing slash = directory shadow (tmpfs)
```

The wrapper turns each entry into a kernel-level mount (`/dev/null` for files, `tmpfs` for directories). At container start, `entrypoint.sh` walks the shadow file and verifies every entry is actually mounted; if any aren't, the container refuses to start. Defense-in-depth — there's no way to bypass shadowing by running `docker compose up` directly.

## Custom toolchains in the Claude container

The base image is intentionally minimal. To add language runtimes, build a personal image once and point `image:` in your config at it:

```dockerfile
# ~/my-claude-sidecar/Dockerfile
FROM ghcr.io/mithredate/claude-sidecar:latest
USER root
RUN apk add --no-cache nodejs npm python3 go php83
USER claude
```

```bash
docker build -t claude-sidecar-personal ~/my-claude-sidecar
# Then in ~/.claude-sidecar/config.yaml:
#   image: claude-sidecar-personal
```

For commands you'd rather run version-pinned in your project's actual app container (matching CI), keep them in `.sidecar/bridge.yaml`. The bridge routes those; everything else runs natively in the Claude container.

## Authentication

State (auth, plugins, marketplaces, MCP servers, `.claude.json`, settings, session history) lives in a **single shared Docker volume `claude-sidecar-home`** that's reused across every project. Log in once, install plugins once, configure MCP once.

The volume is declared `external: true` in the generated overlay, so `docker compose down -v` will not delete it. Create it once (and seed `.credentials.json`) with:

```bash
scripts/sync-creds.sh
```

**MCP SSO**: `sync-creds.sh` extracts credentials from the host (macOS Keychain or `~/.claude/.credentials.json` on Linux) into `./.credentials.json`. The wrapper bind-mounts it as a read-only seed; the entrypoint promotes it into the shared volume on first start.

**Re-authenticate**: `docker volume rm claude-sidecar-home` (wipes everything — auth, plugins, MCP, session history — for all projects).

## Viewer (Optional)

Web UI for monitoring Claude sessions. Configuration included in [`examples/compose.yml`](examples/compose.yml). Access at http://localhost:3000.

## Environment Variables

| Variable | Description |
|----------|-------------|
| `CLAUDE_YOLO` | `1` for `--dangerously-skip-permissions` |
| `ANTHROPIC_API_KEY` | Optional API key (otherwise authenticate interactively) |
| `SIDECAR_CONFIG_DIR` | Config directory (default: `$PWD/.sidecar`) |

## Security

- Socket proxy limits Docker API to container list/exec only
- Network firewall restricts outbound to allowed domains
- Runs as non-root user with configurable UID/GID

## Building

```bash
docker build -t claude-sidecar .
```

## License

MIT
