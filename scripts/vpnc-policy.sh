#!/bin/sh
# Assign tunnel IP and install VPN routes in policy table 200 (LAN clients only).
TABLE=200

case "$reason" in
pre-init)
	;;
connect*)
	if [ -z "$TUNDEV" ]; then
		exit 0
	fi
	ip link set dev "$TUNDEV" up 2>/dev/null || true
	if [ -n "$INTERNAL_IP4_ADDRESS" ]; then
		masklen="${INTERNAL_IP4_MASKLEN:-32}"
		ip addr replace "$INTERNAL_IP4_ADDRESS/$masklen" dev "$TUNDEV" 2>/dev/null || \
			ip addr add "$INTERNAL_IP4_ADDRESS/$masklen" dev "$TUNDEV" 2>/dev/null || true
	fi
	if [ -n "$INTERNAL_IP4_MTU" ]; then
		ip link set dev "$TUNDEV" mtu "$INTERNAL_IP4_MTU" 2>/dev/null || true
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
	ip route replace default dev "$TUNDEV" table "$TABLE" 2>/dev/null || true
	;;
disconnect*)
	if [ -n "$TUNDEV" ]; then
		ip addr flush dev "$TUNDEV" 2>/dev/null || true
	fi
	;;
esac
exit 0
