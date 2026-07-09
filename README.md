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

### Bare device: one command (recommended)

On a **fresh** Raspberry Pi / Debian system with network and `curl` or `wget`, run as root:

```bash
curl -fsSL https://raw.githubusercontent.com/pmozdzynski/raspberry-vpn-connector/tailscaled/scripts/bootstrap-device.sh | sh
```

Or with `wget`:

```bash
wget -qO- https://raw.githubusercontent.com/pmozdzynski/raspberry-vpn-connector/tailscaled/scripts/bootstrap-device.sh | sh
```

> **Branch:** The `tailscaled` bootstrap script clones the `tailscaled` branch by default. For `master`, use the `master` URL or pass `BRANCH=master` to `sh` (not to `curl`).

```bash
curl -fsSL https://raw.githubusercontent.com/pmozdzynski/raspberry-vpn-connector/master/scripts/bootstrap-device.sh | BRANCH=master sh
```

This script (`scripts/bootstrap-device.sh`) will:

1. Install **git**, **curl**, **ca-certificates**, **Go**, **openconnect**, **dnsmasq**, **iptables**, **iproute2**, and **hostapd** via `apt`
2. **Clone** this repository to `/opt/vpn-connector-src`
3. **Compile** the binary (auto-detects Pi 1 `GOARM=6`, Pi 2/3 `GOARM=7`, etc.)
4. Run **`install.sh`**: installs the app and starts the systemd service

At the end it prints URLs like:

```
http://192.168.x.x:5000/setup
```

Open that in a browser to finish configuration (WAN/LAN, DHCP, admin password).

> **Note:** The device IP comes from your home router/ISP DHCP and may be unknown beforehand. The script lists every IPv4 address it finds. If none appear yet, connect Ethernet, wait a moment, then run `ip -4 addr show`.

Optional environment variables:

```bash
REPO_URL=https://github.com/you/fork.git \
REPO_DIR=/opt/vpn-connector-src \
BRANCH=tailscaled \
  sh bootstrap-device.sh
```

On very low-memory Pis (Pi 1), enable swap before building if compilation fails:

```bash
sudo dphys-swapfile swapoff
sudo sed -i 's/^CONF_SWAPSIZE=.*/CONF_SWAPSIZE=512/' /etc/dphys-swapfile
sudo dphys-swapfile setup && sudo dphys-swapfile swapon
```

### Quick install (repo already on device)

If you already cloned the repo (or copied files via `scp`):

```bash
cd raspberry-vpn-connector

# Option A: build on another machine, copy binary here (Pi 3 example):
# GOOS=linux GOARCH=arm GOARM=7 go build -o vpn-connector .

# Option B: full bootstrap from repo (installs git/go/packages if missing):
sudo ./scripts/bootstrap-device.sh

# Option C: binary already built, app install only:
sudo ./scripts/install.sh
```

1. Ensure WAN has internet (WiFi associated or cable plugged in).
2. Open `http://<device-ip>:5000/setup` and complete the wizard.
3. Log in at `/login`, add VPN profiles, connect.

## VPN profile example

Each profile stores an OpenConnect `--protocol` value (`anyconnect`, `nc`, `gp`, `pulse`, `f5`, `fortinet`, `array`). Fortinet example:

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
4. Connection completes and LAN traffic is NATed through the VPN tunnel (corporate routes via VPN, general internet via WAN).

## Tailscale filtered exit node

Remote Tailscale clients can use this device as an exit node with **split-tunnel behavior**:

- When OpenConnect is **connected**: corporate routes and corporate DNS zones go through the VPN tunnel; everything else uses the Pi's WAN.
- When OpenConnect is **disconnected**: Tailscale exit node still works; all traffic (including attempted corporate access) is best-effort via WAN.

Enable in the dashboard under **Tailscale Exit Node**, or via API:

```bash
curl -u admin:password -X POST http://<device-ip>:5000/api/tailscale/exit-node \
  -H 'Content-Type: application/json' \
  -d '{"enabled": true}'
```

Requirements:

1. Install Tailscale on the device and run `tailscale up`.
2. Approve the exit node in the [Tailscale admin console](https://login.tailscale.com/admin/machines).
3. On remote clients, select this device as the exit node in the Tailscale client.
4. Configure **split DNS** in the Tailscale admin console: point corporate domains to the Pi's Tailscale IP shown in the dashboard.

The router disables Tailscale MagicDNS on itself (`--accept-dns=false`) so OpenConnect can resolve VPN hostnames via WAN/public DNS. Remote Tailscale clients still use split DNS you configure in the admin console.

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
- `GET/POST /api/tailscale/exit-node` - filtered Tailscale exit node toggle

## Origin

This project is based on [tailscale-raspberry-router](https://github.com/pmozdzynski/tailscale-raspberry-router).

## License

MIT
