# VPN Connector

Linux router (Raspberry Pi or other) with flexible WAN/LAN interfaces and Fortinet VPN via OpenConnect. Web UI on port 5000 for setup, profile management, connect/disconnect, NAT, and FortiToken entry.

## Hardware

Typical setups:

| WAN | LAN | Example |
|-----|-----|---------|
| WiFi | WiFi AP | Pi 3 + 2 USB WiFi adapters |
| Ethernet | WiFi AP | Pi with cable uplink + USB WiFi |
| WiFi | Ethernet | Pi with WiFi uplink + onboard eth |
| Ethernet | Ethernet | Mini PC with two NICs |

The setup wizard lists all detected Ethernet and WiFi interfaces. Pick **two** for WAN and LAN. A third interface (if present) is left untouched.

- **WAN**: internet uplink (existing WiFi client or cable DHCP, unchanged by setup)
- **LAN**: local network (WiFi AP via hostapd, or Ethernet with static IP + dnsmasq DHCP)

## Quick start

1. Ensure WAN has internet (WiFi associated or cable plugged in).
2. Copy this repo to the device and run:

```bash
sudo ./scripts/install.sh
```

3. Open `http://<device-ip>:5000/setup` and complete the wizard.
4. Log in at `/login`, add VPN profiles, connect.

## VPN profile example

```bash
openconnect --protocol=fortinet \
  -u my.username \
  --servercert pin-sha256:XXXXXXYYYY= \
  https://secure.foo.bar:20443
```

Enable **Save password** to reuse the password on reconnect. **FortiToken / OTP is always entered in the dashboard** when Fortinet asks for it (not stored).

## FortiToken / 2FA flow

1. Click **Connect** and enter your VPN password.
2. The dashboard shows **Token required** when Fortinet asks for a one-time code.
3. Type the code from your token app/hardware key and click **Submit token**.
4. Connection completes and LAN traffic is NATed through the VPN tunnel.

Works on Raspberry Pi and any Linux host with OpenConnect installed.

## Build

```bash
# Pi 3 / ARMv7
GOOS=linux GOARCH=arm GOARM=7 go build -o vpn-connector .

# 64-bit Pi / PC
GOOS=linux GOARCH=arm64 go build -o vpn-connector .
```

## Config files

| Path | Purpose |
|------|---------|
| `/etc/vpn-connector/config.json` | Router + admin credentials |
| `/etc/vpn-connector/profiles.json` | Saved VPN profiles (mode 0600) |
| `/run/vpn-connector/` | OpenConnect PID, state, log |

## API (authenticated)

- `GET /status` - network, VPN state, profiles
- `GET/POST/DELETE /api/profiles` - manage profiles
- `POST /api/vpn/connect` - `{"profile_id":"...","password":"..."}` (async; poll status)
- `POST /api/vpn/input` - `{"token":"123456"}` during connection
- `POST /api/vpn/disconnect`
- `POST /api/vpn/reconnect` - uses last profile + saved password (token via UI)

## Origin

This project is based on [tailscale-raspberry-router](https://github.com/pmozdzynski/tailscale-raspberry-router).

## License

MIT
