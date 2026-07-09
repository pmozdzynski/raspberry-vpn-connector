#!/bin/sh
set -eu

INSTALL_ROOT="/opt/vpn-connector"
REPO_DIR="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"

if [ "$(id -u)" -ne 0 ]; then
	echo "Run as root: sudo $0" >&2
	exit 1
fi

log() {
	echo "==> $1"
}

install_app_files() {
	log "Installing application to $INSTALL_ROOT"
	mkdir -p "$INSTALL_ROOT/templates" "$INSTALL_ROOT/configs" "$INSTALL_ROOT/scripts"

	if [ -f "$REPO_DIR/vpn-connector" ]; then
		cp "$REPO_DIR/vpn-connector" "$INSTALL_ROOT/"
	elif command -v go >/dev/null 2>&1; then
		log "Building binary on device"
		(
			cd "$REPO_DIR"
			go build -o "$INSTALL_ROOT/vpn-connector" .
		)
	else
		echo "No prebuilt binary found and Go is not installed." >&2
		echo "Build on another machine, then copy binary into repo root:" >&2
		echo "  GOOS=linux GOARCH=arm GOARM=7 go build -o vpn-connector ." >&2
		exit 1
	fi

	chmod +x "$INSTALL_ROOT/vpn-connector"
	cp -r "$REPO_DIR/templates/." "$INSTALL_ROOT/templates/"
	cp -r "$REPO_DIR/configs/." "$INSTALL_ROOT/configs/"
	if [ -f "$REPO_DIR/scripts/vpnc-policy.sh" ]; then
		cp "$REPO_DIR/scripts/vpnc-policy.sh" "$INSTALL_ROOT/scripts/"
		chmod +x "$INSTALL_ROOT/scripts/vpnc-policy.sh"
	fi
}

install_systemd() {
	log "Installing systemd service"
	cp "$REPO_DIR/configs/vpn-connector.service" /etc/systemd/system/vpn-connector.service
	systemctl daemon-reload
	systemctl enable vpn-connector.service
	systemctl restart vpn-connector.service
}

print_access_help() {
	ips="$(hostname -I 2>/dev/null | tr ' ' '\n' | sed '/^$/d' | head -n 5)"
	log "Installation complete"
	echo
	echo "Open the setup wizard in a browser:"
	if [ -n "$ips" ]; then
		echo "$ips" | while read -r ip; do
			[ -n "$ip" ] && echo "  https://${ip}:5000/setup"
		done
	else
		echo "  https://<device-ip>:5000/setup"
	fi
	echo
	echo "Connect WAN WiFi before setup if it is not already online."
	echo
}

install_app_files
install_systemd
print_access_help
