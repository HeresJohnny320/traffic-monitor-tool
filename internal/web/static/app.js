import { chart } from "./chart.js";
import { loadSettings } from "./settings.js";
import { apiURL, changeHeaders } from "./embed.js";

// ------------------------------------------------------------------ helpers
const $ = s => document.querySelector(s);
const $$ = s => [...document.querySelectorAll(s)];

function fmtBits(bps) {
  const u = ["bps", "Kbps", "Mbps", "Gbps", "Tbps"];
  let i = 0;
  while (bps >= 1000 && i < u.length - 1) { bps /= 1000; i++; }
  return (bps >= 100 || i === 0 ? bps.toFixed(0) : bps.toFixed(bps >= 10 ? 1 : 2)) + " " + u[i];
}
function fmtBytes(b) {
  const u = ["B", "KB", "MB", "GB", "TB", "PB"];
  let i = 0;
  while (b >= 1000 && i < u.length - 1) { b /= 1000; i++; }
  return (b >= 100 || i === 0 ? b.toFixed(0) : b.toFixed(b >= 10 ? 1 : 2)) + " " + u[i];
}
const pad = n => String(n).padStart(2, "0");
const D = t => new Date(t * 1000);
const MON = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const DAY = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const hm = d => `${pad(d.getHours())}:${pad(d.getMinutes())}`;
function fmtWhen(t) {
  if (!t) return "–";
  const d = D(t), now = new Date();
  if (d.toDateString() === now.toDateString()) return "today " + hm(d);
  return `${MON[d.getMonth()]} ${d.getDate()}${d.getFullYear() !== now.getFullYear() ? " " + d.getFullYear() : ""} ${hm(d)}`;
}
function ago(t) {
  const s = Math.max(0, Date.now() / 1000 - t);
  if (s < 90) return "just now";
  if (s < 3600) return Math.round(s / 60) + " min ago";
  if (s < 86400) return Math.round(s / 3600) + " h ago";
  return Math.round(s / 86400) + " days ago";
}

const store = {
  get(k, d) { try { const v = localStorage.getItem("tm." + k); return v == null ? d : JSON.parse(v); } catch { return d; } },
  set(k, v) { try { localStorage.setItem("tm." + k, JSON.stringify(v)); } catch {} },
};

async function api(path, params = {}) {
  const q = new URLSearchParams(Object.entries(params).filter(([, v]) => v !== "" && v != null));
  const r = await fetch(apiURL(`${path}${q.size ? "?" + q : ""}`));
  if (!r.ok) throw new Error(`${path}: ${r.status} ${await r.text()}`);
  return r.json();
}

function h(tag, props = {}, ...kids) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === "class") n.className = v;
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else if (k === "style") n.style.cssText = v;
    else n.setAttribute(k, v);
  }
  for (const c of kids.flat()) if (c != null) n.append(c instanceof Node ? c : String(c));
  return n;
}

const PROTO = { 1: "ICMP", 6: "TCP", 17: "UDP", 47: "GRE", 50: "ESP", 58: "ICMPv6", 132: "SCTP" };
const SERVICE = {
  20: "FTP", 21: "FTP", 22: "SSH", 25: "SMTP", 53: "DNS", 80: "HTTP", 110: "POP3", 123: "NTP", 143: "IMAP",
  443: "HTTPS", 465: "SMTPS", 500: "IKE", 554: "RTSP", 587: "SMTP", 853: "DNS-over-TLS", 993: "IMAPS", 995: "POP3S",
  1194: "OpenVPN", 1935: "RTMP", 3074: "Xbox Live", 3478: "STUN/TURN", 3479: "STUN/TURN", 4500: "IPsec NAT-T",
  5222: "XMPP", 5223: "Apple Push", 5228: "Google Play", 8080: "HTTP alt", 8443: "HTTPS alt", 19302: "Google STUN",
  27015: "Steam", 27036: "Steam", 51820: "WireGuard",
};
function service(p) {
  const pr = PROTO[p.proto] || "proto " + p.proto;
  if (!p.port) return pr;
  if (p.proto === 17 && p.port === 443) return "QUIC (443/udp)";
  return (SERVICE[p.port] ? SERVICE[p.port] + " · " : "") + `${p.port}/${pr.toLowerCase()}`;
}

function shareBar(rx, tx, max) {
  const w = v => (max > 0 ? (v / max) * 100 : 0).toFixed(2) + "%";
  return h("div", { class: "share" }, h("i", { class: "dl", style: `width:${w(rx)}` }), h("i", { class: "ul", style: `width:${w(tx)}` }));
}
const badge = t => (t ? h("span", { class: "badge" }, t) : "");
function hostCell(r) {
  const name = r.name || r.ip;
  return h("div", {}, h("div", { class: "name ellip", title: name }, name), r.name ? h("div", { class: "sub" }, r.ip) : null);
}

// ------------------------------------------------------------------ sortable tables
/**
 * table(el, cols, rows, opts)  cols: [{k, label, num, val: row => sortable, cell: row => Node|string, cls}]
 */
const sortState = store.get("sort", {});
function table(el, cols, rows, { onClick, empty = "Nothing yet", defaultSort, limit } = {}) {
  const id = el.id;
  let st = sortState[id] || defaultSort || null;
  const sorted = rows.slice();
  if (st) {
    const c = cols.find(c => c.k === st.k);
    if (c) {
      const v = c.val || (r => r[c.k]);
      sorted.sort((a, b) => {
        const x = v(a), y = v(b);
        const r = typeof x === "string" ? x.localeCompare(y, undefined, { numeric: true }) : x - y;
        return st.asc ? r : -r;
      });
    }
  }
  const head = h("tr", {}, cols.map(c => {
    const th = h("th", { class: (c.num ? "num " : "") + (c.cls || "") }, c.label);
    if (el.classList.contains("sortable") && c.k) {
      th.dataset.k = c.k;
      if (st && st.k === c.k) th.classList.add("sorted", ...(st.asc ? ["asc"] : []));
      th.addEventListener("click", () => {
        sortState[id] = st && st.k === c.k ? { k: c.k, asc: !st.asc } : { k: c.k, asc: !c.num };
        store.set("sort", sortState);
        table(el, cols, rows, { onClick, empty, defaultSort, limit });
      });
    }
    return th;
  }));
  const shown = limit ? sorted.slice(0, limit) : sorted;
  const body = h("tbody", {}, shown.length ? shown.map((r, i) => {
    const tr = h("tr", {}, cols.map(c => h("td", { class: (c.num ? "num " : "") + (c.cls || "") }, c.cell ? c.cell(r, i) : r[c.k])));
    if (onClick) tr.addEventListener("click", () => onClick(r, tr));
    return tr;
  }) : h("tr", { class: "empty-row" }, h("td", { colspan: cols.length }, empty)));
  el.replaceChildren(h("thead", {}, head), body);
}

// ------------------------------------------------------------------ state
const state = {
  tab: ["live", "history", "ifaces", "devices", "settings"].includes(location.hash.slice(1).split("/")[0]) ? location.hash.slice(1).split("/")[0] : store.get("tab", "live"),
  range: store.get("range", "today"),
  custom: store.get("custom", null),
  step: store.get("step", "auto"),
  network: store.get("network", ""),
  ifaceSel: null, // null = default to the WAN interface
  live: [], // [{t, down, up}]
};

function rangeBounds() {
  const now = new Date();
  const sod = d => new Date(d.getFullYear(), d.getMonth(), d.getDate());
  const s = x => Math.floor(x.getTime() / 1000);
  const end = s(now) + 60;
  switch (state.range) {
    case "today": return [s(sod(now)), end, "Today"];
    case "yesterday": { const t = sod(now); return [s(new Date(t.getFullYear(), t.getMonth(), t.getDate() - 1)), s(t), "Yesterday"]; }
    case "24h": return [end - 86400 - 60, end, "Last 24 hours"];
    case "7d": return [s(new Date(now.getFullYear(), now.getMonth(), now.getDate() - 6)), end, "Last 7 days"];
    case "30d": return [s(new Date(now.getFullYear(), now.getMonth(), now.getDate() - 29)), end, "Last 30 days"];
    case "month": return [s(new Date(now.getFullYear(), now.getMonth(), 1)), end, `${MON[now.getMonth()]} ${now.getFullYear()}`];
    case "lastmonth": {
      const a = new Date(now.getFullYear(), now.getMonth() - 1, 1), b = new Date(now.getFullYear(), now.getMonth(), 1);
      return [s(a), s(b), `${MON[a.getMonth()]} ${a.getFullYear()}`];
    }
    case "year": return [s(new Date(now.getFullYear(), 0, 1)), end, String(now.getFullYear())];
    case "custom": {
      const c = state.custom;
      if (c) {
        const [fy, fm, fd] = c.from.split("-").map(Number), [ty, tm, td] = c.to.split("-").map(Number);
        return [s(new Date(fy, fm - 1, fd)), s(new Date(ty, tm - 1, td + 1)), `${c.from} → ${c.to}`];
      }
    }
  }
  return [s(sod(now)), end, "Today"];
}

function xFormatter(step, from, to) {
  const multiDay = to - from > 86400 + 120;
  switch (step) {
    case "minute": case "5min": case "15min": case "hour":
      return [
        t => { const d = D(t); return multiDay ? `${DAY[d.getDay()]} ${hm(d)}` : hm(d); },
        t => { const d = D(t); return `${DAY[d.getDay()]} ${MON[d.getMonth()]} ${d.getDate()}, ${hm(d)}`; },
      ];
    case "day":
      return [t => { const d = D(t); return `${MON[d.getMonth()]} ${d.getDate()}`; },
              t => { const d = D(t); return `${DAY[d.getDay()]} ${MON[d.getMonth()]} ${d.getDate()}, ${d.getFullYear()}`; }];
    case "week":
      return [t => { const d = D(t); return `${MON[d.getMonth()]} ${d.getDate()}`; },
              t => { const d = D(t); return `Week of ${MON[d.getMonth()]} ${d.getDate()}, ${d.getFullYear()}`; }];
    default:
      return [t => { const d = D(t); return `${MON[d.getMonth()]} ${String(d.getFullYear()).slice(2)}`; },
              t => { const d = D(t); return `${MON[d.getMonth()]} ${d.getFullYear()}`; }];
  }
}
const STEP_WORD = { minute: "minute", "5min": "5 minutes", "15min": "15 minutes", hour: "hour", day: "day", week: "week", month: "month" };

// ------------------------------------------------------------------ chrome
function setTheme(t) {
  if (t) document.documentElement.dataset.theme = t; else delete document.documentElement.dataset.theme;
}
if (store.get("theme", null)) setTheme(store.get("theme", null));
$("#theme").addEventListener("click", () => {
  const dark = document.documentElement.dataset.theme
    ? document.documentElement.dataset.theme === "dark"
    : matchMedia("(prefers-color-scheme: dark)").matches;
  const t = dark ? "light" : "dark";
  setTheme(t); store.set("theme", t); // charts use CSS variables, no redraw needed
});

function showTab(tab) {
  state.tab = tab; store.set("tab", tab);
  history.replaceState(null, "", "#" + tab);
  $$(".tabs button").forEach(b => b.classList.toggle("on", b.dataset.tab === tab));
  $$(".view").forEach(v => (v.hidden = v.dataset.view !== tab));
  // the date range doesn't apply to the live view; no filters on settings
  $("#filters").hidden = tab === "settings";
  $("#ranges").hidden = $("label:has(#step)").hidden = tab === "live";
  $("#custom").hidden = tab === "live" || state.range !== "custom";
  refresh();
}
$$(".tabs button").forEach(b => b.addEventListener("click", () => showTab(b.dataset.tab)));

$$("#ranges button").forEach(b => {
  b.classList.toggle("on", b.dataset.r === state.range);
  b.addEventListener("click", () => {
    state.range = b.dataset.r; store.set("range", state.range);
    $$("#ranges button").forEach(x => x.classList.toggle("on", x === b));
    $("#custom").hidden = state.range !== "custom";
    if (state.range !== "custom" || state.custom) refresh();
  });
});
{
  const today = new Date(), iso = d => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  $("#cfrom").value = state.custom?.from || iso(new Date(today.getFullYear(), today.getMonth(), today.getDate() - 7));
  $("#cto").value = state.custom?.to || iso(today);
  $("#capply").addEventListener("click", () => {
    if (!$("#cfrom").value || !$("#cto").value || $("#cfrom").value > $("#cto").value) return;
    state.custom = { from: $("#cfrom").value, to: $("#cto").value }; store.set("custom", state.custom);
    refresh();
  });
}
$("#step").value = state.step;
$("#step").addEventListener("change", e => { state.step = e.target.value; store.set("step", state.step); refresh(); });
$("#network").addEventListener("change", e => { state.network = e.target.value; store.set("network", state.network); refresh(); });

async function loadStatus() {
  try {
    const s = await api("status");
    const sel = $("#network");
    if (sel.options.length === 1) {
      for (const n of s.networks || []) sel.append(h("option", { value: n.name }, `${n.name} (${n.kind})`));
      sel.value = state.network;
      if (sel.value !== state.network) { state.network = ""; }
    }
    const st = $("#status");
    st.className = "status " + (s.collector_alive ? "ok" : "down");
    const m = s.meta || {};
    st.title = s.collector_alive
      ? `Collector running · exporters: ${m.exporters || "none yet"} · flows: ${Number(m.flows_total || 0).toLocaleString()}`
      : "Collector not running (no heartbeat in the last 2 minutes)";
    state.liveWindow = Number(m.live_window || 60);
    state.history = s.history !== false;
    renderWelcome(s);
    $("#mode").hidden = state.history;
    $$(".tabs button").forEach(b => (b.hidden = !state.history && !["live", "settings"].includes(b.dataset.tab)));
    if (!state.history && !["live", "settings"].includes(state.tab)) state.tab = "live";
  } catch (e) { $("#status").className = "status down"; }
}

// ------------------------------------------------------------------ first run
function renderWelcome(s) {
  const box = $("#welcome");
  if (!s.first_run || store.get("welcomeDismissed", false)) { box.hidden = true; return; }
  const listen = s.netflow_listen || ":2055";
  const port = listen.split(":").pop();
  // listening on loopback = running on the firewall itself
  const dest = /^(127\.|localhost|\[::1\])/.test(listen) ? `127.0.0.1:${port} (this firewall)` : `${location.hostname}:${port}`;
  box.replaceChildren(
    h("div", { class: "welcome-t" }, "Welcome to Traffic Monitor"),
    h("div", {}, "Everything is off except this live view, and nothing is stored. To see your devices, send NetFlow from pfSense (softflowd) or OPNsense (Reporting → NetFlow) to ",
      h("b", {}, dest), ", exporting from LAN/VLAN/VPN interfaces (not WAN)."),
    h("div", {}, "Then open Settings to add your networks, SNMP for WAN/VLAN/VPN totals, history, the API or Home Assistant, whenever you want them."),
    h("div", { class: "welcome-a" },
      h("button", { class: "primary", onclick: () => showTab("settings") }, "Open Settings"),
      h("button", { class: "ghost", onclick: () => { store.set("welcomeDismissed", true); box.hidden = true; } }, "Dismiss")));
  box.hidden = false;
}

// ------------------------------------------------------------------ live
function wanRates(ifaces, hosts) {
  const wan = ifaces.filter(i => i.kind === "wan");
  if (wan.length) {
    return { down: wan.reduce((s, i) => s + i.in_bps, 0), up: wan.reduce((s, i) => s + i.out_bps, 0), src: wan.map(i => i.label).join(" + ") + " · SNMP" };
  }
  return { down: hosts.reduce((s, x) => s + x.rx_bps, 0), up: hosts.reduce((s, x) => s + x.tx_bps, 0), src: "sum of devices · NetFlow" };
}

let liveSeeded = false;
async function seedLive() {
  if (liveSeeded || state.history === false) return;
  liveSeeded = true;
  try {
    const to = Math.floor(Date.now() / 1000), from = to - 30 * 60;
    const live = await api("live");
    const wan = live.ifaces.find(i => i.kind === "wan");
    const res = wan
      ? await api("ifaces/series", { from, to, step: "minute", name: wan.name })
      : await api("series", { from, to, step: "minute" });
    if (res.step !== "minute") return;
    const pts = res.points.slice(0, -1).map(p => ({ t: p.t + 30, down: p.rx * 8 / 60, up: p.tx * 8 / 60 }));
    state.live = pts.concat(state.live);
  } catch {}
}

async function loadLive() {
  await seedLive();
  const d = await api("live");
  let hosts = d.hosts;
  if (state.network) hosts = hosts.filter(x => x.network === state.network);
  const wan = wanRates(d.ifaces, d.hosts);
  if (!state.live.length || state.live[state.live.length - 1].t < d.t) state.live.push({ t: d.t, down: wan.down, up: wan.up });
  const cut = d.t - 30 * 60;
  state.live = state.live.filter(p => p.t >= cut);
  if (state.tab !== "live") return;

  $("#l-down").textContent = fmtBits(wan.down);
  $("#l-up").textContent = fmtBits(wan.up);
  $("#l-down-s").textContent = $("#l-up-s").textContent = wan.src;
  const active = hosts.filter(x => x.rx_bps + x.tx_bps + x.lan_rx_bps + x.lan_tx_bps > 0);
  $("#l-hosts").textContent = active.length;
  const top = hosts[0];
  $("#l-top").textContent = top ? top.name || top.ip : "–";
  $("#l-top-s").textContent = top ? `↓ ${fmtBits(top.rx_bps)}  ↑ ${fmtBits(top.tx_bps)}` : "";
  $("#live-hint").textContent = `averaged over ${state.liveWindow || 60}s of NetFlow`;

  chart($("#live-chart"), {
    type: "line", area: true, height: 240,
    x: state.live.map(p => p.t),
    series: [{ name: "Download", color: "--dl", values: state.live.map(p => p.down) },
             { name: "Upload", color: "--ul", values: state.live.map(p => p.up) }],
    fmtY: fmtBits,
    fmtX: t => { const x = D(t), span = state.live.length ? state.live.at(-1).t - state.live[0].t : 0; return span < 600 ? `${hm(x)}:${pad(x.getSeconds())}` : hm(x); },
    fmtTip: t => { const x = D(t); return `${hm(x)}:${pad(x.getSeconds())}`; },
  });

  const maxIf = Math.max(1, ...d.ifaces.map(i => i.in_bps + i.out_bps));
  table($("#live-ifaces"), [
    { k: "label", label: "Interface", cell: r => h("div", {}, h("div", { class: "name" }, r.label), r.label !== r.name ? h("div", { class: "sub" }, r.name) : null) },
    { k: "kind", label: "Type", cell: r => badge(r.kind), cls: "hide-sm" },
    { k: "in_bps", label: "In", num: true, cell: r => fmtBits(r.in_bps) },
    { k: "out_bps", label: "Out", num: true, cell: r => fmtBits(r.out_bps) },
    { label: "", cell: r => shareBar(r.in_bps, r.out_bps, maxIf), cls: "hide-sm" },
  ], d.ifaces, { empty: "No interface data yet. Turn on SNMP in Settings to see WAN, VLAN and VPN interfaces." });

  const maxH = Math.max(1, ...hosts.map(x => x.rx_bps + x.tx_bps));
  table($("#live-hosts"), [
    { k: "name", label: "Device", val: r => r.name || r.ip, cell: hostCell },
    { k: "network", label: "Network", cell: r => badge(r.network), cls: "hide-sm" },
    { k: "rx_bps", label: "Download", num: true, cell: r => fmtBits(r.rx_bps) },
    { k: "tx_bps", label: "Upload", num: true, cell: r => fmtBits(r.tx_bps) },
    { k: "lan", label: "Local", num: true, val: r => r.lan_rx_bps + r.lan_tx_bps, cell: r => fmtBits(r.lan_rx_bps + r.lan_tx_bps), cls: "hide-sm" },
    { label: "", cell: r => shareBar(r.rx_bps, r.tx_bps, maxH), cls: "hide-sm" },
  ], hosts, { onClick: state.history ? r => openHost(r.ip) : null, empty: "No flows received in the live window", defaultSort: { k: "rx_bps", asc: false }, limit: 100 });
  $("#live-hosts").classList.toggle("clickable", !!state.history);
}

// ------------------------------------------------------------------ history
function busy(ids, on) { ids.forEach(id => $(id).classList.toggle("loading", on)); }

async function loadHistory() {
  const [from, to, label] = rangeBounds();
  const params = { from, to, network: state.network };
  busy(["#hist-chart"], true);
  const [top, ser, peers] = await Promise.all([
    api("top", params), api("series", { ...params, step: state.step }), api("peers", { ...params, limit: 25 }),
  ]);
  busy(["#hist-chart"], false);

  const sum = k => top.reduce((s, r) => s + r[k], 0);
  $("#h-down").textContent = fmtBytes(sum("rx"));
  $("#h-up").textContent = fmtBytes(sum("tx"));
  $("#h-lan").textContent = fmtBytes(sum("lan_rx") + sum("lan_tx"));
  $("#h-n").textContent = top.length;
  $("#h-n-s").textContent = label;
  $("#h-title").textContent = `Internet usage per ${STEP_WORD[ser.step]} · ${label}`;

  const [fx, ft] = xFormatter(ser.step, from, to);
  chart($("#hist-chart"), {
    type: ser.points.length > 120 ? "line" : "bar", stacked: true, area: true, height: 280,
    x: ser.points.map(p => p.t),
    series: [{ name: "Download", color: "--dl", values: ser.points.map(p => p.rx) },
             { name: "Upload", color: "--ul", values: ser.points.map(p => p.tx) }],
    fmtY: fmtBytes, fmtX: fx, fmtTip: ft,
  });

  renderTopHosts(top);
  $("#h-search").oninput = () => renderTopHosts(top);

  table($("#hist-peers"), [
    { label: "Remote", cell: r => h("div", {}, h("div", { class: "name ellip", title: r.host || r.remote }, r.host || r.remote), r.host ? h("div", { class: "sub" }, r.remote) : null) },
    { label: "Service", cell: r => h("span", { class: "sub" }, service(r)), cls: "hide-sm" },
    { label: "Down", num: true, cell: r => fmtBytes(r.rx) },
    { label: "Up", num: true, cell: r => fmtBytes(r.tx) },
  ], peers, { empty: "No destination data in this range" });
}

function renderTopHosts(top) {
  const q = $("#h-search").value.trim().toLowerCase();
  const rows = q ? top.filter(r => (r.name + " " + r.ip + " " + r.network).toLowerCase().includes(q)) : top;
  const total = top.reduce((s, r) => s + r.rx + r.tx, 0);
  const max = Math.max(1, ...top.map(r => r.rx + r.tx));
  table($("#hist-hosts"), [
    { label: "#", cell: (r, i) => h("span", { class: "sub" }, i + 1) },
    { k: "name", label: "Device", val: r => r.name || r.ip, cell: hostCell },
    { k: "network", label: "Network", cell: r => badge(r.network), cls: "hide-sm" },
    { k: "rx", label: "Download", num: true, cell: r => fmtBytes(r.rx) },
    { k: "tx", label: "Upload", num: true, cell: r => fmtBytes(r.tx) },
    { k: "total", label: "Total", num: true, val: r => r.rx + r.tx, cell: r => h("b", {}, fmtBytes(r.rx + r.tx)) },
    { k: "share", label: "Share", val: r => r.rx + r.tx, cls: "hide-sm",
      cell: r => h("div", { title: total ? ((r.rx + r.tx) / total * 100).toFixed(1) + "% of internet traffic" : "" }, shareBar(r.rx, r.tx, max)) },
    { k: "lan", label: "Local", num: true, val: r => r.lan_rx + r.lan_tx, cell: r => h("span", { class: "sub" }, fmtBytes(r.lan_rx + r.lan_tx)), cls: "hide-sm" },
  ], rows, { onClick: r => openHost(r.ip), defaultSort: { k: "total", asc: false }, empty: "No traffic recorded in this range" });
}

// ------------------------------------------------------------------ interfaces
async function loadIfaces() {
  const [from, to, label] = rangeBounds();
  busy(["#if-chart"], true);
  const tot = await api("ifaces", { from, to });
  if (state.ifaceSel == null) state.ifaceSel = (tot.find(t => t.kind === "wan") || { name: "" }).name;
  const [ser] = await Promise.all([
    api("ifaces/series", { from, to, step: state.step, name: state.ifaceSel }),
  ]);
  busy(["#if-chart"], false);
  const sel = tot.find(t => t.name === state.ifaceSel);
  $("#if-title").textContent = `${sel ? sel.label + " (" + sel.name + ")" : "All interfaces combined"} · per ${STEP_WORD[ser.step]} · ${label}`;
  const [fx, ft] = xFormatter(ser.step, from, to);
  chart($("#if-chart"), {
    type: ser.points.length > 120 ? "line" : "bar", stacked: true, area: true, height: 280,
    x: ser.points.map(p => p.t),
    series: [{ name: "In", color: "--dl", values: ser.points.map(p => p.rx) },
             { name: "Out", color: "--ul", values: ser.points.map(p => p.tx) }],
    fmtY: fmtBytes, fmtX: fx, fmtTip: ft,
  });
  const max = Math.max(1, ...tot.map(r => r.in + r.out));
  const rows = [{ name: "", label: "All interfaces", kind: "", in: tot.reduce((s, r) => s + r.in, 0), out: tot.reduce((s, r) => s + r.out, 0), all: true }, ...tot];
  table($("#if-table"), [
    { k: "label", label: "Interface", cell: r => h("div", {}, h("div", { class: "name" }, r.label), r.name && r.label !== r.name ? h("div", { class: "sub" }, r.name) : null) },
    { k: "kind", label: "Type", cell: r => badge(r.kind) },
    { k: "in", label: "In", num: true, cell: r => fmtBytes(r.in) },
    { k: "out", label: "Out", num: true, cell: r => fmtBytes(r.out) },
    { k: "total", label: "Total", num: true, val: r => r.in + r.out, cell: r => h("b", {}, fmtBytes(r.in + r.out)) },
    { label: "", cls: "hide-sm", cell: r => r.all ? "" : shareBar(r.in, r.out, max) },
    { label: "", cell: r => r.all ? "" : h("button", { class: "ghost", title: "Rename", onclick: async e => {
        e.stopPropagation();
        const v = prompt(`Label for ${r.name}`, r.label);
        if (v == null) return;
        await fetch(apiURL("ifaces/rename"), { method: "POST", headers: changeHeaders, body: JSON.stringify({ name: r.name, label: v.trim() }) });
        refresh();
      } }, "✎") },
  ], rows, { onClick: r => { state.ifaceSel = r.name; loadIfaces(); }, empty: "No interface data – SNMP is disabled or hasn't polled yet" });
  const idx = sel ? rows.indexOf(sel) : 0;
  if (!sortState["if-table"]) $("#if-table").tBodies[0].rows[idx]?.classList.add("sel");
}

// ------------------------------------------------------------------ devices
async function loadDevices() {
  const [from, to, label] = rangeBounds();
  const [hosts, top] = await Promise.all([api("hosts"), api("top", { from, to, network: state.network })]);
  const usage = Object.fromEntries(top.map(t => [t.ip, t]));
  let rows = hosts.filter(x => !state.network || x.network === state.network).map(x => ({
    ...x, name: x.custom_name || x.name, rx: usage[x.ip]?.rx || 0, tx: usage[x.ip]?.tx || 0,
  }));
  const render = () => {
    const q = $("#d-search").value.trim().toLowerCase();
    const shown = q ? rows.filter(r => (r.name + " " + r.ip + " " + r.network).toLowerCase().includes(q)) : rows;
    table($("#dev-table"), [
      { k: "name", label: "Device", val: r => r.name || r.ip, cell: hostCell },
      { k: "network", label: "Network", cell: r => badge(r.network) },
      { k: "rx", label: `Down (${label})`, num: true, cell: r => fmtBytes(r.rx) },
      { k: "tx", label: `Up (${label})`, num: true, cell: r => fmtBytes(r.tx) },
      { k: "last_seen", label: "Last seen", num: true, cell: r => h("span", { title: fmtWhen(r.last_seen) }, ago(r.last_seen)) },
      { k: "first_seen", label: "First seen", num: true, cls: "hide-sm", cell: r => h("span", { class: "sub" }, fmtWhen(r.first_seen)) },
    ], shown, { onClick: r => openHost(r.ip), defaultSort: { k: "last_seen", asc: false }, empty: "No devices seen yet" });
  };
  $("#d-search").oninput = render;
  render();
}

// ------------------------------------------------------------------ device drawer
let drawerIP = null;
async function openHost(ip) {
  drawerIP = ip;
  history.replaceState(null, "", `#${state.tab}/${encodeURIComponent(ip)}`);
  $("#drawer").hidden = $("#scrim").hidden = false;
  const [from, to, label] = rangeBounds();
  const span = state.tab === "live" ? [Math.floor(Date.now() / 1000) - 86400, Math.floor(Date.now() / 1000) + 60, "Last 24 hours"] : [from, to, label];
  const [hosts, top, ser, peers] = await Promise.all([
    api("hosts"), api("top", { from: span[0], to: span[1], ip }),
    api("series", { from: span[0], to: span[1], ip, step: state.tab === "live" ? "auto" : state.step }),
    api("peers", { from: span[0], to: span[1], ip, limit: 50 }),
  ]);
  if (drawerIP !== ip) return;
  const host = hosts.find(x => x.ip === ip) || { ip };
  const name = host.custom_name || host.name;
  $("#dr-name").textContent = name || ip;
  $("#dr-sub").textContent = [name ? ip : "", host.network, host.name && host.custom_name ? "DNS: " + host.name : "", span[2]].filter(Boolean).join(" · ");
  $("#dr-rename").value = host.custom_name || "";
  const t = top[0] || { rx: 0, tx: 0, lan_rx: 0, lan_tx: 0 };
  $("#dr-down").textContent = fmtBytes(t.rx);
  $("#dr-up").textContent = fmtBytes(t.tx);
  $("#dr-lan").textContent = fmtBytes(t.lan_rx + t.lan_tx);
  const [fx, ft] = xFormatter(ser.step, span[0], span[1]);
  chart($("#dr-chart"), {
    type: ser.points.length > 120 ? "line" : "bar", stacked: true, area: true, height: 220,
    x: ser.points.map(p => p.t),
    series: [{ name: "Download", color: "--dl", values: ser.points.map(p => p.rx) },
             { name: "Upload", color: "--ul", values: ser.points.map(p => p.tx) }],
    fmtY: fmtBytes, fmtX: fx, fmtTip: ft,
  });
  table($("#dr-peers"), [
    { label: "Remote", cell: r => h("div", {}, h("div", { class: "name ellip", title: r.host || r.remote }, r.host || r.remote), r.host ? h("div", { class: "sub" }, r.remote) : null) },
    { label: "Service", cell: r => h("span", { class: "sub" }, service(r)) },
    { label: "Down", num: true, cell: r => fmtBytes(r.rx) },
    { label: "Up", num: true, cell: r => fmtBytes(r.tx) },
  ], peers, { empty: "No internet destinations recorded" });
}
function closeDrawer() { drawerIP = null; history.replaceState(null, "", "#" + state.tab); $("#drawer").hidden = $("#scrim").hidden = true; }
$("#dr-close").addEventListener("click", closeDrawer);
$("#scrim").addEventListener("click", closeDrawer);
document.addEventListener("keydown", e => { if (e.key === "Escape") closeDrawer(); });
$("#dr-save").addEventListener("click", async () => {
  if (!drawerIP) return;
  await fetch(apiURL("hosts/rename"), { method: "POST", headers: changeHeaders, body: JSON.stringify({ ip: drawerIP, name: $("#dr-rename").value.trim() }) });
  const ip = drawerIP;
  refresh();
  openHost(ip);
});

// ------------------------------------------------------------------ loop
async function refresh() {
  try {
    if (state.tab === "live") await loadLive();
    else if (state.tab === "history") await loadHistory();
    else if (state.tab === "ifaces") await loadIfaces();
    else if (state.tab === "devices") await loadDevices();
    else if (state.tab === "settings") await loadSettings($('[data-view="settings"]'));
  } catch (e) { console.error(e); }
}

// deep link: #history/192.168.1.10 opens that device
const deepIP = location.hash.slice(1).split("/")[1];
await loadStatus();
showTab(state.tab);
if (deepIP && state.history) openHost(decodeURIComponent(deepIP));
setInterval(() => loadLive().catch(() => {}), 3000);       // keeps the live buffer filling on every tab
setInterval(loadStatus, 30000);
setInterval(() => { if (!["live", "settings"].includes(state.tab) && !document.hidden && drawerIP == null) refresh(); }, 60000);
