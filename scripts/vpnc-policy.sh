#!/bin/sh
# Install VPN split routes into policy table 200 only (vpn-connector manages WAN/LAN).
TABLE=200

case "$reason" in
pre-init)
	;;
connect*)
	if [ -n "$TUNDEV" ]; then
		ip link set dev "$TUNDEV" up 2>/dev/null || true
	fi
	n=${INTERNAL_IP4_NUM_ROUTES:-0}
	i=0
	while [ "$i" -lt "$n" ]; do
		eval net=\$INTERNAL_IP4_ROUTE_$i
		eval masklen=\$INTERNAL_IP4_MASKLEN_$i
		if [ -n "$net" ] && [ -n "$masklen" ]; then
			ip route replace "$net/$masklen" dev "$TUNDEV" table "$TABLE" 2>/dev/null || true
		fi
		i=$((i + 1))
	done
	if ! ip route show table "$TABLE" 2>/dev/null | grep -q '^default'; then
		ip route replace default dev "$TUNDEV" table "$TABLE" 2>/dev/null || true
	fi
	;;
disconnect*)
	;;
esac
exit 0
