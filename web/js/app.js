"use strict";

/* spark-mini-dash UI — vanilla JS, no build step.
   Cockpit-cluster layout: one instrument panel per node, side by side.
   Circular gauges (CPU with per-core turbine ring, GPU hero, memory),
   a thermometer, and power. Polls /api/state. */

const $ = (sel, el = document) => el.querySelector(sel);

const VB = 240, CX = 120, CY = 120;   // gauge viewBox
const TEMP_LO = 20, TEMP_HI = 110;    // thermometer scale

let panels = new Map(); // node index → DOM refs (config position is the only identity stable across polls)

/* ---------- formatting ---------- */

const fmtPct = v => v == null ? "—" : Math.round(v) + "%";
const fmtTemp = v => v == null ? "—" : Math.round(v) + "°C";
const fmtW = v => v == null ? "—" : Math.round(v) + "W";
const GB = kib => Math.round(kib * 1024 / 1e9); // decimal GB, matching Sync

function binClass(u) {
  if (u == null || u < 1) return "";
  if (u <= 25) return "h1";
  if (u <= 50) return "h2";
  if (u <= 75) return "h3";
  return "h4";
}

function sevClass(v, warn, serious, crit) {
  if (v == null) return "";
  if (v >= crit) return "sev-critical";
  if (v >= serious) return "sev-serious";
  if (v >= warn) return "sev-warn";
  return "";
}

function lastSeen(ageS) {
  if (ageS >= 5400) return "over 1h ago";
  if (ageS >= 90) return Math.round(ageS / 60) + "m ago";
  return Math.max(0, Math.round(ageS)) + "s ago";
}

function fmtUp(s) {
  if (s == null) return "—";
  if (s >= 86400) return Math.floor(s / 86400) + "d " + Math.floor(s % 86400 / 3600) + "h";
  if (s >= 3600) return Math.floor(s / 3600) + "h " + Math.floor(s % 3600 / 60) + "m";
  if (s >= 60) return Math.floor(s / 60) + "m";
  return Math.round(s) + "s";
}

/* ---------- geometry ---------- */

const ARC_R = { cpu: 84, gpu: 96, mem: 84 };

// 270° arc from 135° (lower-left) sweeping clockwise to 405° (lower-right).
function arcPath(r) {
  const a0 = 135 * Math.PI / 180, a1 = 405 * Math.PI / 180;
  const x0 = CX + r * Math.cos(a0), y0 = CY + r * Math.sin(a0);
  const x1 = CX + r * Math.cos(a1), y1 = CY + r * Math.sin(a1);
  return `M ${x0.toFixed(2)} ${y0.toFixed(2)} A ${r} ${r} 0 1 1 ${x1.toFixed(2)} ${y1.toFixed(2)}`;
}

function setArc(el, pct) {
  // Round linecaps close sub-pixel gaps into a blob at the arc's max end
  // (and render a stray dot near zero), so clamp the degenerate cases:
  // near-zero hides, near-max draws solid.
  if (pct == null || pct < 2) {
    el.style.visibility = "hidden";
    return;
  }
  el.style.visibility = "";
  const p = Math.min(100, pct);
  el.style.strokeDasharray = p >= 98 ? "100 100" : `${p} ${100 - p}`;
}

/* ---------- tooltip ---------- */

const tip = $("#tooltip");

function showTip(html, cx, cy) {
  tip.hidden = false;
  tip.innerHTML = html;
  const r = tip.getBoundingClientRect();
  let x = cx + 16, y = cy + 18;
  if (x + r.width > innerWidth - 8) x = cx - r.width - 16;
  if (y + r.height > innerHeight - 8) y = cy - r.height - 14;
  tip.style.left = Math.max(8, x) + "px";
  tip.style.top = Math.max(8, y) + "px";
}

function hideTip() { tip.hidden = true; }

function gaugeTipHTML(kind, L, socLabel) {
  const cpu = L.cpu, mem = L.memory, gpu = L.gpu;
  if (kind === "cpu") {
    if (!cpu) return "<div class='tt-title'>CPU</div>n/a";
    let busiest = "";
    const withIdx = (cpu.per_core_pct || [])
      .map((v, i) => [i, v]).filter(([, v]) => v != null && v > 1)
      .sort((a, b) => b[1] - a[1]).slice(0, 3);
    if (withIdx.length) {
      busiest = `<br>busiest: ${withIdx.map(([i, v]) => `${i}·${Math.round(v)}%`).join(", ")}`;
    }
    return `<div class='tt-title'>CPU</div>${fmtPct(cpu.util_pct)} of ${cpu.cores} logical cores${busiest}`;
  }
  if (kind === "gpu") {
    if (!gpu) return "<div class='tt-title'>GPU</div>n/a";
    const clock = gpu.sm_clock_mhz == null ? "" : `<br>SM clock ${gpu.sm_clock_mhz} MHz`;
    return `<div class='tt-title'>${gpu.name || "GPU"}</div>` +
      `util ${fmtPct(gpu.util_pct)} · ${fmtTemp(gpu.temp_c)} · ${fmtW(gpu.power_w)}${clock}`;
  }
  if (kind === "mem") {
    if (!mem || mem.total_kib == null) return "<div class='tt-title'>Memory</div>n/a";
    let swap = "";
    if (mem.swap_used_kib > 0) swap = `<br>swap ${GB(mem.swap_used_kib)} GB of ${GB(mem.swap_total_kib)} GB`;
    return `<div class='tt-title'>Unified memory</div>` +
      `${GB(mem.used_kib)} of ${GB(mem.total_kib)} GB (${fmtPct(mem.used_pct)})` +
      `<br>available ${GB(mem.available_kib)} GB${swap}`;
  }
  if (kind === "therm") {
    const therm = L.therm;
    const soc = therm && therm.soc_max_c;
    return `<div class='tt-title'>Temperature</div>GPU ${fmtTemp(L.gpu && L.gpu.temp_c)}` +
      `<br>${socLabel} ${fmtTemp(soc)}`;
  }
  return "";
}

/* ---------- panel construction ---------- */

const CHIP = {
  live:    { cls: "ok",   text: "LIVE",    svg: '<circle cx="6" cy="6" r="4"/>' },
  stale:   { cls: "warn", text: "STALE",   svg: '<path d="M6 1.5 L11 10.5 H1 Z"/>' },
  offline: { cls: "crit", text: "OFFLINE", svg: '<rect x="2" y="2" width="8" height="8"/>' },
};

function el(html) {
  const t = document.createElement("template");
  t.innerHTML = html.trim();
  return t.content.firstChild;
}

function makeGauge(kind, hero, label) {
  const r = ARC_R[kind];
  const root = el(`<div class="gauge${hero ? " hero" : ""}" data-g="${kind}" tabindex="0">
    <svg viewBox="0 0 ${VB} ${VB}" aria-hidden="true">
      <path class="arc-track" d="${arcPath(r)}" pathLength="100"/>
      <path class="arc-val" d="${arcPath(r)}" pathLength="100" stroke-dasharray="0 100"/>
      ${kind === "cpu" ? '<g class="fan"></g>' : ""}
    </svg>
    <div class="gcenter">
      <div class="glabel">${label}</div>
      <div class="gval">—</div>
      <div class="gcap"></div>
    </div>
  </div>`);
  return {
    root,
    arc: $(".arc-val", root),
    val: $(".gval", root),
    cap: $(".gcap", root),
    fan: $(".fan", root),
    fanTicks: null, // [{tick, hit}] — built when core count is known
  };
}

function buildFan(g, cores) {
  g.fan.innerHTML = "";
  g.fanTicks = [];
  const rin = 95;
  for (let i = 0; i < cores; i++) {
    const a = (-90 + i * 360 / cores) * Math.PI / 180;
    const x1 = CX + rin * Math.cos(a), y1 = CY + rin * Math.sin(a);
    const tick = document.createElementNS("http://www.w3.org/2000/svg", "line");
    tick.setAttribute("x1", x1.toFixed(1)); tick.setAttribute("y1", y1.toFixed(1));
    const hit = document.createElementNS("http://www.w3.org/2000/svg", "line");
    hit.setAttribute("x1", x1.toFixed(1)); hit.setAttribute("y1", y1.toFixed(1));
    hit.setAttribute("x2", x1.toFixed(1)); hit.setAttribute("y2", y1.toFixed(1));
    hit.setAttribute("class", "hit");
    hit.dataset.core = i;
    g.fan.appendChild(tick);
    g.fan.appendChild(hit);
    g.fanTicks.push({ tick, hit, cos: Math.cos(a), sin: Math.sin(a) });
  }
}

function ensurePanel(node, idx) {
  let p = panels.get(idx);
  if (p) return p;
  const root = el(`<section class="panel" style="--hue: ${node.color || "#3987e5"}"></section>`);
  const idrow = el(`<div class="idrow">
    <div class="name"></div>
    <div class="chip"><svg viewBox="0 0 12 12"></svg><span></span></div>
    <div class="uptime"></div>
  </div>`);
  root.appendChild(idrow);

  const gaugerow = el(`<div class="gaugerow"></div>`);
  const cpu = makeGauge("cpu", false, "CPU");
  const gpu = makeGauge("gpu", true, "GPU");
  const mem = makeGauge("mem", false, "MEM");
  [cpu, gpu, mem].forEach(g => gaugerow.appendChild(g.root));

  const side = el(`<div class="side">
    <div><div class="lbl">TEMP</div><div class="val" data-v="temp">—</div><div class="sub" data-v="soc">—</div></div>
    <div class="thermo" tabindex="0">
      <div class="track"></div><div class="mercury"></div><div class="socmark"><i></i></div>
    </div>
    <div><div class="lbl">POWER</div><div class="val" data-v="power">—</div><div class="cap" data-v="peak">—</div></div>
  </div>`);
  gaugerow.appendChild(side);
  root.appendChild(gaugerow);

  $("#panels").appendChild(root);

  p = {
    root,
    name: $(".name", idrow),
    chipSvg: $(".chip svg", idrow),
    chipText: $(".chip span", idrow),
    uptime: $(".uptime", idrow),
    cpu, gpu, mem,
    tempVal: $('[data-v="temp"]', side),
    socSub: $('[data-v="soc"]', side),
    powerVal: $('[data-v="power"]', side),
    peakCap: $('[data-v="peak"]', side),
    thermo: $(".thermo", side),
  };
  // thermometer threshold ticks are config-driven; labels added in render (first pass)
  panels.set(idx, p);
  return p;
}

/* ---------- header (shared by both views) ---------- */

function updateHeader(s) {
  let liveCount = 0;
  s.nodes.forEach(n => { if (n.status === "live") liveCount++; });
  const pill = $("#online");
  pill.hidden = false;
  $("#online-text").textContent = `${liveCount} Online`;
  pill.classList.toggle("zero", liveCount === 0);
  $("#stamp").textContent = "updated " + new Date().toLocaleTimeString();
  $("#poll").textContent = "poll " + Math.round(s.config.poll_interval_ms / 1000) + "s";
  document.title = `${liveCount} online · DGX Spark`;
}

/* ---------- render ---------- */

function render(s) {
  const th = s.config.thresholds;
  const socLabel = s.config.soc_label || "SoC";
  const panelsEl = $("#panels");
  panelsEl.style.gridTemplateColumns = `repeat(${s.nodes.length}, minmax(0, 1fr))`;

  s.nodes.forEach((node, idx) => {
    const p = ensurePanel(node, idx);
    if (node.color) p.root.style.setProperty("--hue", node.color);
    const st = node.status;

    const chip = CHIP[st] || CHIP.offline;
    p.chipSvg.innerHTML = chip.svg;
    p.chipText.textContent = st === "live" ? chip.text
      : chip.text + " · last seen " + lastSeen(node.last_good_age_s);
    p.root.classList.toggle("dim", st !== "live");
    p.uptime.textContent = "up " + fmtUp(node.latest && node.latest.uptime_s);

    p.name.textContent = node.name || (node.latest && node.latest.hostname) || node.url;

    const L = node.latest || {};
    const cpu = L.cpu, mem = L.memory, gpu = L.gpu, therm = L.therm;

    // CPU gauge + turbine
    p.cpu.val.textContent = fmtPct(cpu && cpu.util_pct);
    setArc(p.cpu.arc, cpu && cpu.util_pct);
    p.cpu.cap.textContent = cpu ? `${cpu.cores} logical cores` : "n/a";
    p.cpu.root.setAttribute("aria-label", `CPU ${fmtPct(cpu && cpu.util_pct)} of ${cpu ? cpu.cores : "?"} cores`);
    const perCore = (cpu && cpu.per_core_pct) || [];
    const cores = perCore.length || (cpu && cpu.cores) || 0;
    if (cores && (!p.cpu.fanTicks || p.cpu.fanTicks.length !== cores)) buildFan(p.cpu, cores);
    if (p.cpu.fanTicks) {
      p.cpu.fanTicks.forEach((t, i) => {
        const u = perCore[i];
        const bin = binClass(u);
        if (!bin) {
          fanHide(p, i);
          return;
        }
        const rin = 95, len = 4 + (u / 100) * 14;
        p.cpu.fanTicks[i].tick.setAttribute("class", bin);
        p.cpu.fanTicks[i].tick.setAttribute("x2", (CX + (rin + len) * p.cpu.fanTicks[i].cos).toFixed(1));
        p.cpu.fanTicks[i].tick.setAttribute("y2", (CY + (rin + len) * p.cpu.fanTicks[i].sin).toFixed(1));
        p.cpu.fanTicks[i].hit.setAttribute("x2", (CX + (rin + 18) * p.cpu.fanTicks[i].cos).toFixed(1));
        p.cpu.fanTicks[i].hit.setAttribute("y2", (CY + (rin + 18) * p.cpu.fanTicks[i].sin).toFixed(1));
      });
    }

    // GPU hero. The % is the headline read for an AI box.
    p.gpu.val.textContent = fmtPct(gpu && gpu.util_pct);
    setArc(p.gpu.arc, gpu && gpu.util_pct);
    p.gpu.cap.textContent = gpu && gpu.name ? gpu.name : "n/a";
    p.gpu.root.setAttribute("aria-label", `GPU ${fmtPct(gpu && gpu.util_pct)}`);

    // MEMORY (unified pool). Big value stays white on purpose: used = total −
    // available counts reclaimable cache, so a healthy GB10 idles near ~96%.
    // Only the arc tints past the (high) thresholds.
    p.mem.val.textContent = fmtPct(mem && mem.used_pct);
    setArc(p.mem.arc, mem && mem.used_pct);
    p.mem.cap.textContent = mem && mem.total_kib != null
      ? `${GB(mem.used_kib)} / ${GB(mem.total_kib)} GB` : "n/a";
    p.mem.arc.className.baseVal = "arc-val " +
      sevClass(mem && mem.used_pct, th.mem_warn_pct, th.mem_warn_pct, th.mem_crit_pct);
    p.mem.root.setAttribute("aria-label", `Memory ${fmtPct(mem && mem.used_pct)}`);

    // TEMP: GPU temperature headline + SoC marker on the thermometer
    const gt = gpu && gpu.temp_c;
    const gtSev = sevClass(gt, th.gpu_temp_warn_c, th.gpu_temp_serious_c, th.gpu_temp_critical_c);
    p.tempVal.textContent = (gtSev ? "▲ " : "") + fmtTemp(gt);
    p.tempVal.className = "val " + gtSev;
    const soc = therm && therm.soc_max_c;
    p.socSub.textContent = soc == null ? "—" : `${socLabel} ${Math.round(soc)}°`;
    p.socSub.className = "sub " + sevClass(soc, th.soc_temp_warn_c, th.soc_temp_serious_c, th.soc_temp_critical_c);
    drawThermo(p, gt, soc, gtSev, socLabel, th);

    // POWER: current + peak from the 10-minute ring
    p.powerVal.textContent = fmtW(gpu && gpu.power_w);
    const pw = (node.history && node.history.gpu_power_w) || [];
    const peak = pw.reduce((m, v) => v != null && v > m ? v : m, 0);
    p.peakCap.textContent = pw.some(v => v != null) ? `peak ${Math.round(peak)}W` : "n/a";

    // hover/focus wiring is static except aria labels
    wirePanel(p, node, s, idx);
  });
}

function fanHide(p, i) {
  const t = p.cpu.fanTicks[i];
  t.tick.style.visibility = "hidden";
  t.hit.style.visibility = "hidden";
}

function drawThermo(p, gt, soc, gtSev, socLabel, th) {
  const frac = v => (Math.max(TEMP_LO, Math.min(TEMP_HI, v)) - TEMP_LO) / (TEMP_HI - TEMP_LO) * 100;
  // scale furniture: labeled ticks at the GPU thresholds (rebuild if config changes)
  const marks = [[th.gpu_temp_warn_c, "warn"], [th.gpu_temp_serious_c, "serious"], [th.gpu_temp_critical_c, "critical"]];
  const key = JSON.stringify(marks);
  if (p.thermoKey !== key) {
    p.thermo.querySelectorAll(".tick").forEach(t => t.remove());
    marks.forEach(([t]) => {
      if (t == null || t <= TEMP_LO || t >= TEMP_HI) return;
      p.thermo.appendChild(el(`<div class="tick" style="bottom: ${frac(t)}%"><i>${Math.round(t)}</i></div>`));
    });
    p.thermoKey = key;
  }
  p.thermo.querySelector(".mercury").style.height = gt == null ? "0%" : frac(gt) + "%";
  const merc = p.thermo.querySelector(".mercury");
  merc.className = "mercury " + gtSev;
  const socEl = p.thermo.querySelector(".socmark");
  if (soc == null) {
    socEl.style.visibility = "hidden";
  } else {
    socEl.style.visibility = "";
    socEl.style.bottom = frac(soc) + "%";
    socEl.querySelector("i").textContent = socLabel;
  }
  p.thermo.setAttribute("aria-label", `GPU temperature ${fmtTemp(gt)}`);
}

/* ---------- hover wiring (idempotent per render) ---------- */

function wirePanel(p, node, s, idx) {
  if (p.wired) {
    p.wired.state = s; // refresh closure state
    return;
  }
  p.wired = { state: s, idx };
  const L = () => (p.wired.state.nodes[p.wired.idx] || {}).latest || {};
  const socLabel = () => p.wired.state.config.soc_label || "SoC";

  const gaugeHover = ev => {
    const kind = ev.currentTarget.dataset.g;
    const hit = ev.target.closest && ev.target.closest(".hit");
    let html;
    if (kind === "cpu" && hit) {
      const i = +hit.dataset.core;
      const v = (L().cpu && L().cpu.per_core_pct || [])[i];
      html = `<div class='tt-title'>Core ${i}</div>${fmtPct(v)}`;
    } else {
      html = gaugeTipHTML(kind, L(), socLabel());
    }
    showTip(html, ev.clientX, ev.clientY);
  };
  [p.cpu, p.gpu, p.mem].forEach(g => {
    g.root.addEventListener("mousemove", gaugeHover);
    g.root.addEventListener("mouseleave", hideTip);
  });

  // thermo hover + focus
  const thermoEl = p.thermo;
  thermoEl.addEventListener("mousemove", ev =>
    showTip(gaugeTipHTML("therm", L(), socLabel()), ev.clientX, ev.clientY));
  thermoEl.addEventListener("mouseleave", hideTip);

  // focus shows the same tooltip anchored to the element (keyboard path)
  const focusTip = elm => () => {
    const r = elm.getBoundingClientRect();
    const html = elm.classList.contains("thermo")
      ? gaugeTipHTML("therm", L(), socLabel())
      : gaugeTipHTML(elm.dataset.g, L(), socLabel());
    showTip(html, r.right, r.top);
  };
  [p.cpu, p.gpu, p.mem].forEach(g => {
    g.root.addEventListener("focus", focusTip(g.root));
    g.root.addEventListener("blur", hideTip);
  });
  thermoEl.addEventListener("focus", focusTip(thermoEl));
  thermoEl.addEventListener("blur", hideTip);
}

/* ---------- table view ---------- */

const CHIP_GLYPH = {
  live: '<circle cx="6" cy="6" r="4"/>',
  stale: '<path d="M6 1.5 L11 10.5 H1 Z"/>',
  offline: '<rect x="2" y="2" width="8" height="8"/>',
};

const TV_COLS = [
  { h: "Node", f: n => n.name },
  { h: "Status", f: n => `<span class="chip ${CHIP[n.status] ? CHIP[n.status].cls : "crit"}">` +
      `<svg viewBox="0 0 12 12">${CHIP_GLYPH[n.status] || CHIP_GLYPH.offline}</svg>` +
      `<span>${n.status.toUpperCase()}</span></span>` },
  { h: "CPU", num: true, f: n => fmtPct(n.latest && n.latest.cpu && n.latest.cpu.util_pct) },
  { h: "Cores", num: true, f: n => n.latest && n.latest.cpu ? n.latest.cpu.cores : "—" },
  { h: "MEM", num: true, f: n => fmtPct(n.latest && n.latest.memory && n.latest.memory.used_pct) },
  { h: "Memory", f: n => n.latest && n.latest.memory && n.latest.memory.total_kib != null
      ? `${GB(n.latest.memory.used_kib)} / ${GB(n.latest.memory.total_kib)} GB` : "n/a" },
  { h: "Swap", num: true, f: n => n.latest && n.latest.memory && n.latest.memory.swap_used_kib > 0
      ? GB(n.latest.memory.swap_used_kib) + " GB" : "—" },
  { h: "GPU", num: true, f: n => fmtPct(n.latest && n.latest.gpu && n.latest.gpu.util_pct) },
  { h: "GPU temp", num: true, f: n => fmtTemp(n.latest && n.latest.gpu && n.latest.gpu.temp_c) },
  { h: "SoC temp", num: true, f: n => fmtTemp(n.latest && n.latest.therm && n.latest.therm.soc_max_c) },
  { h: "Power", num: true, f: n => fmtW(n.latest && n.latest.gpu && n.latest.gpu.power_w) },
  { h: "SM clock", num: true, f: n => n.latest && n.latest.gpu && n.latest.gpu.sm_clock_mhz != null
      ? n.latest.gpu.sm_clock_mhz + " MHz" : "—" },
  { h: "Uptime", num: true, f: n => fmtUp(n.latest && n.latest.uptime_s) },
];

function renderTable(s) {
  const head = $("#tv-head");
  if (!head.children.length) {
    head.innerHTML = TV_COLS.map(c => `<th${c.num ? ' class="num"' : ""}>${c.h}</th>`).join("");
  }
  $("#tv-body").innerHTML = s.nodes.map(n =>
    "<tr>" + TV_COLS.map(c => `<td${c.num ? ' class="num"' : ""}>${c.f(n)}</td>`).join("") + "</tr>"
  ).join("");
}

let tableOn = false;

function toggleView(force) {
  tableOn = force != null ? force : !tableOn;
  $("#panels").hidden = tableOn;
  $("#tableview").hidden = !tableOn;
  $("#view-toggle").setAttribute("aria-pressed", String(tableOn));
}

$("#view-toggle").addEventListener("click", () => toggleView());
if (location.hash === "#table") toggleView(true); // bookmarkable table view
document.addEventListener("keydown", ev => {
  if ((ev.key === "t" || ev.key === "T") && !ev.repeat && !ev.metaKey && !ev.ctrlKey && !ev.altKey) {
    toggleView();
  }
});

/* ---------- polling ---------- */

let misses = 0;

async function tick() {
  try {
    const res = await fetch("/api/state", { signal: AbortSignal.timeout(1800) });
    if (!res.ok) throw new Error("HTTP " + res.status);
    const s = await res.json();
    updateHeader(s);
    if (!tableOn) render(s);
    else renderTable(s);
    misses = 0;
    document.body.classList.remove("stale-view");
  } catch {
    // hold the previous render; dim only after two consecutive misses so a
    // single slow tick doesn't flash the whole page
    if (++misses >= 2) document.body.classList.add("stale-view");
  }
}

tick();
setInterval(tick, 2000);
setInterval(() => { $("#clock").textContent = new Date().toLocaleTimeString(); }, 1000);
$("#clock").textContent = new Date().toLocaleTimeString();
