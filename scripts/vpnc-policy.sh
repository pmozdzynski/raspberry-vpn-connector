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

write_vpn_dns_state() {
	mkdir -p /run/vpn-connector
	f=/run/vpn-connector/vpn-dns.conf
	: >"$f"
	for var in INTERNAL_IP4_DNS INTERNAL_IP4_DNS_2 INTERNAL_IP4_DNS_3 INTERNAL_IP4_DNS_4; do
		eval val=\$$var
		if [ -n "$val" ]; then
			for ip in $val; do
				echo "dns=$ip" >>"$f"
			done
		fi
	done
	if [ -n "$CISCO_DEF_DOMAIN" ]; then
		for d in $CISCO_DEF_DOMAIN; do
			echo "domain=$d" >>"$f"
		done
	fi
	if [ -n "$INTERNAL_IP4_DNS_DOMAIN" ]; then
		for d in $(echo "$INTERNAL_IP4_DNS_DOMAIN" | tr ',' ' '); do
			echo "domain=$d" >>"$f"
		done
	fi
	if [ -n "$CISCO_SPLIT_DNS_INC" ]; then
		for d in $CISCO_SPLIT_DNS_INC; do
			echo "domain=$d" >>"$f"
		done
	fi
}

clear_vpn_dns_state() {
	rm -f /run/vpn-connector/vpn-dns.conf
}

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
	write_vpn_dns_state
	;;
disconnect*)
	if [ -n "$VPNSCRIPT" ]; then
		"$VPNSCRIPT" "$@"
	fi
	clear_vpn_dns_state
	;;
esac
exit 0
