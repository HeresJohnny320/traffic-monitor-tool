# Traffic Monitor

**See which device on your network is using the internet — right now, today, this month, this year.**
A small, fast bandwidth monitor for **pfSense** and **OPNsense**: live Kbps/Mbps/Gbps per device, WAN in/out, VLANs and VPNs, history, a read-only API, and Home Assistant integration. One ~16 MB binary using ~20 MB of RAM. Everything is configured from the browser.

![Live view](docs/screenshots/live.png)

- [What it does](#what-it-does)
- [How it works](#how-it-works)
- [What data it collects](#what-data-it-collects)
- [What you can use it for](#what-you-can-use-it-for)
- [Install](#install) · [Set up the firewall](#set-up-the-firewall) · [Using the dashboard](#using-the-dashboard)
- [Settings reference](#settings-reference) · [Modes](#modes)
- [API](#api) · [Prometheus](#prometheus-metrics) · [Home Assistant](#home-assistant)
- [Security](#security) · [Performance](#performance) · [Upgrade / uninstall](#upgrade--uninstall)
- [Troubleshooting](#troubleshooting) · [Development](#development)

---

## What it does

| | |
|---|---|
| **Live per device** | Download/upload speed of every device on your network, refreshed every few seconds, sortable |
| **Internet in/out** | Total WAN download/upload, live and per day |
| **Every interface** | LAN, each **VLAN**, **OpenVPN / WireGuard / IPsec** tunnels, WAN — live rates and totals |
| **Every network** | Group devices by LAN / VLAN / VPN subnet and filter everything by it |
| **History** *(optional)* | Usage per minute, hour, day, week, month for today, yesterday, 7/30 days, this/last month, this year or any range |
| **Top lists** | Which devices used the most internet, and which internet destinations (with hostnames and service, e.g. HTTPS, QUIC, WireGuard) |
| **Local traffic** | LAN↔LAN, inter-VLAN and VPN↔LAN traffic is shown separately and never counted as internet use |
| **Device names** | From your DNS (pfSense/OPNsense DHCP registration), fixed names, or rename in the UI |
| **Read-only API** | JSON for scripts and dashboards, plus Prometheus `/metrics` |
| **Home Assistant** | Turn on MQTT and devices + sensors appear automatically |
| **Settings in the browser** | No config files needed; changes apply in about a second |

Everything except the live dashboard is **off until you turn it on** (storage, API, SNMP, MQTT, reverse DNS).

<details><summary>More screenshots</summary>

![History](docs/screenshots/history.png)
![Settings](docs/screenshots/settings.png)

</details>

## How it works

```
 pfSense / OPNsense                                    Traffic Monitor (on the firewall itself,
┌─────────────────────────────┐                        or on any Linux/FreeBSD/Mac/Windows box)
│ softflowd / NetFlow exporter│── NetFlow v5/v9/IPFIX ─►┌────────────────────┐    ┌──────────────┐
│  (summaries of connections) │      UDP 2055           │ collector          │───►│ memory (live)│
│                             │                         │                    │    │ SQLite/Postgres/MySQL
│ SNMP (built in)             │◄── SNMP, UDP 161 ───────┤ (interface totals) │    │  (optional)  │
└─────────────────────────────┘                         └─────────┬──────────┘    └──────────────┘
                                                                  │
                     browser ◄── dashboard + Settings ────────────┤
           scripts/Grafana ◄── /api/v1, /metrics (optional) ──────┤
           Home Assistant  ◄── MQTT discovery (optional) ─────────┘
```

1. **NetFlow** — the firewall's flow exporter sends a small summary for each connection: source/destination IP and port, protocol, byte and packet counts, start/end time. Traffic Monitor adds them up per local IP. A flow between a local subnet and the outside is **internet** traffic (download or upload); between two local subnets it's **local** traffic.
2. **SNMP** *(optional)* — polls the firewall's interface byte counters every 5 s for exact WAN/LAN/VLAN/VPN totals.
3. **Live** numbers are kept in memory. **History** is written only if you turn storage on (SQLite file, PostgreSQL, or MySQL/MariaDB).
4. The **dashboard, API and MQTT** all read the same data.

Nothing inspects packet contents and nothing is logged on the firewall. Compared with packet-capture tools like ntopng, this is why it's so light: the firewall's kernel already counts the bytes, Traffic Monitor only adds them up.

## What data it collects

| Collected | Not collected |
|---|---|
| Local device IP addresses and their names (DNS, or names you give) | Packet contents, payloads |
| Bytes and packets per connection (sums only) | Website URLs, page titles, cookies |
| Remote IP + service port per device, per hour (*"talking to"*, only with storage on) | DNS queries (only reverse lookups of IPs to show names) |
| Firewall interface names and byte counters (SNMP) | Anything about devices outside your configured subnets |

- **Storage off (default):** live rates and since-midnight totals in memory only; gone on restart.
- **Storage on:** per-device bytes per minute (kept 14 days) and per hour (kept forever), destinations per hour (30 days), interface bytes. Retention is configurable. A year for 20 devices is a few MB.

## What you can use it for

- Find out what's eating your bandwidth right now ("why is Netflix buffering?") — sort devices by current download.
- Track daily/monthly usage against an **ISP data cap**; set a Home Assistant alert at 80 %.
- See how much each **VLAN** uses (IoT, guest, work) and whether the guest network hogs the line.
- Monitor **VPN** clients (WireGuard/OpenVPN road warriors) and site-to-site tunnels.
- Spot **unusual devices or uploads** — an IoT camera suddenly uploading gigabytes, a device talking to unexpected destinations.
- Feed Grafana via Prometheus for long-term dashboards, or automate with Home Assistant (pause backups while gaming, notify on heavy guest use).
- Check the WAN is delivering the speed you pay for over time.

## Install

### On pfSense or OPNsense (simplest)

Log in as **root** over SSH (pfSense: option *8 Shell*; OPNsense: option *8 Shell*) and run:

```sh
fetch -qo - https://github.com/heresjohnny320/traffic-monitor-tool/releases/latest/download/install.sh | sh
```

The installer:
- detects pfSense / OPNsense and the CPU (amd64, arm64 for Netgate 1100/2100, armv7 for SG-3100);
- downloads the release and **verifies its SHA-256 checksum**;
- installs `/usr/local/bin/traffic-monitor`, runs it as an unprivileged user `trafficmon`, and starts it at boot (logs go to *Status → System Logs*);
- writes `/usr/local/etc/traffic-monitor/traffic-monitor.yaml` with a **random admin password** (printed at the end) and NetFlow listening on **127.0.0.1 only**;
- on pfSense, **sets everything up for you**: installs softflowd and points it at Traffic Monitor from every inside interface, turns on SNMP for localhost only, and imports your networks, VLANs, VPNs, interface names and device names (DHCP leases, static mappings, DNS overrides). If you already use softflowd or SNMP for something else, they're left alone and the installer tells you what to change. The import repeats every 15 minutes and can be run any time from *Settings → Import from pfSense*; names and networks you set yourself always win. Use `SETUP=0` to skip the softflowd/SNMP part;
- adds Traffic Monitor to the firewall's own web UI: **pfSense → Status → Traffic Monitor**, **OPNsense → Reporting → Traffic Monitor**. The full dashboard and its Settings open there, behind the firewall's login;
- prints the dashboard address and the two clicks needed on the firewall (next section).

Open it from the firewall's menu, or directly at **`http://<firewall-LAN-IP>:8080`** (with the printed password).

> **Inside the firewall's web UI**, a small page (`/usr/local/www/traffic_monitor.php`) checks your firewall login and passes requests to the Traffic Monitor service, using a secret in `/usr/local/etc/traffic-monitor/gui-token`. Admins see it automatically; to give other users access, assign the privilege *WebCfg - Status: Traffic Monitor* (pfSense) or *Reporting: Traffic Monitor* (OPNsense). Re-run the installer after a firewall upgrade if the menu entry disappears.

> It runs fine on the firewall (≈20 MB RAM, negligible CPU). The default LAN rule allows the dashboard from LAN; never open the port on WAN. To keep the firewall untouched, install it on another machine instead.

### On another machine (Linux with systemd)

```sh
curl -fsSL https://github.com/heresjohnny320/traffic-monitor-tool/releases/latest/download/install.sh | sudo sh
```

Same as above, with a hardened systemd service (`journalctl -u traffic-monitor -f` for logs), settings in `/etc/traffic-monitor/`, data in `/var/lib/traffic-monitor/`. Point the firewall's NetFlow at this machine's IP and set **Settings → Accept flows from** to the firewall's IP.

### Manual / other systems (macOS, Windows, Docker hosts)

Download the archive for your platform from [Releases](https://github.com/heresjohnny320/traffic-monitor-tool/releases), extract it and run:

```sh
./traffic-monitor all            # macOS / Linux, then open http://localhost:8080
traffic-monitor.exe all          # Windows (Command Prompt or PowerShell)
```

No config file is needed; it's created when you save settings. If port 8080 is already taken, the first start uses the next free port (8081, 8082, …), saves it to `traffic-monitor.yaml`, and logs the address; change it any time under *Settings → Dashboard & security*. The installer does the same for new installs. On these systems Traffic Monitor runs only while that window is open; it isn't installed as a service. Other commands: `traffic-monitor version`, and `collector` / `web` to run the two halves separately (with storage on).

## Set up the firewall

Traffic Monitor only needs flow data. If it runs **on the firewall**, use `127.0.0.1` as the destination; otherwise use the IP of the machine running it.

**pfSense**
1. *System → Package Manager → Available Packages* → install **softflowd**.
2. *Services → softflowd*: **Enable**; **Interface**: LAN, every VLAN, OpenVPN/WireGuard — **not WAN** (after NAT every device looks like your public IP); **Host**: `127.0.0.1`; **Port**: `2055`; **Netflow version**: 9; flow tracking level *Full*. If there's a flow timeout / max-life option, set **60 s**.
3. *(Optional, WAN/VLAN/VPN totals)* *Services → SNMP*: Enable, bind to Localhost (or LAN), keep the **MibII** module. In Traffic Monitor: *Settings → SNMP* on, firewall IP `127.0.0.1`.

**OPNsense**
1. *Reporting → NetFlow*: **Listening interfaces**: LAN, VLANs, VPN (not WAN); **Version**: v9; **Destinations**: `127.0.0.1:2055`. Apply.
2. *(Optional)* *System → Firmware → Plugins* → **os-net-snmp**; *Services → Net-SNMP* → enable. In Traffic Monitor: *Settings → SNMP* on, `127.0.0.1`.

Then in the dashboard: **Settings → Networks** — add your subnets (e.g. LAN `192.168.1.0/24`, IoT `192.168.20.0/24`, WireGuard `10.6.0.0/24`). Until you do, all private addresses count as one network called *Local*. With SNMP on, **Settings → Interfaces** → click your WAN interface and set its type to **WAN**.

## Using the dashboard

| Tab | What's there |
|---|---|
| **Live** | WAN download/upload now, active devices, top device, a throughput chart, every interface's rate (SNMP) and every device's rate. Click a column to sort; filter by network at the top. |
| **History** *(storage on)* | Range presets and custom dates, *Group by* minute → month. Download/upload chart, totals, top devices with share of total, top destinations. Click a device for its own chart and *talking to* list, and to rename it. |
| **Interfaces** *(storage on)* | Totals and a chart per interface (WAN, LAN, VLANs, VPNs). |
| **Devices** *(storage on)* | Every device ever seen, first/last seen, usage in the selected range, search. |
| **Settings** | Everything below, with validation; *Save & apply* restarts collection in about a second (live rates and today's totals are kept). |

Links are shareable: `http://host:8080/#history/192.168.1.10` opens that device.

## Settings reference

Every setting is in the **Settings** tab and in `traffic-monitor.yaml` (same names). [`traffic-monitor.example.yaml`](traffic-monitor.example.yaml) has them all with comments. Settings saved from the UI are written atomically with mode `0600`; the previous file is kept as `.bak`.

| Setting | Default | Meaning |
|---|---|---|
| `database.enabled` | `false` | Keep history. Off = live-only, nothing written anywhere |
| `database.driver` | `sqlite` | `sqlite` (local file, nothing to install), `postgres`, or `mysql` (MySQL 8+ / MariaDB 10.5+) |
| `database.dsn` | `traffic-monitor.db` | SQLite file path, `postgres://user:pass@host:5432/db?sslmode=disable`, or `mysql://user:pass@host:3306/db` (add `?tls=true` for TLS) |
| `retention.minute_days` | `14` | Days of 1-minute data |
| `retention.hour_days` | `0` | Days of hourly data (0 = forever; day/week/month/year views use it) |
| `retention.peer_days` | `30` | Days of per-device destination data |
| `netflow.listen` | `:2055` | UDP address for NetFlow/IPFIX (installer on a firewall: `127.0.0.1:2055`) |
| `netflow.live_window` | `60` | Seconds averaged for live device rates; match the exporter's active timeout |
| `netflow.allowed_exporters` | `[]` | IPs/subnets allowed to send flows; empty = anyone who can reach the port |
| `snmp.enabled` | `false` | Poll interface counters |
| `snmp.target` / `port` | — / `161` | Firewall address |
| `snmp.community` / `version` | `public` / `2c` | SNMP v1/v2c community |
| `snmp.interval` | `5s` | Poll interval |
| `snmp.only_listed` | `false` | Only poll interfaces listed under `interfaces` |
| `networks` | private ranges as *Local* | List of `{name, kind: lan/vlan/vpn/dmz/other, cidr: [subnets]}` |
| `interfaces` | auto | Map `ifname: {label, kind: wan/lan/vlan/vpn/other}`; VLANs (`igb1.20`) and VPNs (`ovpns1`, `wg0`, `enc0`) are detected automatically |
| `hosts` | — | Map `ip: name` of fixed device names |
| `reverse_dns` | `false` | Name devices via reverse DNS (works well with the DNS Resolver's DHCP registration) |
| `web.listen` | `:8080` | Dashboard/API address |
| `web.ui` | `true` | Serve the dashboard (false = API only) |
| `web.settings` | `true` | Allow editing in the Settings tab (false = read-only, file only) |
| `web.username` / `password` | — | Dashboard login (the installer sets one) |
| `web.allowed_hosts` | `[]` | Extra host names for the address bar (public domains); see [Security](#security) |
| `api.enabled` | `false` | Serve `/api/v1/*` and `/metrics` |
| `api.tokens` | `[]` | Accepted tokens; empty = no token required |
| `api.cors` | `false` | Allow browser apps on other origins |
| `mqtt.enabled` | `false` | Publish to Home Assistant via MQTT discovery |
| `mqtt.broker` | `tcp://localhost:1883` | `tcp://`, `ssl://` or `ws://` |
| `mqtt.username` / `password` | — | Broker login |
| `mqtt.devices` | `named` | Client devices in HA: `named` (in `hosts` or renamed), `all` (max 500), `none` |
| `mqtt.device_list` | `[]` | Extra IPs/names to include |
| `mqtt.interfaces` / `networks` | `true` / `true` | One HA device per interface / per network |
| `mqtt.interval` | `10s` | Publish interval |
| `mqtt.topic_prefix` / `discovery_prefix` / `client_id` | `traffic-monitor` / `homeassistant` / `traffic-monitor` | MQTT naming |
| `mqtt.tls_insecure` | `false` | Skip certificate checks for `ssl://` |

## Modes

| You want | `database` | `web.ui` | `api` | `mqtt` |
|---|---|---|---|---|
| **Fresh install**: live dashboard only | off | on | off | off |
| Full history | on | on | any | any |
| Live view + API for your own tools, store nothing | off | on | on | off |
| Feed Home Assistant only (`traffic-monitor collector`) | off | off | off | on |
| Headless data source | any | off | on | any |

It refuses to start if every output is off, and refuses `traffic-monitor web` with storage off (a separate web process would have no data).

## API

Turn on in *Settings → API*. Read-only: `GET` only (anything else returns `405`). Served on the dashboard port.

**Authentication** — when tokens exist, send one as `Authorization: Bearer <token>`, `X-API-Key: <token>` or `?token=<token>`. Generate tokens in Settings. The dashboard login does not apply to the API.

```sh
curl -H "Authorization: Bearer $TOKEN" http://192.168.1.1:8080/api/v1/summary
```

### Endpoints

| Endpoint | Returns |
|---|---|
| `GET /api/v1/` | Index of endpoints, mode |
| `GET /api/v1/status` | `version`, `mode` (`full`/`live_only`), `collector_alive`, uptime, `exporters`, flow/packet counters (`flows_total`, `flows_ignored`, `flows_limited`, `packets_bad`, `packets_refused`), `snmp_enabled` |
| `GET /api/v1/summary` | `wan` (see below), `today` totals, `active_devices`, `known_devices`, `top_device`, `networks` |
| `GET /api/v1/devices` | All devices (Device objects). Filters: `?network=IoT`, `?active=true`, `?limit=10` |
| `GET /api/v1/devices/{ip or name}` | One device. A valid IP not seen yet returns zeros (keeps sensors available); unknown name → `404` |
| `GET /api/v1/interfaces` | Interfaces (needs SNMP) |
| `GET /api/v1/interfaces/{name or label}` | One interface, e.g. `igb0` or `WAN` |
| `GET /api/v1/networks` | Per network |
| `GET /api/v1/networks/{name}` | One network |
| `GET /api/v1/usage` | Per-device totals for a range: `?range=today\|yesterday\|24h\|7d\|30d\|month\|lastmonth\|year\|<N>h\|<N>d` or `?from=&to=` (unix seconds); `?network=`, `?limit=`. Storage off: only `today` |
| `GET /api/v1/history` | Time series: `?range=`, `?step=minute\|5min\|15min\|hour\|day\|week\|month` (default auto), and one of `?ip=`, `?network=`, `?interface=`. Needs storage |
| `GET /metrics` | Prometheus format |

Errors are `{"error": "..."}` with `400` (bad parameter), `401` (token), `404`, `405`, or `503` (needs storage).

### Objects

Units are in the field names: `_bps` = bits per second, `_bytes` = bytes. "today" = since local midnight (or since start in live-only mode).

**Device**
```json
{ "ip": "192.168.1.10", "name": "Gaming PC", "network": "LAN", "kind": "lan", "active": true,
  "download_bps": 14500000, "upload_bps": 1480000, "local_bps": 0,
  "today_download_bytes": 33652919090, "today_upload_bytes": 3789310155, "today_local_bytes": 550860902 }
```
**Interface** — `name`, `label`, `kind` (`wan`/`lan`/`vlan`/`vpn`/`other`), `speed_bps`, `in_bps`, `out_bps`, `today_in_bytes`, `today_out_bytes`. *In/out are from the firewall's view: WAN in = download, LAN in = upload from LAN devices.*

**Network** — `name`, `kind`, `cidrs`, `active_devices`, `download_bps`, `upload_bps`, `local_bps`, `today_download_bytes`, `today_upload_bytes`, `today_local_bytes`.

**WAN** (in summary) — `download_bps`, `upload_bps`, `download_mbps`, `upload_mbps`, `today_download_bytes`, `today_upload_bytes`, `today_download_gb`, `today_upload_gb`, `source` (`snmp` from WAN-type interfaces, or `netflow` = sum of devices), `interfaces`.

**Usage** — `{ "range", "from", "to", "download_bytes", "upload_bytes", "local_bytes", "devices": [{ "ip", "name", "network", "download_bytes", "upload_bytes", "local_bytes" }] }`

**History** — `{ "range", "from", "to", "step", "points": [{ "t", "time", "download_bytes", "upload_bytes", "local_bytes" }] }` (`in_bytes`/`out_bytes` for `?interface=`).

### Prometheus metrics

```yaml
scrape_configs:
  - job_name: traffic-monitor
    authorization: { credentials: <token> }
    static_configs: [{ targets: ["192.168.1.1:8080"] }]
```

| Metric | Labels |
|---|---|
| `traffic_monitor_collector_up`, `traffic_monitor_history_enabled`, `traffic_monitor_flows_total`, `traffic_monitor_active_devices` | — |
| `traffic_monitor_wan_{download,upload}_bits_per_second`, `traffic_monitor_wan_today_{download,upload}_bytes` | `source` |
| `traffic_monitor_device_{download,upload,local}_bits_per_second`, `traffic_monitor_device_today_{download,upload}_bytes` | `ip`, `name`, `network` |
| `traffic_monitor_interface_{in,out}_bits_per_second`, `traffic_monitor_interface_today_{in,out}_bytes` | `interface`, `label`, `kind` |
| `traffic_monitor_network_{download,upload}_bits_per_second`, `traffic_monitor_network_active_devices`, `traffic_monitor_network_today_{download,upload}_bytes` | `network`, `kind` |

Prometheus keeps its own history, so live-only mode + Prometheus is a valid setup.

## Home Assistant

**MQTT discovery (no YAML in Home Assistant):** install the *Mosquitto broker* add-on and the *MQTT* integration, then in *Settings → Home Assistant (MQTT)* turn it on and enter the broker (`tcp://homeassistant.local:1883`) and login. These appear under *Settings → Devices & services → MQTT*:

| HA device | Entities |
|---|---|
| **Traffic Monitor** | WAN download/upload (Mbit/s), WAN downloaded/uploaded today (GB), active devices, known devices, top device, collector (connectivity) |
| **Traffic Monitor WAN / IoT / …** (per interface) | In, Out, In today, Out today |
| **LAN network, IoT network, …** | Download, Upload, downloaded/uploaded today, active devices |
| **Gaming PC, …** (per `mqtt.devices`) | Download, Upload, downloaded/uploaded today, active |

Rates use `data_rate`, totals `data_size` + `total_increasing` (works with statistics and utility meters; resets at midnight). Entities go *unavailable* when Traffic Monitor stops. Renaming a device updates it in HA.

**REST sensors (no MQTT)** — enable the API and use e.g.:

```yaml
rest:
  - resource: http://192.168.1.1:8080/api/v1/summary
    headers: { Authorization: !secret traffic_monitor_auth }   # "Bearer <token>"
    scan_interval: 10
    sensor:
      - name: Internet download
        value_template: "{{ value_json.wan.download_mbps }}"
        unit_of_measurement: "Mbit/s"
        device_class: data_rate
        state_class: measurement
      - name: Internet downloaded today
        value_template: "{{ value_json.wan.today_download_gb }}"
        unit_of_measurement: "GB"
        device_class: data_size
        state_class: total_increasing
```

## Security

- **Nothing is exposed by default** beyond the live dashboard; API and MQTT are off. Keep the dashboard port off WAN.
- **Dashboard login** — the installer sets a random admin password. Without a login the Settings page shows a warning, since anyone on the network could change settings. The password is never sent back to the browser.
- **CSRF** — every change needs a custom request header that other websites cannot send, so a malicious page can't change settings or rename devices through your browser.
- **DNS rebinding** — requests are only accepted for IP addresses, single-label names and local suffixes (`.lan`, `.local`, `.home.arpa`, `.localdomain`, `.internal`, …) plus `web.allowed_hosts`. Add your domain there if you use a reverse proxy.
- **Headers** — strict Content-Security-Policy, no framing, no MIME sniffing, no referrers.
- **NetFlow injection** — on a firewall install NetFlow listens on 127.0.0.1 only; otherwise set *Accept flows from* (`netflow.allowed_exporters`).
- **Flood resistance** — bounded device table (20 000), template cache (4 096), pending history, exporter list and HA devices, so spoofed packets can't exhaust memory. The decoder is fuzz-tested in CI.
- **API** — read-only; tokens compared in constant time; HTTP timeouts on all connections.
- **Privileges** — the service runs as the unprivileged `trafficmon` user (systemd unit additionally sandboxed); the settings file is `0600`.
- **Supply chain** — releases ship SHA-256 checksums that the installer verifies; CI runs `govulncheck`; Dependabot keeps dependencies current.
- Use a reverse proxy for HTTPS if you access it beyond your LAN.

## Performance

Measured at ~1 000 flow records/second (far more than a busy home network): **~18 MB RAM** live-only, **~22 MB** with SQLite, CPU below 5 % of one core. Settings apply/shutdown in ~20 ms. On the firewall, softflowd adds a few MB.

## Upgrade / uninstall

```sh
# upgrade (keeps settings and history) — just run the installer again
fetch -qo - https://github.com/heresjohnny320/traffic-monitor-tool/releases/latest/download/install.sh | sh
# specific version
fetch -qo - .../install.sh | VERSION=v1.2 sh
# uninstall (keeps settings/history); PURGE=1 also deletes them
fetch -qo - .../install.sh | sh -s uninstall
fetch -qo - .../install.sh | PURGE=1 sh -s uninstall
```

(On Linux use `curl -fsSL … | sudo sh`.) Service control: pfSense `service traffic_monitor.sh restart`, OPNsense/FreeBSD `service traffic_monitor restart`, Linux `systemctl restart traffic-monitor`.

## Troubleshooting

| Symptom | Check |
|---|---|
| No devices in Live | Is the exporter sending? `GET /api/v1/status` (or the green dot's tooltip) shows `exporters` and `flows_total`. Flows only from WAN? Export from LAN/VLAN interfaces. If set, does *Accept flows from* include the sender? |
| Everything shows as one IP | You're exporting from WAN (post-NAT). Use inside interfaces. |
| Live rates look bursty | Set the exporter's active timeout to 60 s and `live_window` to match. |
| No interfaces | Turn on SNMP in Settings and the firewall; on pfSense keep the MibII module. |
| "host name … is not allowed" | Opening it by a public domain — add it under *Allowed host names*. |
| Local/inter-VLAN numbers look doubled | Both VLANs export the same flow; internet totals are unaffected. |
| IPv6 device appears several times | Privacy addresses; add your prefix to a network and name them. |
| Logs | pfSense/OPNsense *Status → System Logs* (tag `traffic-monitor`); Linux `journalctl -u traffic-monitor` |

## Development

```sh
go test -race ./...                        # unit + end-to-end tests (incl. MQTT with an embedded broker)
go test ./internal/netflow -fuzz=FuzzDecode # fuzz the NetFlow decoder
go run ./cmd/fakeflow -to 127.0.0.1:2055   # synthetic NetFlow for trying the UI
go run . all                                # dashboard on :8080
```

Layout: `internal/netflow` (v5/v9/IPFIX decoder), `internal/collector` (aggregation, SNMP), `internal/live` (in-memory state), `internal/store` (SQLite/Postgres/MySQL), `internal/snapshot` (shared view), `internal/web` (dashboard, settings, API, metrics; static UI embedded, no external scripts), `internal/hass` (MQTT discovery), `gui/` (pfSense/OPNsense web UI pages), `install.sh`.

**CI** (`.github/workflows/ci.yml`) runs on every pull request: gofmt, vet, race tests, 60 s fuzzing, govulncheck, shellcheck, JS syntax, and builds for FreeBSD amd64/arm64/armv7 (pfSense/OPNsense), Linux amd64/arm64/armv7, macOS and Windows (downloadable from the run).
**Releases**: every push to `main` (except docs-only changes) runs `.github/workflows/release.yml`, which tests, builds, and publishes the next version (v1.0, v1.1, … v1.9, v2.0, …) with archives, `SHA256SUMS` and `install.sh` with this repository filled in.
