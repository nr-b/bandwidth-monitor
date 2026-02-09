# Bandwidth Monitor

A real-time network monitoring dashboard for Linux, written in Go. 

Single-binary deployment with an embedded web UI, optional DNS stats (AdGuard Home or NextDNS), UniFi wireless monitoring, GeoIP enrichment, and a macOS menu bar plugin.

## Features

### Traffic Tab
- **Live interface stats** — queries the kernel via netlink (`RTM_GETLINK`) every second; shows RX/TX rates, totals, packets, errors, and drops per interface
- **Interface grouping** — auto-classifies interfaces using netlink `IFLA_INFO_KIND` as Physical, VLAN, PPP/WAN, VPN, or Loopback
- **VPN routing detection** — configurable sentinel files to show whether a VPN interface is actively routing traffic
- **Real-time line chart** — Chart.js with per-interface filtering and 1-hour sliding window
- **Per-interface sparklines** — mini inline charts on each interface card
- **Top talkers by bandwidth** — live transfer rates via packet capture (gopacket/libpcap)
- **Top talkers by volume** — rolling 24-hour totals with 1-minute bucket aggregation
- **Protocol breakdown** — TCP / UDP / ICMP / Other pie chart
- **IP version breakdown** — IPv4 vs IPv6 traffic split
- **GeoIP enrichment** — country flags, ASN org names via MaxMind MMDB files
- **Reverse DNS** — resolves IPs to hostnames with in-memory cache

### DNS Tab
- **AdGuard Home or NextDNS integration** — total queries, blocked count/percentage, average latency
- **Time-series charts** — queries and blocked requests over time
- **Top clients, domains, and blocked domains** — pie charts + ranked detail tables
- **Upstream DNS servers** — response counts and average latency

### WiFi Tab
- **UniFi controller integration** — polls AP and client data from the UniFi API
- **AP cards** — per-AP status, clients, firmware, uptime, IP, MAC, live RX/TX rates
- **Clients per AP / per SSID** — pie charts and detail tables
- **Traffic per AP / per SSID** — cumulative bytes + live rates
- **Per-client traffic table** — hostname, IP, SSID, AP, signal strength (color badges), RX/TX totals, live rates
- **Search & sort** — filter clients by name/IP/MAC/SSID/AP; sort by traffic, rate, name, or signal

### NAT Tab
- **Conntrack via netlink** — uses [ti-mo/conntrack](https://github.com/ti-mo/conntrack) to query the kernel's connection tracking table directly via Netlink (no `/proc/net/nf_conntrack` needed)
- **Connection table overview** — total active connections, max table size, usage percentage (color-coded warnings at >50% and >80%)
- **IPv4 / IPv6 split** — separate counts and sub-tabs for browsing entries by IP version
- **Protocol breakdown** — TCP / UDP / ICMP / other pie chart
- **TCP state distribution** — ESTABLISHED, TIME_WAIT, SYN_SENT, CLOSE_WAIT, etc. with color-coded badges
- **NAT type detection** — classifies each flow as SNAT, DNAT, both, or none by comparing original vs reply tuples
- **Per-flow counters** — bytes and packets per connection (requires `net.netfilter.nf_conntrack_acct=1`)
- **Top sources & destinations** — ranked tables by connection count
- **Full entry table** — original and reply tuples with translated addresses highlighted, searchable and filterable by NAT type
- **macOS menu bar** — SwiftBar plugin shows connection count, table usage, IPv4/IPv6 split, and SNAT/DNAT counts

### General
- **WebSocket live updates** — 1-second refresh with automatic reconnection
- **Dark/light/auto theme** — saved to localStorage
- **Fully embedded UI** — all HTML/CSS/JS baked into the binary via `go:embed`
- **macOS menu bar plugin** — SwiftBar/xbar script showing live stats

## Requirements

- **Linux** — uses netlink (`RTM_GETLINK`, `RTM_GETADDR`) for interface stats and addresses
- **libpcap-dev** — for packet capture (top talkers)
- **nf_conntrack kernel module** — for the NAT tab (loaded automatically on most routers)
- **Go 1.24+** — to build

```bash
# Debian/Ubuntu
sudo apt install libpcap-dev

# RHEL/Fedora
sudo dnf install libpcap-devel

# Arch
sudo pacman -S libpcap
```

## Quick Start

```bash
# Build
make build

# Download GeoIP databases (optional, free)
make geoip

# Run (needs root or CAP_NET_RAW + CAP_NET_ADMIN for packet capture and netlink)
sudo ./bandwidth-monitor
```

Then open **http://localhost:8080**.

## Configuration

All configuration is via environment variables. Copy the example file and edit:

```bash
cp env.example /opt/bandwidth-monitor/.env
chmod 0600 /opt/bandwidth-monitor/.env
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `LISTEN` | `:8080` | HTTP listen address (e.g. `198.51.100.1:8080`) |
| `DEVICE` | *(all)* | Network device for packet capture (e.g. `eth0`) |
| `PROMISCUOUS` | `true` | Enable promiscuous mode for packet capture (`true`/`false`) |
| `GEO_COUNTRY` | `GeoLite2-Country.mmdb` | Path to GeoLite2 Country MMDB |
| `GEO_ASN` | `GeoLite2-ASN.mmdb` | Path to GeoLite2 ASN MMDB |
| `ADGUARD_URL` | *(disabled)* | AdGuard Home base URL (e.g. `http://adguard.example.net`) |
| `ADGUARD_USER` | | AdGuard Home username |
| `ADGUARD_PASS` | | AdGuard Home password |
| `NEXTDNS_PROFILE` | *(disabled)* | NextDNS profile ID (e.g. `abc123`) |
| `NEXTDNS_API_KEY` | | NextDNS API key (from [my.nextdns.io/account](https://my.nextdns.io/account)) |
| `UNIFI_URL` | *(disabled)* | UniFi controller URL (e.g. `https://unifi.example.net:8443`) |
| `UNIFI_USER` | | UniFi controller username |
| `UNIFI_PASS` | | UniFi controller password |
| `UNIFI_SITE` | `default` | UniFi site name |
| `VPN_STATUS_FILES` | *(none)* | Comma-separated `iface=path` pairs for VPN routing detection (e.g. `wg0=/run/wg0-active`) |
| `LOCAL_NETS` | *(auto-detect)* | Comma-separated CIDRs for RX/TX direction detection (e.g. `192.0.2.0/24,2001:db8::/48`). Auto-discovered from local interfaces if not set. |
| `SPAN_DEVICE` | *(disabled)* | SPAN/mirror port interface for direction-aware RX/TX (requires `LOCAL_NETS`; e.g. `eth1`) |

The DNS tab supports either **AdGuard Home** or **NextDNS** (mutually exclusive; AdGuard takes priority if both are configured). The WiFi tab is only shown when UniFi is configured. The NAT tab appears automatically when the `nf_conntrack` kernel module is loaded and the process has `CAP_NET_ADMIN`.

### Conntrack (NAT) Configuration

The NAT tab works out of the box on any Linux system with `nf_conntrack` loaded — no configuration needed. To enable per-flow byte/packet counters:

```bash
# Enable conntrack accounting (required for per-flow bytes/packets)
sysctl -w net.netfilter.nf_conntrack_acct=1

# Make persistent
echo 'net.netfilter.nf_conntrack_acct=1' >> /etc/sysctl.conf
```

The binary needs `CAP_NET_ADMIN` (or root) for netlink access. The included systemd service already grants this via `AmbientCapabilities`.

The UniFi integration auto-detects both legacy controllers (port 8443) and UniFi OS devices (UDM/UDR/CloudKey Gen2+, port 443).

### RX/TX Direction Detection

The top-talkers tables show per-host RX (download) and TX (upload) columns. Local network ranges are **auto-discovered** from interface addresses at startup — no configuration needed in most cases.

For SPAN/mirror port setups or if auto-discovery doesn't cover all your addresses (e.g. dynamic ISP prefixes), set `LOCAL_NETS` explicitly — similar to iftop's `-F`/`-G` flags:

```bash
LOCAL_NETS=192.0.2.0/24,2001:db8::/48
```

### SPAN / Mirror Port Mode

On a SPAN or mirror port, the kernel reports all mirrored traffic as RX on the interface, making the normal RX/TX split meaningless. Setting `SPAN_DEVICE` activates a pcap-based overlay that inspects IP headers and classifies direction using `LOCAL_NETS`:

- **src in LOCAL_NETS → remote** = upload (TX)
- **remote → dst in LOCAL_NETS** = download (RX)
- **both local** = counted as both (intra-LAN)

```bash
# In your .env
SPAN_DEVICE=eth1
LOCAL_NETS=192.0.2.0/24,2001:db8::/48
```

All other interfaces keep their normal netlink-based stats, VPN routing detection, interface grouping, etc. Only the SPAN device gets its RX/TX overridden. Requires root or `CAP_NET_RAW`.

### VPN Routing Detection (OpenWrt)

The `VPN_STATUS_FILES` variable tells bandwidth-monitor which sentinel files to check for active VPN routing. On OpenWrt, a hotplug script (`99-vpn-status`) is included that automatically creates and removes these sentinel files when WireGuard interfaces go up or down.

The script is installed automatically with the OpenWrt package to `/etc/hotplug.d/iface/99-vpn-status`. It reads `VPN_STATUS_FILES` from `/etc/bandwidth-monitor/env` — the same file the main service uses — so there is nothing extra to configure:

```bash
# In /etc/bandwidth-monitor/env
VPN_STATUS_FILES=wg0=/run/wg0-active,wg1=/run/wg1-active
```

When `wg0` comes up, the hotplug script writes a timestamp to `/run/wg0-active`. When it goes down, the file is removed. The dashboard shows a 🔒 icon on interfaces that are actively routing.

## Installation

### Pre-built packages

Pre-built packages are available from [GitHub Releases](https://github.com/awlx/bandwidth-monitor/releases) for:

| Format | Architectures | Platform |
|--------|--------------|----------|
| `.deb` | amd64, arm64 | Debian, Ubuntu, Raspbian |
| `.rpm` | amd64, arm64 | Fedora, RHEL, AlmaLinux |
| `.ipk` | x86_64, aarch64, mips_24kc, mipsel_24kc | OpenWrt 23.05 (stable) |
| `.apk` | x86_64, aarch64 | OpenWrt snapshot (nightly) |

#### Debian / Ubuntu

```bash
sudo dpkg -i bandwidth-monitor_*.deb
sudo vi /etc/bandwidth-monitor/env
sudo systemctl enable --now bandwidth-monitor
```

#### RHEL / Fedora

```bash
sudo rpm -i bandwidth-monitor-*.rpm
sudo vi /etc/bandwidth-monitor/env
sudo systemctl enable --now bandwidth-monitor
```

#### OpenWrt (stable, opkg)

```bash
opkg update && opkg install libpcap
opkg install /tmp/bandwidth-monitor_*.ipk
vi /etc/bandwidth-monitor/env
/etc/init.d/bandwidth-monitor enable
/etc/init.d/bandwidth-monitor start
```

Optional GeoIP databases:
```bash
scp GeoLite2-Country.mmdb GeoLite2-ASN.mmdb root@router:/etc/bandwidth-monitor/
/etc/init.d/bandwidth-monitor restart
```

#### OpenWrt (snapshot, apk)

```bash
apk update && apk add libpcap
apk add --allow-untrusted /tmp/bandwidth-monitor-*.apk
vi /etc/bandwidth-monitor/env
/etc/init.d/bandwidth-monitor enable
/etc/init.d/bandwidth-monitor start
```

### Using the Makefile

```bash
# Build, download GeoIP DBs, install to /opt/bandwidth-monitor,
# set up systemd service, and start
make install
```

This will:
1. Build the binary
2. Download GeoIP databases if not present
3. Copy everything to `/opt/bandwidth-monitor/`
4. Create `.env` from `env.example` (if it doesn't exist)
5. Install and enable the systemd service

```bash
# Check status
systemctl status bandwidth-monitor

# View logs
journalctl -u bandwidth-monitor -f

# Uninstall everything
make uninstall
```

### Manual

```bash
go build -o bandwidth-monitor .
sudo mkdir -p /opt/bandwidth-monitor
sudo cp bandwidth-monitor /opt/bandwidth-monitor/
sudo cp env.example /opt/bandwidth-monitor/.env
sudo chmod 0600 /opt/bandwidth-monitor/.env
# Edit .env with your settings
sudo cp bandwidth-monitor.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now bandwidth-monitor
```

### Systemd Service

The included `bandwidth-monitor.service` runs the binary with:
- `CAP_NET_RAW` and `CAP_NET_ADMIN` for packet capture and netlink access (no full root needed)
- `ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp=yes` hardening
- Environment loaded from `/opt/bandwidth-monitor/.env`

## macOS Menu Bar Plugin

A [SwiftBar](https://github.com/swiftbar/SwiftBar) / [xbar](https://xbarapp.com/) plugin is included at `swiftbar/bandwidth-monitor.5s.sh`. It shows live RX/TX rates, DNS stats, and WiFi client counts in the macOS menu bar.

**Dependencies:** `curl`, `jq` (install via `brew install jq`)

**Setup:**
1. Copy `swiftbar/bandwidth-monitor.5s.sh` to your SwiftBar plugin directory
2. Make it executable: `chmod +x bandwidth-monitor.5s.sh`
3. Edit the defaults at the top of the script, or set environment variables

**Configuration via environment variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `BW_SERVERS` | `http://localhost:8080` | Comma-separated list of servers to try in order (first reachable wins) |
| `BW_SERVER` | `http://localhost:8080` | Single server URL (used if `BW_SERVERS` is not set) |
| `BW_PREFER_IFACE` | *(auto)* | Default preferred interface for menu bar title (e.g. `ppp0`) |
| `BW_PREFER_IFACE_MAP` | *(none)* | Per-server interface override: `url=iface,url=iface` |

**Multi-server example** (edit the defaults in the script):
```bash
SERVERS="http://198.51.100.1:8080, http://203.0.113.1:8080"
PREFER_IFACE_MAP="http://198.51.100.1:8080=eth0,http://203.0.113.1:8080=ppp0"
```

The plugin tries each server in order with a 1-second timeout. The preferred interface is resolved per-server from the map. Shows a 🔒 icon when VPN routing is active.

## Architecture

```
main.go                   → entry point, env config, wires all components
collector/                → netlink-based interface stats (RTM_GETLINK/RTM_GETADDR), rates, 24h history, VPN routing
conntrack/                → netlink-based conntrack (NAT) table reader via ti-mo/conntrack
talkers/                  → pcap packet capture, per-IP tracking, 1-min bucket aggregation
handler/                  → HTTP REST API + WebSocket handler
dns/                      → common DNS provider interface
adguard/                  → AdGuard Home API client (stats, top clients/domains)
nextdns/                  → NextDNS API client (stats, top clients/domains)
unifi/                    → UniFi controller API client (APs, SSIDs, clients, live rates)
geoip/                    → MaxMind MMDB GeoIP lookups (country, ASN)
static/
  index.html              → HTML shell with three tabs
  app.js                  → all frontend JavaScript (charts, tables, WebSocket)
  style.css               → full stylesheet (dark/light themes, glassmorphism)
swiftbar/                 → macOS menu bar plugin
packaging/
  openwrt-Makefile        → OpenWrt package definition
  openwrt-files/
    bandwidth-monitor.init → procd init script for OpenWrt
    99-vpn-status         → OpenWrt hotplug script for VPN sentinel files
  postinstall.sh          → deb/rpm post-install script
  preremove.sh            → deb/rpm pre-remove script
nfpm.yaml                 → deb/rpm packaging config (nfpm)
.github/workflows/        → CI: builds deb, rpm, ipk, apk on push & tag
nfpm.yaml                 → deb/rpm packaging config (nfpm)
.github/workflows/        → CI: builds deb, rpm, ipk, apk on push & tag
env.example               → example environment configuration
bandwidth-monitor.service → systemd unit file
Makefile                  → build, install, GeoIP download targets
```

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/interfaces` | GET | Current stats for all interfaces |
| `/api/interfaces/history` | GET | 24h time-series per interface |
| `/api/talkers/bandwidth` | GET | Top 10 by current bandwidth |
| `/api/talkers/volume` | GET | Top 10 by 24h volume |
| `/api/dns` | GET | DNS summary (AdGuard Home or NextDNS) |
| `/api/wifi` | GET | UniFi WiFi summary |
| `/api/conntrack` | GET | NAT / conntrack summary (connections, states, NAT types, entries) |
| `/api/summary` | GET | Compact summary for menu bar clients |
| `/api/ws` | WS | WebSocket — pushes all data every second |

## Screenshots

<table>
  <tr>
    <th>Traffic (Light)</th>
    <th>DNS (Light)</th>
    <th>WiFi (Light)</th>
  </tr>
  <tr>
    <td><img src="docs/traffic-light.png" width="300" alt="Traffic (light)" /></td>
    <td><img src="docs/dns-light.png" width="300" alt="DNS (light)" /></td>
    <td><img src="docs/wifi-light.png" width="300" alt="WiFi (light)" /></td>
  </tr>
  <tr>
    <th>Traffic (Dark)</th>
    <th>DNS (Dark)</th>
    <th>WiFi (Dark)</th>
  </tr>
  <tr>
    <td><img src="docs/traffic-dark.png" width="300" alt="Traffic (dark)" /></td>
    <td><img src="docs/dns-dark.png" width="300" alt="DNS (dark)" /></td>
    <td><img src="docs/wifi-dark.png" width="300" alt="WiFi (dark)" /></td>
  </tr>
</table>

## Notes

- **Interface stats** and **NAT tab** use netlink — require `CAP_NET_ADMIN` (or root)
- **Top talkers** require `root` or `CAP_NET_RAW` for packet capture
- If running without root, grant `CAP_NET_RAW` + `CAP_NET_ADMIN` for full functionality
- **GeoIP** is optional — without MMDB files, country/ASN columns are simply hidden
- **NAT tab** requires `CAP_NET_ADMIN` (or root) for netlink access to the conntrack table; enable `nf_conntrack_acct=1` for per-flow byte counters
- **DNS and WiFi** tabs only appear when their respective integrations are configured
- All assets are embedded in the binary — single-file deployment, no runtime dependencies
