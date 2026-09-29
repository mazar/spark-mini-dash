"use strict";

/* spark-mini-dash UI — vanilla JS, no build step.
   Polls /api/state every 2s and renders one full-width row per node,
   matching the NVIDIA Sync Resource Monitor's layout. */

const $ = (sel, el = document) => el.querySelector(sel);

const SPARK_W = 190, SPARK_H = 36, TEMP_LO = 20, TEMP_HI = 110;

let rows = new Map(); // node index → DOM refs (the config position is the only identity stable across polls)

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

/* ---------- row construction ---------- */

const CHIP = {
  live:    { cls: "ok",   text: "LIVE",    svg: '<circle cx="6" cy="6" r="4"/>' },
  stale:   { cls: "warn", text: "STALE",   svg: '<path d="M6 1.5 L11 10.5 H1 Z"/>' },
  offline: { cls: "crit", text: "OFFLINE", svg: '<rect x="2" y="2" width="8" height="8"/>' },
};

function ensureRow(node, idx) {
  let r = rows.get(idx);
  if (r) return r;
  const root = document.createElement("section");
  root.className = "node";
  root.style.setProperty("--hue", node.color || "#3987e5");
  root.innerHTML = `
    <div class="id">
      <div class="name"></div>
      <div class="chip"><svg viewBox="0 0 12 12"></svg><span></span></div>
      <div class="heatmap"></div>
    </div>
    <div class="metric" data-m="cpu">
      <div class="lbl">CPU</div><div class="val"></div>
      <div class="bar"><i></i></div><div class="cap"></div>
    </div>
    <div class="metric" data-m="mem">
      <div class="lbl">MEMORY</div><div class="val"></div>
      <div class="bar"><i></i></div><div class="cap"></div>
    </div>
    <div class="metric" data-m="gpu">
      <div class="lbl">GPU</div><div class="val"></div>
      <div class="bar"><i></i></div><div class="cap"></div>
    </div>
    <div class="metric" data-m="temp">
      <div class="lbl">TEMP</div><div class="val"></div><div class="sub"></div>
      <svg class="spark" viewBox="0 0 ${SPARK_W} ${SPARK_H}" preserveAspectRatio="none"></svg>
    </div>
    <div class="metric" data-m="power">
      <div class="lbl">POWER</div><div class="val"></div>
    </div>`;
  $("#cards").appendChild(root);
  const q = s => $(s, root);
  const heat = q(".heatmap");
  const cells = [];
  for (let i = 0; i < 20; i++) {
    const c = document.createElement("i");
    c.className = "cell";
    heat.appendChild(c);
    cells.push(c);
  }
  r = {
    root,
    name: q(".name"),
    chipSvg: q(".chip svg"),
    chipText: q(".chip span"),
    cells,
    cpuVal: q('[data-m="cpu"] .val'), cpuBar: q('[data-m="cpu"] .bar i'), cpuCap: q('[data-m="cpu"] .cap'),
    memVal: q('[data-m="mem"] .val'), memBar: q('[data-m="mem"] .bar i'), memCap: q('[data-m="mem"] .cap'),
    gpuVal: q('[data-m="gpu"] .val'), gpuBar: q('[data-m="gpu"] .bar i'), gpuCap: q('[data-m="gpu"] .cap'),
    tempVal: q('[data-m="temp"] .val'), tempSub: q('[data-m="temp"] .sub'), spark: q('[data-m="temp"] .spark'),
    powerVal: q('[data-m="power"] .val'),
  };
  rows.set(idx, r);
  return r;
}

function lastSeen(ageS) {
  if (ageS >= 5400) return "over 1h ago";
  if (ageS >= 90) return Math.round(ageS / 60) + "m ago";
  return Math.max(0, Math.round(ageS)) + "s ago";
}

/* ---------- render ---------- */

function setBar(el, pct) {
  el.style.width = pct == null ? "0%" : Math.max(0, Math.min(100, pct)) + "%";
}

function sparkline(svg, values, hue) {
  if (!values || values.length < 2) { svg.innerHTML = ""; return; }
  let d = "", pen = false, lastPt = null;
  const n = values.length;
  for (let i = 0; i < n; i++) {
    const v = values[i];
    if (v == null) { pen = false; continue; }
    const x = (i / (n - 1)) * SPARK_W;
    const y = SPARK_H - ((v - TEMP_LO) / (TEMP_HI - TEMP_LO)) * SPARK_H;
    d += (pen ? "L" : "M") + x.toFixed(1) + " " + y.toFixed(1) + " ";
    pen = true;
    lastPt = { x, y };
  }
  let dot = "";
  if (lastPt) {
    dot = `<circle class="dot" cx="${lastPt.x.toFixed(1)}" cy="${lastPt.y.toFixed(1)}" r="3.5"/>`;
  }
  svg.innerHTML = d ? `<path class="line" d="${d}"/>${dot}` : "";
}

function render(s) {
  const th = s.config.thresholds;
  const cards = $("#cards");
  cards.style.gridTemplateRows = `repeat(${s.nodes.length}, minmax(0, 1fr))`;

  let liveCount = 0;
  s.nodes.forEach((node, idx) => {
    const r = ensureRow(node, idx);
    if (node.color) r.root.style.setProperty("--hue", node.color);
    const st = node.status;
    if (st === "live") liveCount++;

    // status chip (glyph + word, never color alone); stale/offline keep
    // last-good values but dim the row
    const chip = CHIP[st] || CHIP.offline;
    r.chipSvg.innerHTML = chip.svg;
    r.chipText.textContent = st === "live" ? chip.text
      : chip.text + " · last seen " + lastSeen(node.last_good_age_s);
    r.root.classList.toggle("dim", st !== "live");

    const L = node.latest || {};
    const cpu = L.cpu, mem = L.memory, gpu = L.gpu, therm = L.therm;

    r.name.textContent = node.name || (L.hostname || node.url);

    // CPU
    r.cpuVal.textContent = fmtPct(cpu && cpu.util_pct);
    setBar(r.cpuBar, cpu && cpu.util_pct);
    r.cpuCap.textContent = cpu ? `${cpu.cores} logical cores` : "n/a";
    const perCore = (cpu && cpu.per_core_pct) || [];
    r.cells.forEach((c, i) => c.className = "cell " + binClass(perCore[i]));

    // MEMORY (unified pool; decimal GB captions like Sync). The big value
    // stays white on purpose: used = total − available counts reclaimable
    // cache, so a healthy GB10 idles at ~96% and the number is
    // informational. Only the bar tints past the (high) thresholds.
    r.memVal.textContent = fmtPct(mem && mem.used_pct);
    setBar(r.memBar, mem && mem.used_pct);
    if (mem && mem.total_kib != null) {
      let cap = `${GB(mem.used_kib)} GB of ${GB(mem.total_kib)} GB`;
      if (mem.swap_used_kib > 0) cap += ` · swap ${GB(mem.swap_used_kib)} GB`;
      r.memCap.textContent = cap;
    } else {
      r.memCap.textContent = "n/a";
    }
    r.memBar.className = sevClass(mem && mem.used_pct, th.mem_warn_pct, th.mem_warn_pct, th.mem_crit_pct);

    // GPU
    r.gpuVal.textContent = fmtPct(gpu && gpu.util_pct);
    setBar(r.gpuBar, gpu && gpu.util_pct);
    r.gpuCap.textContent = gpu && gpu.name ? gpu.name : "n/a";

    // TEMP: GPU temperature headline + SoC max secondary + trend sparkline
    const gt = gpu && gpu.temp_c;
    const gtSev = sevClass(gt, th.gpu_temp_warn_c, th.gpu_temp_serious_c, th.gpu_temp_critical_c);
    r.tempVal.textContent = (gtSev ? "▲ " : "") + fmtTemp(gt);
    r.tempVal.className = "val " + gtSev;
    const soc = therm && therm.soc_max_c;
    r.tempSub.textContent = soc == null ? "—" :
      `${s.config.soc_label} ${Math.round(soc)}°`;
    r.tempSub.className = "sub " + sevClass(soc, th.soc_temp_warn_c, th.soc_temp_serious_c, th.soc_temp_critical_c);
    sparkline(r.spark, node.history && node.history.gpu_temp_c, node.color);

    // POWER
    r.powerVal.textContent = fmtW(gpu && gpu.power_w);
  });

  // header pill: N Online (green), or red when none
  const pill = $("#online");
  pill.hidden = false;
  $("#online-text").textContent = `${liveCount} Online`;
  pill.classList.toggle("zero", liveCount === 0);

  $("#stamp").textContent = "updated " + new Date().toLocaleTimeString();
  document.title = `${liveCount} online · DGX Spark`;
}

/* ---------- polling ---------- */

async function tick() {
  try {
    const res = await fetch("/api/state", { signal: AbortSignal.timeout(1500) });
    if (!res.ok) throw new Error("HTTP " + res.status);
    render(await res.json());
    document.body.classList.remove("stale-view");
  } catch {
    // hold the previous render, dimmed — no skeleton flash
    document.body.classList.add("stale-view");
  }
}

tick();
setInterval(tick, 2000);
setInterval(() => { $("#clock").textContent = new Date().toLocaleTimeString(); }, 1000);
$("#clock").textContent = new Date().toLocaleTimeString();
