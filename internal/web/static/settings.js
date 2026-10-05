// Settings tab: edits traffic-monitor.yaml through /api/settings. The model is the
// same structure (and key names) as the YAML file.

function h(tag, props = {}, ...kids) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (v == null || v === false) continue;
    if (k === "class") n.className = v;
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else if (k === "value") n.value = v;
    else if (k === "checked") n.checked = !!v;
    else n.setAttribute(k, v === true ? "" : v);
  }
  for (const c of kids.flat()) if (c != null && c !== false) n.append(c instanceof Node ? c : String(c));
  return n;
}
const get = (o, path) => path.split(".").reduce((x, k) => (x == null ? undefined : x[k]), o);
function set(o, path, v) {
  const ks = path.split(".");
  let x = o;
  for (const k of ks.slice(0, -1)) x = x[k] ??= {};
  x[ks.at(-1)] = v;
}

let S = null;          // server response
let cfg = null;        // working copy
let orig = "";         // snapshot for dirty detection
let ifaceRows = [];    // interfaces map as editable rows
let hostRows = [];     // hosts map as editable rows
let root = null;
let readonly = false;
let saving = false;

const KINDS_NET = ["lan", "vlan", "vpn", "dmz", "other"];
const KINDS_IF = ["wan", "lan", "vlan", "vpn", "other"];

function serialize() {
  cfg.interfaces = Object.fromEntries(ifaceRows.filter(r => r.name.trim()).map(r => [r.name.trim(), { label: r.label.trim(), kind: r.kind }]));
  cfg.hosts = Object.fromEntries(hostRows.filter(r => r.ip.trim()).map(r => [r.ip.trim(), r.name.trim()]));
  return JSON.stringify(cfg);
}
function dirty() { return serialize() !== orig; }

export async function loadSettings(container) {
  root = container;
  const r = await fetch("api/settings");
  if (!r.ok) { root.replaceChildren(h("div", { class: "card" }, "Could not load settings: " + r.status)); return; }
  S = await r.json();
  readonly = S.mode !== "edit";
  if (!S.config) { root.replaceChildren(h("div", { class: "banner" }, S.reason)); return; }
  cfg = structuredClone(S.config);
  cfg.networks ??= [];
  cfg.api.tokens ??= [];
  cfg.mqtt.device_list ??= [];
  cfg.netflow.allowed_exporters ??= [];
  cfg.web.allowed_hosts ??= [];
  ifaceRows = Object.entries(cfg.interfaces || {}).map(([name, v]) => ({ name, label: v.label || "", kind: v.kind || "other" }));
  hostRows = Object.entries(cfg.hosts || {}).map(([ip, name]) => ({ ip, name }));
  orig = serialize();
  render();
}

// ------------------------------------------------------------------ controls
const changed = () => updateBar();
const rerender = () => { const y = window.scrollY; render(); window.scrollTo(0, y); };

function text(path, opts = {}) {
  return h("input", {
    type: opts.type || "text", value: get(cfg, path) ?? "", placeholder: opts.placeholder, disabled: readonly,
    class: opts.wide ? "wide" : null, autocomplete: "off", spellcheck: "false",
    oninput: e => { set(cfg, path, opts.num ? (e.target.value === "" ? 0 : Number(e.target.value)) : e.target.value); changed(); },
  });
}
const num = (path, opts = {}) => text(path, { ...opts, type: "number", num: true });
function secret(path, placeholder) {
  const inp = text(path, { type: "password", placeholder });
  const btn = h("button", { class: "ghost sm", type: "button", title: "Show / hide", onclick: () => { inp.type = inp.type === "password" ? "text" : "password"; } }, "👁");
  return h("span", { class: "with-btn" }, inp, btn);
}
function toggle(path, label) {
  return h("label", { class: "switch" },
    h("input", { type: "checkbox", checked: !!get(cfg, path), disabled: readonly, onchange: e => { set(cfg, path, e.target.checked); changed(); rerender(); } }),
    h("span", { class: "track" }), h("span", {}, label));
}
function select(path, options, onchange) {
  const s = h("select", { disabled: readonly, onchange: e => { set(cfg, path, e.target.value); changed(); if (onchange) rerender(); } },
    options.map(([v, l]) => h("option", { value: v }, l)));
  s.value = get(cfg, path) ?? "";
  return s;
}
function listText(path, placeholder) {
  return h("input", {
    type: "text", class: "wide", value: (get(cfg, path) || []).join(", "), placeholder, disabled: readonly,
    oninput: e => { set(cfg, path, e.target.value.split(",").map(x => x.trim()).filter(Boolean)); changed(); },
  });
}
const field = (label, control, help) => [h("div", { class: "set-l" }, label), h("div", { class: "set-c" }, control, help ? h("div", { class: "hint" }, help) : null)];
const section = (id, title, desc, ...body) => h("section", { class: "card set", id: "set-" + id },
  h("div", { class: "card-h" }, h("div", {}, h("h2", {}, title), desc ? h("div", { class: "hint" }, desc) : null)), ...body);
const grid = (...rows) => h("div", { class: "set-grid" }, ...rows.flat());

function hostPort(listen, fallbackPort) {
  const port = (listen || "").split(":").pop() || fallbackPort;
  if (/^(127\.|localhost|\[::1\])/.test(listen || "")) return `127.0.0.1:${port} (this machine only)`;
  return `${location.hostname}:${port}`;
}

// ------------------------------------------------------------------ render
function render() {
  const out = [];
  if (readonly) out.push(h("div", { class: "banner" }, "Read-only: " + S.reason));
  else if (!S.auth) out.push(h("div", { class: "banner warn" },
    "No dashboard login is set, so anyone on your network can open this page and change settings. ",
    h("a", { href: "#settings", onclick: e => { e.preventDefault(); document.getElementById("set-security").scrollIntoView({ behavior: "smooth" }); } }, "Set a username and password"), "."));

  // --- storage
  const db = cfg.database.enabled;
  out.push(section("storage", "Storage", "Off = live-only: nothing is written anywhere. On = history by day, week, month and year.",
    grid(
      field("Keep history", toggle("database.enabled", db ? "On" : "Off (live only)")),
      db && field("Where", select("database.driver", [["sqlite", "Local file (SQLite), nothing to install"], ["postgres", "PostgreSQL server"]], true)),
      db && field(cfg.database.driver === "postgres" ? "Connection URL" : "File path",
        text("database.dsn", { wide: true, placeholder: cfg.database.driver === "postgres" ? "postgres://user:pass@host:5432/traffic-monitor?sslmode=disable" : "traffic-monitor.db" }),
        cfg.database.driver === "postgres" ? "The database must exist; tables are created automatically." : "Created automatically. A year of history for 20 devices is only a few MB."),
      db && field("Minute detail", num("retention.minute_days"), "days to keep 1-minute data"),
      db && field("Hourly data", num("retention.hour_days"), "days, 0 = forever (days, weeks, months and years are built from this)"),
      db && field("Destinations", num("retention.peer_days"), "days to keep \"talking to\" data"),
    )));

  // --- firewall data
  const snmp = cfg.snmp.enabled;
  out.push(section("firewall", "Firewall data", "Nothing is installed on the firewall: it sends NetFlow and answers SNMP.",
    h("h3", { class: "set-sub" }, "NetFlow (traffic per device)"),
    grid(
      field("Listen on", text("netflow.listen", { placeholder: ":2055" }),
        `In pfSense (softflowd) or OPNsense (Reporting → NetFlow), send flows to ${hostPort(cfg.netflow.listen, 2055)}. Export from LAN/VLAN/VPN interfaces, not WAN.`),
      field("Live average", num("netflow.live_window"), "seconds; match the exporter's active flow timeout (60 recommended)"),
      field("Accept flows from", listText("netflow.allowed_exporters", "192.168.1.1"), "the firewall's IP (comma separated). Empty = anyone who can reach the port, so fake traffic could be injected."),
      field("Device names", toggle("reverse_dns", cfg.reverse_dns ? "Look up names with reverse DNS" : "Off")),
    ),
    h("h3", { class: "set-sub" }, "SNMP (WAN, VLAN and VPN interface totals)"),
    grid(
      field("SNMP", toggle("snmp.enabled", snmp ? "On" : "Off"), snmp ? null : "Without SNMP, WAN totals are the sum of all devices."),
      snmp && field("Firewall IP", text("snmp.target", { placeholder: "192.168.1.1" })),
      snmp && field("Community", secret("snmp.community", "public")),
      snmp && field("Version", select("snmp.version", [["2c", "v2c"], ["1", "v1"]])),
      snmp && field("Port", num("snmp.port")),
      snmp && field("Poll every", text("snmp.interval", { placeholder: "5s" })),
      snmp && field("Interfaces", toggle("snmp.only_listed", cfg.snmp.only_listed ? "Only those listed below" : "All interfaces that are up")),
    )));

  // --- networks
  out.push(section("networks", "Networks", "Your subnets. Traffic between a subnet and the outside is internet usage; traffic between them is local.",
    S.default_networks && cfg.networks.length === 0
      ? h("div", { class: "banner" }, "Using the built-in private ranges (10/8, 172.16/12, 192.168/16) as one network called \"Local\". Add your LAN, VLANs and VPN subnets to see them separately.")
      : null,
    h("table", { class: "tbl edit" },
      h("thead", {}, h("tr", {}, h("th", {}, "Name"), h("th", {}, "Type"), h("th", {}, "Subnets (comma separated)"), h("th", {}))),
      h("tbody", {}, cfg.networks.map((n, i) => h("tr", {},
        h("td", {}, h("input", { value: n.name, placeholder: "IoT", disabled: readonly, oninput: e => { n.name = e.target.value; changed(); } })),
        h("td", {}, (() => { const s = h("select", { disabled: readonly, onchange: e => { n.kind = e.target.value; changed(); } }, KINDS_NET.map(k => h("option", { value: k }, k.toUpperCase()))); s.value = n.kind || "lan"; return s; })()),
        h("td", { class: "grow" }, h("input", { class: "wide", value: (n.cidr || []).join(", "), placeholder: "192.168.20.0/24", disabled: readonly,
          oninput: e => { n.cidr = e.target.value.split(",").map(x => x.trim()).filter(Boolean); changed(); } })),
        h("td", {}, readonly ? null : h("button", { class: "ghost sm", type: "button", title: "Remove", onclick: () => { cfg.networks.splice(i, 1); changed(); rerender(); } }, "✕")),
      )))),
    readonly ? null : h("div", { class: "row-actions" },
      h("button", { type: "button", onclick: () => { cfg.networks.push({ name: "", kind: "vlan", cidr: [] }); changed(); rerender(); } }, "+ Add network"))));

  // --- interfaces
  const listed = new Set(ifaceRows.map(r => r.name));
  const suggestions = (S.detected_ifaces || []).filter(d => !listed.has(d.name));
  out.push(section("interfaces", "Interfaces", "Names and types for firewall interfaces seen over SNMP. Mark your internet connection as WAN.",
    h("datalist", { id: "if-detected" }, (S.detected_ifaces || []).map(d => h("option", { value: d.name }, d.label))),
    h("table", { class: "tbl edit" },
      h("thead", {}, h("tr", {}, h("th", {}, "Interface"), h("th", {}, "Label"), h("th", {}, "Type"), h("th", {}))),
      h("tbody", {}, ifaceRows.length ? ifaceRows.map((r, i) => h("tr", {},
        h("td", {}, h("input", { value: r.name, list: "if-detected", placeholder: "igb0", disabled: readonly, oninput: e => { r.name = e.target.value; changed(); } })),
        h("td", { class: "grow" }, h("input", { class: "wide", value: r.label, placeholder: "WAN", disabled: readonly, oninput: e => { r.label = e.target.value; changed(); } })),
        h("td", {}, (() => { const s = h("select", { disabled: readonly, onchange: e => { r.kind = e.target.value; changed(); } }, KINDS_IF.map(k => h("option", { value: k }, k.toUpperCase()))); s.value = r.kind; return s; })()),
        h("td", {}, readonly ? null : h("button", { class: "ghost sm", type: "button", title: "Remove", onclick: () => { ifaceRows.splice(i, 1); changed(); rerender(); } }, "✕")),
      )) : h("tr", { class: "empty-row" }, h("td", { colspan: 4 }, "None yet. Interfaces are auto-detected (VLANs and VPNs by name); add one to rename it or mark the WAN.")))),
    readonly ? null : h("div", { class: "row-actions" },
      h("button", { type: "button", onclick: () => { ifaceRows.push({ name: "", label: "", kind: "wan" }); changed(); rerender(); } }, "+ Add interface"),
      suggestions.length ? h("span", { class: "hint" }, "Detected:") : null,
      suggestions.map(d => h("button", { type: "button", class: "chip", title: `Add ${d.name}`,
        onclick: () => { ifaceRows.push({ name: d.name, label: d.label === d.name ? "" : d.label, kind: d.kind || "other" }); changed(); rerender(); } }, `+ ${d.name}`)))));

  // --- device names
  out.push(section("hosts", "Device names", "Fixed names by IP. Reverse DNS fills in the rest; with storage on you can also rename devices from their detail panel.",
    h("table", { class: "tbl edit" },
      h("thead", {}, h("tr", {}, h("th", {}, "IP address"), h("th", {}, "Name"), h("th", {}))),
      h("tbody", {}, hostRows.length ? hostRows.map((r, i) => h("tr", {},
        h("td", {}, h("input", { value: r.ip, placeholder: "192.168.1.10", disabled: readonly, oninput: e => { r.ip = e.target.value; changed(); } })),
        h("td", { class: "grow" }, h("input", { class: "wide", value: r.name, placeholder: "Gaming PC", disabled: readonly, oninput: e => { r.name = e.target.value; changed(); } })),
        h("td", {}, readonly ? null : h("button", { class: "ghost sm", type: "button", title: "Remove", onclick: () => { hostRows.splice(i, 1); changed(); rerender(); } }, "✕")),
      )) : h("tr", { class: "empty-row" }, h("td", { colspan: 3 }, "No fixed names")))),
    readonly ? null : h("div", { class: "row-actions" },
      h("button", { type: "button", onclick: () => { hostRows.push({ ip: "", name: "" }); changed(); rerender(); } }, "+ Add name"))));

  // --- dashboard & security
  out.push(section("security", "Dashboard & security", null,
    grid(
      field("Address", text("web.listen", { placeholder: ":8080" }), "Changing the port moves this page; you'll be redirected."),
      field("Dashboard", toggle("web.ui", cfg.web.ui ? "On" : "Off (API only)"), cfg.web.ui ? null : "Turning the dashboard off also hides this Settings page. To get it back, set web.ui: true in the config file."),
      field("Username", text("web.username", { placeholder: "leave empty for no login" })),
      field("Password", secret("web.password", S.password_set ? "unchanged" : "")),
      field("Allowed host names", listText("web.allowed_hosts", "traffic.example.com"),
        "Only needed if you open the dashboard by a public domain name (e.g. behind a reverse proxy). IPs and local names (.lan, .local, .home.arpa) always work."),
    )));

  // --- API
  const api = cfg.api.enabled;
  out.push(section("api", "API", "Read-only JSON at /api/v1 and Prometheus /metrics, for Home Assistant, Grafana or your own scripts.",
    grid(
      field("API", toggle("api.enabled", api ? "On" : "Off")),
      api && field("Tokens", h("div", { class: "tokens" },
        cfg.api.tokens.length ? null : h("div", { class: "hint warn-text" }, "No token: anyone on your network can read the API."),
        cfg.api.tokens.map((t, i) => {
          const inp = h("input", { type: "password", value: t, readonly: true, class: "mono" });
          return h("div", { class: "with-btn" }, inp,
            h("button", { class: "ghost sm", type: "button", title: "Show / hide", onclick: () => { inp.type = inp.type === "password" ? "text" : "password"; } }, "👁"),
            h("button", { class: "ghost sm", type: "button", title: "Copy", onclick: () => navigator.clipboard?.writeText(t) }, "⧉"),
            readonly ? null : h("button", { class: "ghost sm", type: "button", title: "Remove", onclick: () => { cfg.api.tokens.splice(i, 1); changed(); rerender(); } }, "✕"));
        }),
        readonly ? null : h("button", { type: "button", onclick: () => {
          const b = new Uint8Array(24); crypto.getRandomValues(b);
          cfg.api.tokens.push([...b].map(x => x.toString(16).padStart(2, "0")).join("")); changed(); rerender();
        } }, "+ Generate token"))),
      api && field("Other websites", toggle("api.cors", cfg.api.cors ? "Allowed (CORS)" : "Not allowed")),
      api && field("Try it", h("code", { class: "mono hint" }, `curl ${cfg.api.tokens.length ? '-H "Authorization: Bearer <token>" ' : ""}http://${hostPort(cfg.web.listen, 8080)}/api/v1/summary`)),
    )));

  // --- MQTT
  const mq = cfg.mqtt.enabled;
  out.push(section("mqtt", "Home Assistant (MQTT)", "Turn on and Traffic Monitor appears in Home Assistant with sensors automatically, through MQTT discovery. Needs an MQTT broker, e.g. the Mosquitto add-on.",
    grid(
      field("MQTT", toggle("mqtt.enabled", mq ? "On" : "Off")),
      mq && field("Broker", text("mqtt.broker", { wide: true, placeholder: "tcp://homeassistant.local:1883" }), "tcp://host:1883, or ssl://host:8883 for TLS"),
      mq && field("Username", text("mqtt.username")),
      mq && field("Password", secret("mqtt.password")),
      mq && field("Devices in HA", select("mqtt.devices", [["named", "Named devices only"], ["all", "Every device"], ["none", "None (just Traffic Monitor, interfaces, networks)"]]),
        "Named = listed under Device names above (or renamed in the dashboard)."),
      mq && field("Also include", listText("mqtt.device_list", "192.168.1.20, Office NAS"), "extra IPs or names, comma separated"),
      mq && field("Interfaces", toggle("mqtt.interfaces", cfg.mqtt.interfaces ? "One HA device per firewall interface" : "Off")),
      mq && field("Networks", toggle("mqtt.networks", cfg.mqtt.networks ? "One HA device per network" : "Off")),
      mq && field("Update every", text("mqtt.interval", { placeholder: "10s" })),
      mq && field("Topic prefix", text("mqtt.topic_prefix")),
      mq && field("Discovery prefix", text("mqtt.discovery_prefix"), "Home Assistant's default is homeassistant"),
      mq && field("Client ID", text("mqtt.client_id")),
      mq && field("TLS", toggle("mqtt.tls_insecure", cfg.mqtt.tls_insecure ? "Don't verify certificate" : "Verify certificate")),
    )));

  out.push(h("p", { class: "hint foot" }, `Saved to ${S.config_path}. Saving restarts collection for a moment; live rates and today's totals are kept.`));

  // save bar
  out.push(h("div", { class: "savebar", id: "savebar", hidden: true },
    h("span", { id: "save-msg" }, "Unsaved changes"),
    h("span", { class: "spacer" }),
    h("button", { type: "button", class: "ghost", onclick: () => loadSettings(root) }, "Discard"),
    h("button", { type: "button", class: "primary", id: "save-btn", onclick: save }, "Save & apply")));

  root.replaceChildren(...out.filter(Boolean));
  updateBar();
}

function updateBar() {
  const bar = document.getElementById("savebar");
  if (!bar || readonly) return;
  const d = dirty();
  bar.hidden = !d && !bar.classList.contains("error");
  if (d && !saving && !bar.classList.contains("error")) document.getElementById("save-msg").textContent = "Unsaved changes";
}

function showMsg(msg, error) {
  const bar = document.getElementById("savebar");
  bar.hidden = false;
  bar.classList.toggle("error", !!error);
  document.getElementById("save-msg").textContent = msg;
}

async function save() {
  if (saving) return;
  serialize();
  if (!cfg.web.ui && !confirm("Turn the dashboard off? This page will stop working; only the API (if on) keeps running.")) return;
  saving = true;
  document.getElementById("save-btn").disabled = true;
  document.getElementById("savebar").classList.remove("error");
  showMsg("Saving…");
  try {
    const r = await fetch("api/settings", { method: "PUT", headers: { "Content-Type": "application/json", "X-Traffic-Monitor": "1" }, body: JSON.stringify({ config: cfg }) });
    const body = await r.text();
    if (!r.ok) throw new Error(body.trim() || r.statusText);
    const res = JSON.parse(body);
    showMsg("Saved. Applying…");
    const port = (res.listen || "").split(":").pop();
    if (!res.ui) { showMsg("Saved. The dashboard is now off."); return; }
    if (port && port !== (location.port || (location.protocol === "https:" ? "443" : "80"))) {
      setTimeout(() => { location.href = `${location.protocol}//${location.hostname}:${port}/#settings`; }, 2500);
      return;
    }
    await waitForRestart();
    location.reload();
  } catch (e) {
    showMsg(String(e.message || e), true);
  } finally {
    saving = false;
    const b = document.getElementById("save-btn");
    if (b) b.disabled = false;
  }
}

async function waitForRestart() {
  await new Promise(r => setTimeout(r, 1200));
  for (let i = 0; i < 40; i++) {
    try { const r = await fetch("api/status", { cache: "no-store" }); if (r.ok || r.status === 401) return; } catch {}
    await new Promise(r => setTimeout(r, 500));
  }
}
