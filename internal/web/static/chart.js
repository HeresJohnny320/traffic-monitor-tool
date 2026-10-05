// Minimal dependency-free SVG charts: time-series line/area with crosshair
// tooltip, and stacked columns with per-bar tooltip.
const NS = "http://www.w3.org/2000/svg";

function el(tag, attrs = {}, parent) {
  const n = document.createElementNS(NS, tag);
  for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
  if (parent) parent.appendChild(n);
  return n;
}

function niceMax(v) {
  if (!(v > 0)) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]) if (m * p >= v) return m * p;
  return 10 * p;
}

/**
 * chart(container, {
 *   type: "line" | "bar",
 *   x: [unix seconds...],
 *   series: [{name, color (css var), values: [...]}],
 *   fmtY: v => string, fmtX: t => string (axis), fmtTip: t => string (tooltip title),
 *   stacked: bool (bar), height: px
 * })
 */
export function chart(container, opts) {
  container.classList.add("chart");
  const draw = () => render(container, opts);
  draw();
  if (!container._ro) {
    container._ro = new ResizeObserver(() => container._draw && container._draw());
    container._ro.observe(container);
  }
  container._draw = draw;
}

function render(box, o) {
  const W = Math.max(box.clientWidth, 280);
  const H = o.height || 260;
  const m = { t: 12, r: 12, b: 26, l: 64 };
  const iw = W - m.l - m.r, ih = H - m.t - m.b;
  box.replaceChildren();
  const svg = el("svg", { width: W, height: H, viewBox: `0 0 ${W} ${H}`, role: "img" }, box);
  const tip = document.createElement("div");
  tip.className = "tip";
  box.appendChild(tip);

  const n = o.x.length;
  if (!n || o.series.every(se => se.values.every(v => !v))) {
    const t = el("text", { x: W / 2, y: H / 2, "text-anchor": "middle", class: "empty" }, svg);
    t.textContent = "No data in this range yet";
    return;
  }
  let max = 0;
  for (let i = 0; i < n; i++) {
    if (o.type === "bar" && o.stacked) max = Math.max(max, o.series.reduce((s, se) => s + (se.values[i] || 0), 0));
    else for (const se of o.series) max = Math.max(max, se.values[i] || 0);
  }
  max = niceMax(max);
  const y = v => m.t + ih - (v / max) * ih;

  // grid + y axis
  const g = el("g", { class: "axis" }, svg);
  for (let k = 0; k <= 4; k++) {
    const v = (max * k) / 4, yy = Math.round(y(v)) + 0.5;
    el("line", { x1: m.l, x2: W - m.r, y1: yy, y2: yy, class: k ? "grid" : "base" }, g);
    const t = el("text", { x: m.l - 8, y: yy + 4, "text-anchor": "end" }, g);
    t.textContent = o.fmtY(v);
  }

  let xpos, slot = 0;
  if (o.type === "bar") {
    slot = iw / n;
    xpos = i => m.l + slot * i + slot / 2;
  } else {
    const t0 = o.x[0], t1 = o.x[n - 1] === t0 ? t0 + 1 : o.x[n - 1];
    xpos = i => m.l + ((o.x[i] - t0) / (t1 - t0)) * iw;
  }

  // x labels: ~ one per 90px
  const every = Math.max(1, Math.ceil(n / Math.max(2, Math.floor(iw / 90))));
  for (let i = 0; i < n; i += every) {
    const t = el("text", { x: xpos(i), y: H - 8, "text-anchor": "middle" }, g);
    t.textContent = o.fmtX(o.x[i]);
  }

  const marks = el("g", {}, svg);
  if (o.type === "bar") {
    const bw = Math.max(1, Math.min(slot - 2, 48));
    for (let i = 0; i < n; i++) {
      let base = 0;
      const col = el("g", { class: "col", tabindex: "-1" }, marks);
      o.series.forEach((se, si) => {
        const v = se.values[i] || 0;
        if (v <= 0) return;
        const y0 = y(base), y1 = y(base + v);
        // 2px surface gap between stacked segments
        const top = y1, h = Math.max(0, y0 - y1 - (base > 0 ? 2 : 0));
        const isTop = o.series.slice(si + 1).every(s2 => !(s2.values[i] > 0));
        const r = isTop ? Math.min(4, bw / 2, h) : 0;
        el("path", { d: roundTop(xpos(i) - bw / 2, top, bw, h, r), fill: `var(${se.color})` }, col);
        base += v;
      });
      const hit = el("rect", { x: m.l + slot * i, y: m.t, width: slot, height: ih, fill: "transparent" }, col);
      hit.addEventListener("pointerenter", () => showTip(i, xpos(i)));
      hit.addEventListener("pointerleave", hideTip);
    }
  } else {
    for (const se of o.series) {
      const pts = o.x.map((_, i) => [xpos(i), y(se.values[i] || 0)]);
      const d = pts.map((p, i) => (i ? "L" : "M") + p[0].toFixed(1) + "," + p[1].toFixed(1)).join("");
      if (o.area) {
        el("path", { d: d + `L${pts[n - 1][0]},${y(0)}L${pts[0][0]},${y(0)}Z`, fill: `var(${se.color})`, class: "area" }, marks);
      }
      el("path", { d, fill: "none", stroke: `var(${se.color})`, "stroke-width": 2, "stroke-linejoin": "round" }, marks);
    }
    const hair = el("line", { y1: m.t, y2: m.t + ih, class: "hair", visibility: "hidden" }, svg);
    const dots = o.series.map(se => el("circle", { r: 4, fill: `var(${se.color})`, class: "dot", visibility: "hidden" }, svg));
    const hit = el("rect", { x: m.l, y: m.t, width: iw, height: ih, fill: "transparent" }, svg);
    hit.addEventListener("pointermove", ev => {
      const r = svg.getBoundingClientRect();
      const px = ev.clientX - r.left;
      let best = 0, bd = Infinity;
      for (let i = 0; i < n; i++) { const d = Math.abs(xpos(i) - px); if (d < bd) { bd = d; best = i; } }
      const x = xpos(best);
      hair.setAttribute("x1", x); hair.setAttribute("x2", x); hair.setAttribute("visibility", "visible");
      o.series.forEach((se, si) => {
        dots[si].setAttribute("cx", x); dots[si].setAttribute("cy", y(se.values[best] || 0));
        dots[si].setAttribute("visibility", "visible");
      });
      showTip(best, x);
    });
    hit.addEventListener("pointerleave", () => {
      hair.setAttribute("visibility", "hidden");
      dots.forEach(d => d.setAttribute("visibility", "hidden"));
      hideTip();
    });
  }

  function showTip(i, x) {
    tip.replaceChildren();
    const h = document.createElement("div");
    h.className = "tip-h";
    h.textContent = (o.fmtTip || o.fmtX)(o.x[i]);
    tip.appendChild(h);
    for (const se of o.series) {
      const row = document.createElement("div");
      row.className = "tip-r";
      const key = document.createElement("span");
      key.className = "key"; key.style.background = `var(${se.color})`;
      const v = document.createElement("b"); v.textContent = o.fmtY(se.values[i] || 0);
      const l = document.createElement("span"); l.textContent = se.name;
      row.append(key, v, l);
      tip.appendChild(row);
    }
    if (o.series.length > 1 && o.type === "bar") {
      const row = document.createElement("div");
      row.className = "tip-r tot";
      const v = document.createElement("b"); v.textContent = o.fmtY(o.series.reduce((s, se) => s + (se.values[i] || 0), 0));
      const l = document.createElement("span"); l.textContent = "Total";
      row.append(document.createElement("span"), v, l);
      tip.appendChild(row);
    }
    tip.style.display = "block";
    const tw = tip.offsetWidth;
    tip.style.left = (x + 14 + tw > W ? x - 14 - tw : x + 14) + "px";
    tip.style.top = m.t + "px";
  }
  function hideTip() { tip.style.display = "none"; }
}

function roundTop(x, y, w, h, r) {
  if (h <= 0) return "";
  r = Math.max(0, Math.min(r, h));
  return `M${x},${y + h}V${y + r}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${y + h}Z`;
}
