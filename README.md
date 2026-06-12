# Claude Sidecar

[![Build and Publish Docker Image](https://github.com/mithredate/claude-sidecar/actions/workflows/docker-publish.yml/badge.svg)](https://github.com/mithredate/claude-sidecar/actions/workflows/docker-publish.yml)

A Docker image that runs headless Claude Code in a single container with project toolchains installed in-image (via mise) and an egress firewall.

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

Claude Code runs in a single container with the project mounted at its **real host
path**. Toolchains (`node`, `go`, `python`, …) are installed on demand by
[mise](https://mise.jdx.dev) from each project's `.tool-versions` / `mise.toml`, so
Claude runs build/test commands directly. It reaches your app's services
(db, redis, …) over the compose network by service name — there is no Docker socket
access or command bridge. A startup firewall restricts egress and Claude runs as a
non-root user.

## Configuration

### Toolchains (mise)

Toolchains resolve per-project from `.tool-versions` or `mise.toml` and install on
first use (prebuilt — fast, no compiler). Installed versions persist in the
`claude-home` volume. Set `MISE_TRUSTED_CONFIG_PATHS` (the example points it at your
projects root) so project configs are trusted without a manual `mise trust`.

### Network Firewall

A startup firewall whitelists allowed domains using `iptables` + `ipset`. Requires
`NET_ADMIN` and `NET_RAW` capabilities.

**Allowed by default**: GitHub (dynamic IP ranges) plus the domains in
[`.sidecar/allowed-domains.txt`](.sidecar/allowed-domains.txt) (Anthropic APIs, npm,
and the mise/toolchain download hosts). Add the domains of any MCP servers or
package registries your projects need.

> **Limitation:** domains are resolved to IPs once at startup. CDN-backed hosts
> rotate IPs, so a long-lived container may eventually fail to reach them until
> restarted. A periodic re-resolution or egress proxy is a planned follow-up.

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

Auth and config live in a persistent Docker volume (`<project>_claude-home`). On
first start, the entrypoint **seeds** that volume from two read-only mounts and
never overwrites them again, so:

- there is no re-authentication or re-onboarding across container recreation, and
- OAuth tokens refreshed inside the container persist in the volume (the seed file
  stays read-only and untouched).

Provide the two seeds before `docker compose up`:

1. **`.credentials.json`** — the credential blob. It must contain the **full**
   keychain entry (both `claudeAiOauth` *and* `mcpOAuth`), or MCP servers will be
   unauthenticated in the container.

   ```bash
   # macOS — capture the WHOLE blob (do not hand-extract a single key)
   security find-generic-password -s "Claude Code-credentials" -w > .credentials.json

   # Linux — the file already contains the full blob
   cp ~/.claude/.credentials.json .credentials.json
   ```

   Verify it has both keys:
   ```bash
   python3 -c "import json;print(list(json.load(open('.credentials.json')).keys()))"
   # -> ['claudeAiOauth', 'mcpOAuth']
   ```

2. **`~/.claude.json`** — global config (`hasCompletedOnboarding`, `oauthAccount`,
   per-project `mcpServers`). Mounted from your home dir by `compose.yaml`; no copy
   needed.

> **Linux note:** macOS Docker Desktop virtualizes file ownership, so seeds are
> readable regardless of UID. On native Linux, the container's `claude` user must
> match the host file owner — run with `-e PUID=$(id -u) -e PGID=$(id -g)` (or
> build with matching `CLAUDE_UID`/`CLAUDE_GID`), or seeding hits permission errors.

**Re-authenticate / reset:** `docker compose down -v` (removes the `claude-home`
volume so the next start re-seeds from the files above).

## Environment Variables

| Variable | Description |
|----------|-------------|
| `CLAUDE_YOLO` | `1` for `--dangerously-skip-permissions` |
| `ANTHROPIC_API_KEY` | Optional API key (otherwise authenticate interactively) |
| `SIDECAR_CONFIG_DIR` | Config directory (default: `$PWD/.sidecar`) |

## Security

- Network firewall restricts outbound traffic to allowed domains
- Runs as a non-root user with configurable UID/GID
- This repo's secret files (`.env`, `.credentials.json`) are masked from the model

> **Not yet isolated:** credentials the app code needs (DB URLs, API keys) are
> currently visible to the model in the container. Scoping those to sandbox-only
> resources is deferred future work — see CLAUDE.md.

## Building

```bash
docker build -t claude-sidecar .
```

## License

MIT
