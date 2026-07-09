#!/bin/sh
# Full pre-web bootstrap on a fresh Debian / Raspberry Pi OS device.
# Installs git, runtime packages, clones the repo, installs Go, compiles, then runs install.sh.
#
# Usage (on the device, as root):
#   curl -fsSL https://raw.githubusercontent.com/pmozdzynski/raspberry-vpn-connector/tailscaled/scripts/bootstrap-device.sh | sh
#   wget -qO- https://raw.githubusercontent.com/pmozdzynski/raspberry-vpn-connector/tailscaled/scripts/bootstrap-device.sh | sh
#
# Override branch explicitly (note: env var applies to sh, not curl):
#   curl -fsSL https://raw.githubusercontent.com/.../bootstrap-device.sh | BRANCH=tailscaled sh
#
# Or after cloning:
#   sudo ./scripts/bootstrap-device.sh
set -eu

REPO_URL="${REPO_URL:-https://github.com/pmozdzynski/raspberry-vpn-connector.git}"
REPO_DIR="${REPO_DIR:-/opt/vpn-connector-src}"

resolve_branch() {
	if [ -n "${BRANCH:-}" ]; then
		echo "$BRANCH"
		return
	fi
	# When run from a local checkout (not piped via curl), use that branch.
	case "$0" in
		sh|dash|bash|/bin/sh|/bin/dash|/bin/bash) ;;
		*)
			script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
			repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
			if [ -d "$repo_root/.git" ]; then
				git -C "$repo_root" rev-parse --abbrev-ref HEAD 2>/dev/null && return
			fi
			;;
	esac
	# Default for this branch line (tailscaled feature branch).
	echo "tailscaled"
}

BRANCH="$(resolve_branch)"

if [ "$(id -u)" -ne 0 ]; then
	if command -v sudo >/dev/null 2>&1; then
		exec sudo sh "$0" "$@"
	fi
	echo "Run as root: sudo $0" >&2
	exit 1
fi

log() {
	echo "==> $1"
}

repo_is_complete() {
	[ -f "$REPO_DIR/go.mod" ] && [ -f "$REPO_DIR/main.go" ] && [ -f "$REPO_DIR/scripts/install.sh" ]
}

clone_or_update_repo() {
	if [ -d "$REPO_DIR/.git" ]; then
		if ! repo_is_complete; then
			log "Incomplete checkout at $REPO_DIR; removing and re-cloning"
			rm -rf "$REPO_DIR"
		else
			log "Updating existing repo at $REPO_DIR"
			git -C "$REPO_DIR" fetch --depth 1 origin "$BRANCH"
			git -C "$REPO_DIR" checkout -f "$BRANCH"
			git -C "$REPO_DIR" reset --hard "origin/$BRANCH"
			if ! repo_is_complete; then
				log "Update did not produce a complete tree; re-cloning"
				rm -rf "$REPO_DIR"
			else
				return
			fi
		fi
	fi

	log "Cloning $REPO_URL (branch $BRANCH) into $REPO_DIR"
	mkdir -p "$(dirname "$REPO_DIR")"
	git clone --branch "$BRANCH" --depth 1 "$REPO_URL" "$REPO_DIR"

	if ! repo_is_complete; then
		echo "Clone succeeded but source tree is incomplete (missing go.mod/main.go)." >&2
		echo "The remote branch may not contain application source yet." >&2
		echo "Expected: $REPO_URL branch $BRANCH" >&2
		echo "Remove $REPO_DIR and retry after the repository is updated." >&2
		exit 1
	fi
}
detect_goarm() {
	case "$(uname -m)" in
		armv6l) echo "6" ;;
		armv7l) echo "7" ;;
		*) echo "" ;;
	esac
}

install_base_packages() {
	if ! command -v apt-get >/dev/null 2>&1; then
		echo "This script requires apt-get (Debian / Raspberry Pi OS)." >&2
		exit 1
	fi

	log "Updating package lists"
	apt-get update

	log "Installing git, curl, ca-certificates, Go, and VPN/router packages"
	apt-get install -y \
		git curl ca-certificates \
		golang-go \
		dnsmasq openconnect vpnc-scripts iptables iproute2 hostapd \
		2>/dev/null \
		|| apt-get install -y \
			git curl ca-certificates \
			golang \
			dnsmasq openconnect vpnc-scripts iptables iproute2 hostapd
}

build_binary() {
	log "Compiling vpn-connector"
	cd "$REPO_DIR"

	export CGO_ENABLED=0
	goarm="$(detect_goarm)"
	if [ -n "$goarm" ]; then
		export GOARM="$goarm"
		log "Building for GOARM=$GOARM"
	fi

	go build -o vpn-connector .
	chmod +x vpn-connector
}

run_installer() {
	log "Running install.sh"
	sh "$REPO_DIR/scripts/install.sh"
}

install_base_packages
clone_or_update_repo
build_binary
run_installer
