# Claude Sidecar

Headless Claude Code container that routes commands to sidecar containers via a Go bridge.

## Project Purpose

Provides a Docker image with Claude Code that:
- Uses a single dispatcher + symlinks to intercept binary calls (go, php, npm, etc.)
- Forwards calls to a Go bridge that routes them to the correct sidecar container
- Provides secure Docker socket access via proxy
- Includes network firewall with allowed domain whitelist

## Project Structure

```
cmd/bridge/             # Go bridge binary (main.go, config.go, gen_overlay.go)
scripts/
  claude-sidecar        # Host wrapper (bash). Installed to ~/.local/bin/.
  entrypoint.sh         # Container entrypoint (firewall, wrappers init,
                        # creds seed, shadow safety check)
  init-firewall.sh      # iptables + ipset firewall setup
  sync-creds.sh         # Bootstrap shared volume + seed credentials
  wrappers/
    dispatcher          # Single script that routes all commands through bridge
    <symlinks>          # Generated at startup, point to dispatcher
  test/wrapper_test.sh  # Host-side bash tests for the wrapper (PATH-stubs docker)
.sidecar/               # Config dir (bridge.yaml, allowed-domains.txt, shadow)
examples/               # Example compose and bridge configs
```

## Build & Test

```bash
docker build -t claude-sidecar .                         # Build image
go test ./...                                            # Go unit tests (bridge, gen-overlay)
bash scripts/test/wrapper_test.sh                        # Host wrapper unit tests
.test/run-tests.sh                                       # End-to-end integration tests
```

End-user workflow (after install):

```bash
claude-sidecar up        # Generate compose.sidecar.yml + docker compose up -d
claude-sidecar           # Drift-check + drop into Claude (bare invocation)
claude-sidecar exec ...  # Run arbitrary command inside the claude container
```

## Key Components

- **Bridge** (`cmd/bridge/`): Go binary inside the image. Two roles:
  1. *Command routing* — intercepts `go`, `npm`, etc. via dispatcher symlinks and runs them in sidecar containers per `.sidecar/bridge.yaml`. Falls through to native execution when a command isn't routed.
  2. *`bridge gen-overlay`* — reads an OverlaySpec YAML from stdin (project, extra mounts, shadow paths, image, host_network) and emits the `compose.sidecar.yml` that the host wrapper applies via `docker compose -f`.
- **Dispatcher** (`scripts/wrappers/dispatcher`): Single script that routes all commands through the bridge.
- **Symlinks**: Generated at startup via `bridge --init-wrappers`, point to dispatcher.
- **Firewall** (`scripts/init-firewall.sh`): Uses ipset + iptables to whitelist domains. Config from `$SIDECAR_CONFIG_DIR/allowed-domains.txt`.
- **Host wrapper** (`scripts/claude-sidecar`): Bash. Reads `~/.claude-sidecar/config.yaml` and every relevant `.sidecar/shadow` (current project + extra mounts), assembles the OverlaySpec, pipes it through `bridge gen-overlay` in a one-shot `docker run`, writes `compose.sidecar.yml`, and runs `docker compose`.
- **Shadow safety check** (`scripts/entrypoint.sh`): Fail-closed defense-in-depth. At container start, walks `<workspace>/.sidecar/shadow` and verifies each entry is actually `/dev/null`-mounted (files) or tmpfs-mounted (dirs) via `/proc/self/mounts`. Refuses to start otherwise.
- **Shared `claude-sidecar-home` volume**: One external Docker volume shared across every project. Holds auth (`.credentials.json`), plugins, marketplaces, MCP, settings, session history. Log in once, install plugins once.

## Environment Variables

| Variable | Purpose |
|----------|---------|
| `SIDECAR_CONFIG_DIR` | Config directory (default: `$PWD/.sidecar`) |
| `CLAUDE_YOLO` | Set `1` for `--dangerously-skip-permissions` |
| `PUID` | Set container user UID at runtime (for volume permissions) |
| `PGID` | Set container group GID at runtime (for volume permissions) |

## Development Workflow

The `ralph/` directory contains an autonomous agent loop for feature development. It is NOT part of the core project functionality.

When working on features:
1. Read `./ralph/claude.prompt.md` for current task context
2. Keep `./ralph/progress.txt` updated with progress

## References

- More about claude code (claude binary inside the claude container): ./agent-docs/claude-code.md