# Claude Sidecar

Headless Claude Code container that runs alongside a project, with toolchains
installed in-image (via mise) and a network egress firewall.

## Project Purpose

Provides a Docker image with Claude Code that:
- Runs Claude Code as a non-root user with the project mounted at its real host path
- Installs per-project toolchains on demand via mise (node, go, python, …) from
  `.tool-versions` / `mise.toml`
- Reaches project services (db, redis, …) over the compose network by service name
- Includes a network firewall with an allowed-domain whitelist
- Seeds Claude auth + config from the host once, then persists them in a volume
  (no re-auth / re-onboarding across container recreation)

## Project Structure

```
scripts/
  entrypoint.sh     # Container entrypoint (firewall init, seed auth, drop to claude user)
  init-firewall.sh  # iptables + ipset egress firewall
  wrappers/
    claude          # Drops `exec claude` to the non-root claude user; injects CLAUDE_YOLO
.sidecar/           # Config dir (allowed-domains.txt)
examples/           # Example compose files + CLAUDE.md template
```

## Build & Test

```bash
docker compose build claude                  # Build image
docker compose up -d claude                  # Start container (firewall + seed run on boot)
docker compose exec claude claude            # Run Claude interactively (drops to claude user)
docker compose exec -e CLAUDE_YOLO=1 claude claude   # YOLO mode
```

## Key Components

- **Image** (`Dockerfile`): Debian slim base + mise (prebuilt toolchains on glibc) +
  Claude Code native binary. No Docker CLI, no command bridge.
- **Entrypoint** (`scripts/entrypoint.sh`): runs as root to init the firewall and
  seed `~/.claude` from read-only `/seed/*` mounts (only if absent), adjusts the
  claude user's UID/GID from `PUID`/`PGID`, then drops to the claude user.
- **claude wrapper** (`scripts/wrappers/claude`, installed to `/usr/local/sbin`):
  ahead of the real binary on PATH; uses `runuser` to run Claude as the non-root
  `claude` user so it reads the seeded auth in `/home/claude/.claude`.
- **Firewall** (`scripts/init-firewall.sh`): ipset + iptables whitelist. Reads
  `$SIDECAR_CONFIG_DIR/allowed-domains.txt`; always adds GitHub IP ranges. NOTE:
  IPs are resolved once at startup (CDN rotation can break long-lived containers).
- **Toolchains**: mise, activated for non-interactive shells via `BASH_ENV`. Tool
  data lives under `/home/claude/.local/share/mise` (in the `claude-home` volume,
  so installs persist). `MISE_TRUSTED_CONFIG_PATHS` auto-trusts project configs.

## Auth seeding

Two read-only seed mounts (see `compose.yaml`), copied into the `claude-home`
volume on first start only:
- `.credentials.json` → `~/.claude/.credentials.json` — **full** keychain blob
  (`claudeAiOauth` + `mcpOAuth`); macOS extract: `security find-generic-password -s
  "Claude Code-credentials" -w > .credentials.json`. Linux: the file already holds it.
- `~/.claude.json` → `~/.claude.json` — onboarding state, oauthAccount, per-project
  `mcpServers` (keyed by absolute path — hence mounting projects at the real host path).

Reset / re-auth: `docker compose down -v` (drops the volume, re-seeds next start).

> Known limitation: seeded OAuth credentials are shared with the host account. If
> the host re-logs-in or rotates tokens, the container copy can be invalidated.
> For a long-lived container, authenticate it independently (interactive `/login`
> inside the container, or an API key) instead of seeding.

## Environment Variables

| Variable | Purpose |
|----------|---------|
| `SIDECAR_CONFIG_DIR` | Config directory (default: `$PWD/.sidecar`) |
| `MISE_TRUSTED_CONFIG_PATHS` | Paths whose mise configs are auto-trusted |
| `CLAUDE_YOLO` | Set `1` for `--dangerously-skip-permissions` |
| `PUID` / `PGID` | Set container user UID/GID at runtime (volume file ownership; required on Linux) |

## Maintenance: keep the installer skill in sync

The `install-claude-sidecar` skill installs this image into other projects and
mirrors the compose/auth/firewall/toolchain setup here. **Any architectural change
in this repo (compose layout, seed mechanism, env vars, image base, toolchains) must
be reflected in that skill**, or installs will break.

- Skill repo: https://github.com/mithredate/skills — path `dev/skills/install-claude-sidecar/`
- When updating it, use a dedicated **git worktree** (clone the repo first if it
  isn't checked out locally), e.g.:
  ```bash
  git -C <skills-repo> worktree add -b update-claude-sidecar-skill ../skills-claude-sidecar
  ```
  Edit in the worktree, open a PR, then remove the worktree.

## References

- More about claude code (claude binary inside the claude container): ./agent-docs/claude-code.md
