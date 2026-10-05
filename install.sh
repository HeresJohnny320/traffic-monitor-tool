#!/bin/sh
# Traffic Monitor installer for pfSense, OPNsense, FreeBSD and Linux (systemd).
#
#   pfSense / OPNsense (Diagnostics → Command Prompt, or SSH as root, option 8):
#     fetch -qo - https://github.com/OWNER/traffic-monitor/releases/latest/download/install.sh | sh
#   Linux:
#     curl -fsSL https://github.com/OWNER/traffic-monitor/releases/latest/download/install.sh | sudo sh
#
#   sh install.sh [install|uninstall|status]
#
# Re-running install upgrades the binary and keeps your settings.
# Options (environment variables):
#   VERSION=v1.2     install a specific release instead of the latest
#   PORT=8080        dashboard port for a new install
#   PURGE=1          with uninstall: also delete settings and history
#   BASE_URL=...     download from a mirror instead of GitHub releases
set -eu

REPO="${REPO:-OWNER/traffic-monitor}" # the release workflow fills this in
VERSION="${VERSION:-latest}"
PORT="${PORT:-8080}"
SVC_USER="trafficmon"
BIN="/usr/local/bin/traffic-monitor"

say()  { printf '\033[1m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33m!!\033[0m  %s\n' "$*" >&2; }
die()  { printf '\033[31mxx\033[0m  %s\n' "$*" >&2; exit 1; }

[ "${TM_INSTALL_TEST:-}" = 1 ] || [ "$(id -u)" = 0 ] || die "run as root (on pfSense/OPNsense: SSH in as root or use Diagnostics → Command Prompt)"
case "$REPO" in OWNER/*) [ -n "${BASE_URL:-}" ] || die "REPO is not set: run with REPO=<github-user>/traffic-monitor" ;; esac

# ---------------------------------------------------------------- platform
OS=$(uname -s)
case "$OS" in
  FreeBSD)
    OS=freebsd
    if [ -f /etc/platform ] && grep -qi pfsense /etc/platform 2>/dev/null; then PLATFORM=pfsense
    elif [ -d /usr/local/opnsense ] || command -v opnsense-version >/dev/null 2>&1; then PLATFORM=opnsense
    else PLATFORM=freebsd; fi
    CONF_DIR=/usr/local/etc/traffic-monitor
    DATA_DIR=/var/db/traffic-monitor
    ;;
  Linux)
    OS=linux; PLATFORM=linux
    command -v systemctl >/dev/null 2>&1 || die "Linux install needs systemd"
    CONF_DIR=/etc/traffic-monitor
    DATA_DIR=/var/lib/traffic-monitor
    ;;
  *) die "unsupported system: $OS (download a binary from https://github.com/$REPO/releases)" ;;
esac
CONF="$CONF_DIR/traffic-monitor.yaml"

case "$(uname -m)" in
  amd64|x86_64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  armv7*|armv6*|arm) ARCH=arm ;;
  *) die "unsupported CPU: $(uname -m)" ;;
esac

if [ "$PLATFORM" = pfsense ]; then RC=/usr/local/etc/rc.d/traffic_monitor.sh  # pfSense starts *.sh at boot
else RC=/usr/local/etc/rc.d/traffic_monitor; fi
UNIT=/etc/systemd/system/traffic-monitor.service

download() { # url dest
  if command -v fetch >/dev/null 2>&1; then fetch -qo "$2" "$1"
  elif command -v curl >/dev/null 2>&1; then curl -fsSL -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then wget -qO "$2" "$1"
  else die "need fetch, curl or wget"; fi
}
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else sha256 -q "$1"; fi
}
random_secret() { od -An -N18 -tx1 /dev/urandom | tr -d ' \n'; }

service_ctl() { # start|stop|restart|status
  if [ "$OS" = linux ]; then systemctl "$1" traffic-monitor
  else "$RC" "$1"; fi
}

# ---------------------------------------------------------------- install
install_binary() {
  if [ -n "${BASE_URL:-}" ]; then base="$BASE_URL"
  elif [ "$VERSION" = latest ]; then base="https://github.com/$REPO/releases/latest/download"
  else base="https://github.com/$REPO/releases/download/$VERSION"; fi
  asset="traffic-monitor_${OS}_${ARCH}.tar.gz"
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT

  say "Downloading $asset ($VERSION)"
  download "$base/$asset" "$tmp/$asset" || die "download failed: $base/$asset"
  download "$base/SHA256SUMS" "$tmp/SHA256SUMS" || die "download failed: SHA256SUMS"
  want=$(grep " $asset\$" "$tmp/SHA256SUMS" | cut -d' ' -f1)
  got=$(sha256_of "$tmp/$asset")
  if [ -z "$want" ] || [ "$want" != "$got" ]; then
    die "checksum mismatch for $asset (expected $want, got $got)"
  fi
  say "Checksum OK"

  tar -xzf "$tmp/$asset" -C "$tmp"
  install -m 0755 "$tmp/traffic-monitor" "$BIN.new"
  mv -f "$BIN.new" "$BIN"
  if [ -f "$tmp/traffic-monitor.example.yaml" ]; then
    install -m 0644 "$tmp/traffic-monitor.example.yaml" "$CONF_DIR/traffic-monitor.example.yaml" 2>/dev/null || true
  fi
  say "Installed $("$BIN" version)"
}

create_user() {
  if id "$SVC_USER" >/dev/null 2>&1; then return; fi
  say "Creating system user $SVC_USER"
  if [ "$OS" = linux ]; then
    useradd --system --no-create-home --home-dir "$DATA_DIR" --shell /usr/sbin/nologin "$SVC_USER"
  else
    pw useradd -n "$SVC_USER" -c "Traffic Monitor" -d "$DATA_DIR" -s /usr/sbin/nologin -w no
  fi
}

port_in_use() {
  if [ "$OS" = linux ]; then ss -ltn 2>/dev/null | awk '{print $4}' | grep -q ":$1\$"
  else sockstat -46l 2>/dev/null | awk '{print $6}' | grep -q ":$1\$"; fi
}

write_config() {
  if [ -f "$CONF" ]; then say "Keeping existing settings in $CONF"; return; fi
  while port_in_use "$PORT"; do
    warn "port $PORT is in use, trying $((PORT + 1))"
    PORT=$((PORT + 1))
  done
  PASSWORD=$(random_secret)
  if [ "$OS" = freebsd ]; then
    # on the firewall itself flows come from localhost, so nothing else may send them
    NETFLOW_LISTEN='"127.0.0.1:2055"'
  else
    NETFLOW_LISTEN='":2055"'
  fi
  umask 077
  cat > "$CONF" <<EOF
# Traffic Monitor settings. Edit here or in the dashboard's Settings tab.
# Everything starts off except the live dashboard; turn on what you need.
netflow:
  listen: $NETFLOW_LISTEN
database:
  enabled: false
  driver: sqlite
  dsn: $DATA_DIR/traffic-monitor.db   # used once you turn storage on
web:
  listen: ":$PORT"
  username: admin
  password: "$PASSWORD"
EOF
  NEW_PASSWORD=$PASSWORD
}

install_service_freebsd() {
  cat > "$RC" <<'EOF'
#!/bin/sh
# PROVIDE: traffic_monitor
# REQUIRE: LOGIN NETWORKING syslogd
# KEYWORD: shutdown
#
# traffic_monitor_enable (bool):  YES to start at boot
# traffic_monitor_config (path):  settings file
. /etc/rc.subr

name="traffic_monitor"
rcvar="traffic_monitor_enable"
load_rc_config $name

: ${traffic_monitor_enable:="NO"}
: ${traffic_monitor_user:="trafficmon"}
: ${traffic_monitor_config:="/usr/local/etc/traffic-monitor/traffic-monitor.yaml"}
: ${traffic_monitor_dir:="/var/db/traffic-monitor"}

# daemon(8) supervises (restarts on crash), drops to the service user and
# sends output to syslog (Status → System Logs on pfSense/OPNsense)
pidfile="/var/run/${name}.pid"
command="/usr/sbin/daemon"
command_args="-r -S -T traffic-monitor -P ${pidfile} -u ${traffic_monitor_user} /usr/local/bin/traffic-monitor all -config ${traffic_monitor_config}"
start_precmd="cd ${traffic_monitor_dir}"

run_rc_command "$1"
EOF
  chmod 0755 "$RC"
  mkdir -p /etc/rc.conf.d
  echo 'traffic_monitor_enable="YES"' > /etc/rc.conf.d/traffic_monitor
  if [ "$PLATFORM" = opnsense ]; then
    # OPNsense runs these hooks at boot
    mkdir -p /usr/local/etc/rc.syshook.d/start
    printf '#!/bin/sh\n%s start\n' "$RC" > /usr/local/etc/rc.syshook.d/start/90-traffic-monitor
    chmod 0755 /usr/local/etc/rc.syshook.d/start/90-traffic-monitor
  fi
}

install_service_linux() {
  cat > "$UNIT" <<EOF
[Unit]
Description=Traffic Monitor (per-device bandwidth for pfSense/OPNsense)
After=network-online.target
Wants=network-online.target

[Service]
User=$SVC_USER
ExecStart=$BIN all -config $CONF
WorkingDirectory=$DATA_DIR
Restart=on-failure
RestartSec=3
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
ReadWritePaths=$CONF_DIR $DATA_DIR

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable traffic-monitor >/dev/null 2>&1
}

# dashboard_port reads web.listen from the settings file (hand-written or
# saved by the Settings page, which indents differently).
dashboard_port() {
  awk '
    /^[^ #]/ { inweb = ($0 ~ /^web:/) }
    inweb && $1 == "listen:" { v = $2; gsub(/["'"'"']/, "", v); n = split(v, a, ":"); print a[n]; exit }
  ' "$CONF"
}

lan_ip() {
  if [ "$OS" = linux ]; then hostname -I 2>/dev/null | awk '{print $1}'
  else
    # address of the interface that holds the default route's LAN side isn't
    # knowable in general; show the first private address
    ifconfig 2>/dev/null | awk '/inet (10|192\.168|172\.(1[6-9]|2[0-9]|3[01]))\./ {print $2; exit}'
  fi
}

do_install() {
  say "Installing Traffic Monitor on $PLATFORM ($OS/$ARCH)"
  mkdir -p "$CONF_DIR" "$DATA_DIR"
  running=no
  if [ -f "$BIN" ] && service_ctl status >/dev/null 2>&1; then running=yes; service_ctl stop >/dev/null 2>&1 || true; fi
  install_binary
  create_user
  NEW_PASSWORD=""
  write_config
  chown -R "$SVC_USER" "$CONF_DIR" "$DATA_DIR"
  chmod 0750 "$CONF_DIR" "$DATA_DIR"
  chmod 0600 "$CONF"
  if [ "$OS" = linux ]; then install_service_linux; else install_service_freebsd; fi
  service_ctl start >/dev/null 2>&1 || true
  sleep 1
  service_ctl status >/dev/null 2>&1 || warn "the service did not start; check the log (see below)"

  port=$(dashboard_port)
  [ -n "$port" ] || port=$PORT
  ip=$(lan_ip); [ -n "$ip" ] || ip="<this-machine>"
  echo
  if [ "$running" = yes ]; then
    say "Upgraded and restarted."
  else
    say "Traffic Monitor is running."
  fi
  echo "    Dashboard:  http://$ip:$port"
  if [ -n "$NEW_PASSWORD" ]; then
    echo "    Login:      admin / $NEW_PASSWORD"
    echo "                (change it under Settings → Dashboard & security)"
  fi
  echo "    Settings:   $CONF"
  if [ "$OS" = linux ]; then echo "    Logs:       journalctl -u traffic-monitor -f"
  else echo "    Logs:       Status → System Logs → General (traffic-monitor), or: grep traffic-monitor /var/log/system.log"; fi
  echo
  case "$PLATFORM" in
    pfsense) cat <<EOF
Next: send traffic data to it (nothing else is needed on the firewall).
  1. System → Package Manager → Available Packages → install "softflowd".
  2. Services → softflowd: Enable; Interface: LAN (+ VLANs, OpenVPN/WireGuard;
     NOT WAN); Host: 127.0.0.1; Port: 2055; Netflow version: 9.
  3. Optional, for WAN/VLAN/VPN totals: Services → SNMP → Enable, Bind
     Interface: Localhost; then in the dashboard Settings → SNMP: 127.0.0.1.
The dashboard is reachable from LAN by default. Never open port $port on WAN.
EOF
    ;;
    opnsense) cat <<EOF
Next: send traffic data to it (nothing else is needed on the firewall).
  1. Reporting → NetFlow: Listening interfaces: LAN (+ VLANs, VPN; NOT WAN);
     Version: v9; Destinations: 127.0.0.1:2055. Apply.
  2. Optional, for WAN/VLAN/VPN totals: System → Firmware → Plugins → install
     os-net-snmp; Services → Net-SNMP → enable; then in the dashboard
     Settings → SNMP: 127.0.0.1.
The dashboard is reachable from LAN with the default LAN rule. Never open port $port on WAN.
EOF
    ;;
    *) cat <<EOF
Next: point your firewall's NetFlow exporter (pfSense softflowd or OPNsense
Reporting → NetFlow) at $ip:2055, then open the dashboard → Settings and set
"Accept flows from" to the firewall's IP.
EOF
    ;;
  esac
}

do_uninstall() {
  say "Removing Traffic Monitor"
  service_ctl stop >/dev/null 2>&1 || true
  if [ "$OS" = linux ]; then
    systemctl disable traffic-monitor >/dev/null 2>&1 || true
    rm -f "$UNIT"; systemctl daemon-reload
  else
    rm -f "$RC" /etc/rc.conf.d/traffic_monitor /usr/local/etc/rc.syshook.d/start/90-traffic-monitor
  fi
  rm -f "$BIN"
  if [ "${PURGE:-0}" = 1 ]; then
    rm -rf "$CONF_DIR" "$DATA_DIR"
    if [ "$OS" = linux ]; then userdel "$SVC_USER" 2>/dev/null || true
    else pw userdel -n "$SVC_USER" 2>/dev/null || true; fi
    say "Removed, including settings and history."
  else
    say "Removed. Settings ($CONF_DIR) and history ($DATA_DIR) were kept; run with PURGE=1 to delete them."
  fi
}

[ "${TM_INSTALL_TEST:-}" = 1 ] && return 0 2>/dev/null # lets tests source the functions

case "${1:-install}" in
  install|upgrade) do_install ;;
  uninstall|remove) do_uninstall ;;
  status) service_ctl status; "$BIN" version ;;
  *) die "usage: sh install.sh [install|uninstall|status]" ;;
esac
