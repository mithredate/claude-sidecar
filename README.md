# Claude Sidecar

[![Build and Publish Docker Image](https://github.com/mithredate/claude-sidecar/actions/workflows/docker-publish.yml/badge.svg)](https://github.com/mithredate/claude-sidecar/actions/workflows/docker-publish.yml)

A Docker image with Claude Code that delegates command execution to sidecar containers via a secure Docker socket proxy.

## Quick Start

**Automated setup** (recommended):

```bash
claude
/plugin add marketplace mithredate/skills
/plugin install dev@skills
# Then type: install claude-sidecar
```

**Manual setup**: Copy [`examples/compose.yml`](examples/compose.yml) to your project and adjust paths/services.

**Run Claude**:

```bash
docker compose up -d claude              # Start container
docker compose exec claude claude        # Run Claude interactively
docker compose exec -e CLAUDE_YOLO=1 claude claude  # YOLO mode
docker compose down                      # Stop container
```

## How It Works

Claude runs in its own container. A bridge routes commands (`php`, `npm`, `go`, etc.) to your project's sidecar containers via dispatcher symlinks and Docker socket proxy. Symlinks are generated at container startup from `bridge.yaml` configuration.

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

## Authentication

Credentials persist in a Docker volume (`<project>_claude-config`). On first run, Claude prompts for authentication.

**MCP SSO**: Some MCP servers need host credentials. Extract and mount them:

```bash
# macOS
security find-generic-password -s "Claude Code-credentials" -w > .credentials.json

# Linux
cp ~/.claude/.credentials.json .credentials.json
```

Then mount in compose (see [`examples/compose.yml`](examples/compose.yml) for volume configuration including shadowing sensitive files from workspace).

**Re-authenticate**: `docker volume rm <project>_claude-config`

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
