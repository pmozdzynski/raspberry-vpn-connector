#!/bin/sh
# Wrapper around the system vpnc-script: install Fortinet split routes on the
# main routing table (openconnect never sets default for Gibtel split-tunnel).
# LAN clients are forwarded using those same main-table routes + NAT to vpn0.
VPNSCRIPT=""
for candidate in \
	/usr/share/vpnc-scripts/vpnc-script \
	/etc/vpnc/vpnc-script \
	/opt/local/etc/vpnc/vpnc-script; do
	if [ -x "$candidate" ]; then
		VPNSCRIPT="$candidate"
		break
	fi
done

case "$reason" in
pre-init)
	;;
connect*)
	if [ -n "$TUNDEV" ]; then
		ip link set dev "$TUNDEV" up 2>/dev/null || true
	fi
	if [ -n "$INTERNAL_IP4_ADDRESS" ] && [ -n "$TUNDEV" ]; then
		masklen="${INTERNAL_IP4_MASKLEN:-32}"
		ip addr replace "$INTERNAL_IP4_ADDRESS/$masklen" dev "$TUNDEV" 2>/dev/null || \
			ip addr add "$INTERNAL_IP4_ADDRESS/$masklen" dev "$TUNDEV" 2>/dev/null || true
	fi
	if [ -n "$INTERNAL_IP4_MTU" ] && [ -n "$TUNDEV" ]; then
		ip link set dev "$TUNDEV" mtu "$INTERNAL_IP4_MTU" 2>/dev/null || true
	fi
	if [ -n "$VPNSCRIPT" ]; then
		"$VPNSCRIPT" "$@"
	fi
	;;
disconnect*)
	if [ -n "$VPNSCRIPT" ]; then
		"$VPNSCRIPT" "$@"
	fi
	;;
esac
exit 0
