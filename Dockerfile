# Claude Sidecar - single-container dev image
# =============================================
# Runs headless Claude Code together with project toolchains (via mise).
# No command bridge / socket proxy: Claude runs build/test tooling directly and
# reaches project services (db, redis, ...) over the compose network by name.
#
# A startup iptables/ipset firewall whitelists egress; Claude runs as a non-root
# 'claude' user whose UID/GID can be matched to the host for clean file ownership.

FROM debian:12-slim AS runtime

# Runtime dependencies:
# - ca-certificates, curl, git: fetching toolchains + version control
# - iptables, ipset, iproute2: egress firewall (ip/ipset/iptables)
# - dnsutils: 'dig' for resolving allowed domains
# - jq: parse GitHub IP-range API in the firewall
# - bash, procps, less: shell + tooling Claude Code expects
# - xz-utils, unzip, zstd: archive formats mise uses for prebuilt toolchains
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    iptables \
    ipset \
    iproute2 \
    dnsutils \
    jq \
    bash \
    procps \
    less \
    xz-utils \
    unzip \
    zstd && \
    rm -rf /var/lib/apt/lists/*

# Install mise (system-wide). On Debian/glibc mise pulls PREBUILT node/go/python,
# so per-project `.tool-versions` / `.mise.toml` installs are fast and need no compiler.
RUN curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh && \
    mise --version

# Install Claude Code native binary (self-contained, no Node required)
RUN curl -fsSL https://claude.ai/install.sh | bash && \
    cp -L /root/.local/bin/claude /usr/local/bin/claude && \
    rm -rf /root/.local /root/.claude /tmp/*

# Entrypoint + firewall scripts
COPY --chmod=755 scripts/entrypoint.sh /scripts/entrypoint.sh
COPY --chmod=755 scripts/init-firewall.sh /scripts/init-firewall.sh

# claude wrapper: drops `docker compose exec claude claude` to the non-root user
# (container runs as root for firewall init). Placed ahead of /usr/local/bin.
COPY --chmod=755 scripts/wrappers/claude /usr/local/sbin/claude

ENV SHELL=/bin/bash

# Default user UID/GID. Override at build (--build-arg) or runtime (PUID/PGID env,
# handled by the entrypoint) so files created in mounted volumes match the host user.
ARG CLAUDE_UID=1000
ARG CLAUDE_GID=1000

# Create non-root 'claude' user with a real home + bash shell.
RUN groupadd -g ${CLAUDE_GID} claude && \
    useradd -u ${CLAUDE_UID} -g claude -m -d /home/claude -s /bin/bash claude && \
    mkdir -p /home/claude/.claude && \
    chown -R claude:claude /home/claude

# Make mise + its shims available in every shell, including the non-interactive
# `bash -c` invocations Claude Code's Bash tool uses. BASH_ENV is sourced by
# non-interactive bash; activate adds shims and per-directory tool resolution.
#
# Shims auto-install *other* versions once a shim exists, but on a cold volume no
# shim exists yet. The command_not_found_handle closes that gap: any unresolved
# command is retried through `mise exec`, which auto-installs the tool pinned by
# the current project's mise config (and still fails cleanly for real typos).
ENV PATH="/home/claude/.local/share/mise/shims:${PATH}"
ENV BASH_ENV="/etc/mise-bashenv.sh"
RUN printf '%s\n' \
    'eval "$(mise activate bash --shims)"' \
    'command_not_found_handle() {' \
    '  if mise exec -- "$@"; then return 0; else return $?; fi' \
    '}' \
    > /etc/mise-bashenv.sh && \
    chmod 644 /etc/mise-bashenv.sh

# Note: WORKDIR is set per-project via compose (working_dir).
# Container starts as root for firewall init, then drops to 'claude'.
ENTRYPOINT ["/scripts/entrypoint.sh"]
