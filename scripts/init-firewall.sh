#!/bin/sh
# shellcheck shell=dash
# Firewall Initialization Script
# ===============================
# Configures network isolation with allowed domains for the Claude container.
# Reads custom domains from $SIDECAR_CONFIG_DIR/allowed-domains.txt or uses defaults.
#
# Must run as root with NET_ADMIN and NET_RAW capabilities.

set -e

# Configuration (can be overridden via environment variables)
# SIDECAR_CONFIG_DIR: Base directory for claude-sidecar config files
# Set via docker-compose.yml environment section to match your project's mount point
SIDECAR_CONFIG_DIR="${SIDECAR_CONFIG_DIR:-${PWD}/.sidecar}"
ALLOWED_DOMAINS_FILE="${ALLOWED_DOMAINS_FILE:-${SIDECAR_CONFIG_DIR}/allowed-domains.txt}"

IPSET_NAME="allowed_ips"

# Default allowed domains (used if no custom config exists)
# - GitHub: fetched dynamically from api.github.com/meta
# - Package registries: npmjs
# - Anthropic services: API, Console (auth), Sentry, Statsig
DEFAULT_DOMAINS="
registry.npmjs.org
api.anthropic.com
console.anthropic.com
sentry.io
statsig.anthropic.com
statsig.com
"

# Log to stderr for visibility
log() {
    echo "[firewall] $1" >&2
}

log_error() {
    echo "[firewall] ERROR: $1" >&2
}

# Fetch GitHub IP ranges from their API
# Returns space-separated CIDR blocks for git, web, and api
fetch_github_ips() {
    log "Fetching GitHub IP ranges from api.github.com/meta..."

    local response
    response=$(curl -sf --max-time 10 "https://api.github.com/meta" 2>/dev/null) || {
        log_error "Failed to fetch GitHub IP ranges"
        return 1
    }

    # Extract git, web, and api IP ranges
    echo "$response" | jq -r '(.git + .web + .api) | .[]' 2>/dev/null || {
        log_error "Failed to parse GitHub API response"
        return 1
    }
}

# Resolve domain to IP addresses via dig
resolve_domain() {
    local domain="$1"
    # Get A records (IPv4) - multiple IPs may be returned
    dig +short A "$domain" 2>/dev/null | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' || true
}

# Create ipset and add IP addresses
setup_ipset() {
    log "Creating ipset '$IPSET_NAME'..."

    # Destroy existing set if it exists
    ipset destroy "$IPSET_NAME" 2>/dev/null || true

    # Create new hash:net set for CIDR and IP addresses
    ipset create "$IPSET_NAME" hash:net

    # Add localhost ranges
    log "Adding localhost ranges..."
    ipset add "$IPSET_NAME" 127.0.0.0/8

    # Add Docker internal DNS
    log "Adding Docker internal DNS (127.0.0.11)..."
    ipset add "$IPSET_NAME" 127.0.0.11

    # Add Docker internal network ranges (for container-to-container communication)
    # This allows reaching other compose services (db, redis, etc.) by name.
    log "Adding Docker internal network ranges..."
    # Get the container's network from routing table and allow entire subnet
    local docker_network
    docker_network=$(ip route show | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/[0-9]+ dev eth0' | awk '{print $1}' | head -1)
    if [ -n "$docker_network" ]; then
        log "  Adding Docker network: $docker_network"
        ipset add "$IPSET_NAME" "$docker_network" 2>/dev/null || true
    else
        # Fallback: add common Docker network ranges
        log "  Could not detect Docker network, adding common ranges..."
        ipset add "$IPSET_NAME" 172.16.0.0/12 2>/dev/null || true
    fi

    # Fetch and add GitHub IPs
    local github_ips
    github_ips=$(fetch_github_ips) || {
        log_error "Skipping GitHub IPs due to fetch failure"
        github_ips=""
    }

    if [ -n "$github_ips" ]; then
        log "Adding GitHub IP ranges..."
        echo "$github_ips" | while read -r cidr; do
            [ -n "$cidr" ] && ipset add "$IPSET_NAME" "$cidr" 2>/dev/null || true
        done
    fi

    # Get domains to resolve
    local domains
    if [ -f "$ALLOWED_DOMAINS_FILE" ]; then
        log "Reading custom allowed domains from $ALLOWED_DOMAINS_FILE"
        # Filter comments and empty lines
        domains=$(grep -v '^#' "$ALLOWED_DOMAINS_FILE" 2>/dev/null | grep -v '^[[:space:]]*$' || true)
    else
        log "Using default allowed domains (no custom config found)"
        domains="$DEFAULT_DOMAINS"
    fi

    # Resolve each domain and add to ipset
    echo "$domains" | while read -r domain; do
        # Skip empty lines
        [ -z "$domain" ] && continue
        # Trim whitespace
        domain=$(echo "$domain" | tr -d '[:space:]')
        [ -z "$domain" ] && continue

        log "Resolving $domain..."
        local ips
        ips=$(resolve_domain "$domain")

        if [ -n "$ips" ]; then
            echo "$ips" | while read -r ip; do
                [ -n "$ip" ] && {
                    ipset add "$IPSET_NAME" "$ip" 2>/dev/null || true
                    log "  Added $ip"
                }
            done
        else
            log "  Warning: No IPs resolved for $domain"
        fi
    done
}

# Configure iptables rules
setup_iptables() {
    log "Configuring iptables rules..."

    # Flush existing OUTPUT rules (be careful, we're only touching OUTPUT)
    iptables -F OUTPUT 2>/dev/null || true

    # Allow all loopback traffic
    iptables -A OUTPUT -o lo -j ACCEPT

    # Allow established and related connections (for return traffic)
    iptables -A OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT

    # Allow DNS queries (UDP and TCP port 53)
    # This is needed before ipset check since we need DNS to resolve domains
    iptables -A OUTPUT -p udp --dport 53 -j ACCEPT
    iptables -A OUTPUT -p tcp --dport 53 -j ACCEPT

    # Allow traffic to IPs in our allowed set
    iptables -A OUTPUT -m set --match-set "$IPSET_NAME" dst -j ACCEPT

    # REJECT (not DROP) all other outbound traffic for fast feedback
    # REJECT sends an ICMP response so the client knows immediately
    iptables -A OUTPUT -j REJECT --reject-with icmp-port-unreachable

    log "Firewall rules configured successfully"
}

# Display current rules for debugging
show_rules() {
    log "Current iptables OUTPUT rules:"
    iptables -L OUTPUT -n -v 2>&1 | while read -r line; do
        log "  $line"
    done
}

# Verify firewall is working correctly
# Returns 0 if verification passes, 1 if it fails
verify_firewall() {
    log "Verifying firewall configuration..."
    local success=0

    # Test 1: Should be able to reach api.github.com (allowed)
    log "  Testing allowed connection to api.github.com..."
    if curl -sf --max-time 5 "https://api.github.com/meta" >/dev/null 2>&1; then
        log "  ✓ Successfully connected to api.github.com (allowed)"
    else
        log_error "  ✗ Failed to connect to api.github.com - should be allowed!"
        success=1
    fi

    # Test 2: Should NOT be able to reach example.com (blocked)
    log "  Testing blocked connection to example.com..."
    if curl -sf --max-time 5 "http://example.com" >/dev/null 2>&1; then
        log_error "  ✗ Connected to example.com - should be blocked!"
        success=1
    else
        log "  ✓ Connection to example.com correctly blocked"
    fi

    if [ "$success" -eq 0 ]; then
        log "Firewall verification passed"
    else
        log_error "Firewall verification FAILED"
    fi

    return $success
}

# Main function
main() {
    log "Initializing firewall..."

    # Check if running as root
    if [ "$(id -u)" -ne 0 ]; then
        log_error "This script must run as root"
        exit 1
    fi

    # Check required tools
    for cmd in iptables ipset curl jq dig; do
        if ! command -v "$cmd" >/dev/null 2>&1; then
            log_error "Required command not found: $cmd"
            exit 1
        fi
    done

    # Setup ipset with allowed IPs
    setup_ipset

    # Configure iptables
    setup_iptables

    # Show final rules
    show_rules

    # Verify firewall is working
    if ! verify_firewall; then
        log_error "Firewall initialization failed verification"
        exit 1
    fi

    log "Firewall initialization complete"
}

main "$@"
