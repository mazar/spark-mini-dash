"use strict";

/* spark-mini-dash UI — vanilla JS, no build step.
   Two-half layout: node metric strips (left) and the PAIR request-flow
   view (right). Polls /api/state; pairs with a PAIR cluster as a passive,
   read-only node (POST /api/pair/respond carries the one-time PIN). */

const $ = (sel, el = document) => el.querySelector(sel);

const TEMP_LO = 20, TEMP_HI = 110;    // tooltip temp scale

let panels = new Map(); // node index → DOM refs (config position is the only identity stable across polls)

/* ---------- formatting ---------- */

const fmtPct = v => v == null ? "—" : Math.round(v) + "%";
const fmtTemp = v => v == null ? "—" : Math.round(v) + "°C";
const fmtW = v => v == null ? "—" : Math.round(v) + "W";
const GB = kib => Math.round(kib * 1024 / 1e9); // decimal GB, matching Sync

function coreClass(u) {
  if (u == null || u < 1) return "";
  if (u <= 25) return "c0";
  if (u <= 50) return "c1";
  if (u <= 75) return "c2";
  return "c3";
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

function fmtElapsed(ms) {
  if (ms == null || ms < 0) return "—";
  const s = Math.floor(ms / 1000);
  if (s >= 3600) return Math.floor(s / 3600) + "h" + String(Math.floor(s % 3600 / 60)).padStart(2, "0") + "m";
  if (s >= 60) return Math.floor(s / 60) + "m" + String(s % 60).padStart(2, "0") + "s";
  return s + "s";
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

function nodeTipHTML(kind, L, socLabel) {
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

/* ---------- panel construction (metric strips) ---------- */

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

function makeMeter(kind, label) {
  return el(`<div class="meter" data-g="${kind}" tabindex="0">
    <div class="mhead"><span class="mlabel">${label}</span><span class="mval">—</span></div>
    <div class="mbar"><div class="mfill"></div></div>
    <div class="mcap"></div>
  </div>`);
}

function buildPanel(node) {
  const root = el(`<section class="panel" style="--hue: ${node.color || "#3987e5"}"></section>`);
  const idrow = el(`<div class="idrow">
    <div class="name"></div>
    <div class="chip"><svg viewBox="0 0 12 12"></svg><span></span></div>
    <div class="uptime"></div>
  </div>`);
  root.appendChild(idrow);

  const meters = el(`<div class="meters"></div>`);
  const cpu = makeMeter("cpu", "CPU");
  const gpu = makeMeter("gpu", "GPU");
  const mem = makeMeter("mem", "MEM");
  const strip = el(`<div class="corestrip" aria-hidden="true"></div>`);
  $(".mcap", cpu).after(strip);
  [cpu, gpu, mem].forEach(m => meters.appendChild(m));
  root.appendChild(meters);

  const tiles = el(`<div class="tiles">
    <div class="tile" data-g="therm" tabindex="0">
      <div class="tlbl">TEMP</div><div class="tval" data-v="temp">—</div><div class="tsub" data-v="soc">—</div>
    </div>
    <div class="tile" data-g="power" tabindex="0">
      <div class="tlbl">POWER</div><div class="tval" data-v="power">—</div><div class="tcap" data-v="peak">—</div>
    </div>
  </div>`);
  root.appendChild(tiles);

  $("#metrics").appendChild(root);
  return {
    root,
    name: $(".name", idrow),
    chipSvg: $(".chip svg", idrow),
    chipText: $(".chip span", idrow),
    uptime: $(".uptime", idrow),
    cpu, gpu, mem,
    strip,
    tempVal: $('[data-v="temp"]', tiles),
    socSub: $('[data-v="soc"]', tiles),
    powerVal: $('[data-v="power"]', tiles),
    peakCap: $('[data-v="peak"]', tiles),
  };
}

/* ---------- metrics render ---------- */

function setMeter(m, pct, warn, serious, crit) {
  $(".mval", m).textContent = fmtPct(pct);
  const fill = $(".mfill", m);
  if (pct == null || pct < 0.5) {
    fill.style.width = "0%";
  } else {
    fill.style.width = Math.min(100, pct) + "%";
  }
  fill.className = "mfill " + sevClass(pct, warn, serious, crit);
}

function renderNode(p, node, th, socLabel) {
  const st = node.status;
  const chip = CHIP[st] || CHIP.offline;
  p.chipSvg.innerHTML = chip.svg;
  p.chipText.textContent = st === "live" ? chip.text
    : chip.text + " · last seen " + lastSeen(node.last_good_age_s);
  p.root.classList.toggle("dim", st !== "live");
  p.uptime.textContent = "up " + fmtUp(node.latest && node.latest.uptime_s);
  p.name.textContent = node.name || (node.latest && node.latest.hostname) || node.url;

  const L = node.latest || {};
  const cpu = L.cpu, mem = L.memory, gpu = L.gpu;

  // CPU + per-core strip
  setMeter(p.cpu, cpu && cpu.util_pct);
  p.cpu.setAttribute("aria-label", `CPU ${fmtPct(cpu && cpu.util_pct)} of ${cpu ? cpu.cores : "?"} cores`);
  $(".mcap", p.cpu).textContent = cpu ? `${cpu.cores} logical cores` : "n/a";
  const perCore = (cpu && cpu.per_core_pct) || [];
  const strip = p.strip;
  if (strip.childElementCount !== perCore.length) {
    strip.innerHTML = "";
    for (let i = 0; i < perCore.length; i++) {
      const seg = document.createElement("i");
      seg.dataset.core = i;
      strip.appendChild(seg);
    }
  }
  [...strip.children].forEach((seg, i) => {
    seg.className = coreClass(perCore[i]);
  });

  // GPU
  setMeter(p.gpu, gpu && gpu.util_pct);
  $(".mcap", p.gpu).textContent = gpu && gpu.name ? gpu.name : "n/a";
  p.gpu.setAttribute("aria-label", `GPU ${fmtPct(gpu && gpu.util_pct)}`);

  // MEMORY (unified pool). Value stays white on purpose: used = total −
  // available counts reclaimable cache, so a healthy GB10 idles near ~96%.
  // Only the meter tints past the (high) thresholds.
  setMeter(p.mem, mem && mem.used_pct, th.mem_warn_pct, th.mem_warn_pct, th.mem_crit_pct);
  $(".mcap", p.mem).textContent = mem && mem.total_kib != null
    ? `${GB(mem.used_kib)} / ${GB(mem.total_kib)} GB` : "n/a";
  p.mem.setAttribute("aria-label", `Memory ${fmtPct(mem && mem.used_pct)}`);

  // TEMP + POWER tiles
  const gt = gpu && gpu.temp_c;
  const gtSev = sevClass(gt, th.gpu_temp_warn_c, th.gpu_temp_serious_c, th.gpu_temp_critical_c);
  p.tempVal.textContent = (gtSev ? "▲ " : "") + fmtTemp(gt);
  p.tempVal.className = "tval " + gtSev;
  const soc = L.therm && L.therm.soc_max_c;
  p.socSub.textContent = soc == null ? "—" : `${socLabel} ${Math.round(soc)}°`;
  p.socSub.className = "tsub " + sevClass(soc, th.soc_temp_warn_c, th.soc_temp_serious_c, th.soc_temp_critical_c);

  p.powerVal.textContent = fmtW(gpu && gpu.power_w);
  const pw = (node.history && node.history.gpu_power_w) || [];
  const peak = pw.reduce((m, v) => v != null && v > m ? v : m, 0);
  p.peakCap.textContent = pw.some(v => v != null) ? `peak ${Math.round(peak)}W` : "n/a";
}

function render(s) {
  const th = s.config.thresholds;
  const metricsEl = $("#metrics");
  metricsEl.style.gridTemplateColumns = `repeat(${s.nodes.length}, minmax(0, 1fr))`;

  s.nodes.forEach((node, idx) => {
    let p = panels.get(idx);
    if (!p) {
      p = buildPanel(node);
      panels.set(idx, p);
      wirePanel(p);
    }
    if (node.color) p.root.style.setProperty("--hue", node.color);
    renderNode(p, node, th, s.config.soc_label || "SoC");
  });
}

/* ---------- metric hover wiring ---------- */

function wirePanel(p) {
  const hover = ev => {
    const kind = ev.currentTarget.dataset.g;
    if (kind === "power") return;
    const idx = +[...p.root.parentNode.children].indexOf(p.root);
    const s = lastState;
    if (!s) return;
    const node = s.nodes[idx] || {};
    showTip(nodeTipHTML(kind, node.latest || {}, s.config.soc_label || "SoC"), ev.clientX, ev.clientY);
  };
  [p.cpu, p.gpu, p.mem].forEach(m => {
    m.addEventListener("mousemove", hover);
    m.addEventListener("mouseleave", hideTip);
    m.addEventListener("focus", ev => hover(ev));
    m.addEventListener("blur", hideTip);
  });
  const therm = p.root.querySelector('[data-g="therm"]');
  therm.addEventListener("mousemove", hover);
  therm.addEventListener("mouseleave", hideTip);
  therm.addEventListener("focus", hover);
  therm.addEventListener("blur", hideTip);
}

/* ---------- PAIR request-flow view ---------- */

const Flow = (() => {
  const canvas = $("#flow-canvas");
  const ctx2d = canvas.getContext("2d");
  const reduceMotion = matchMedia("(prefers-reduced-motion: reduce)").matches;

  // categorical palette for flow entities — the same CVD-validated order the
  // dashboard assigns to nodes (internal/config DefaultNodeColors), fixed and
  // never cycled past the end
  const PALETTE = ["#3987e5", "#d95926", "#9085e9", "#199e70", "#c98500", "#d55181"];

  let data = { status: "off", members: [], nodes: [], inflight: [], recent: [] };
  const hueByUuid = new Map(); // machine uuid -> hue, first-seen order (stable)
  let raf = 0;

  function machineName(uuid) {
    const n = (data.nodes || []).find(x => x.hostUuid === uuid);
    if (n && n.name) return n.name.toUpperCase();
    const m = (data.members || []).find(x => x.nodeUuid === uuid);
    if (m && (m.name || m.id)) return String(m.name || m.id).toUpperCase();
    return "node-" + String(uuid || "?").slice(0, 4);
  }

  function hue(uuid) {
    if (!hueByUuid.has(uuid)) {
      hueByUuid.set(uuid, PALETTE[hueByUuid.size % PALETTE.length]);
    }
    return hueByUuid.get(uuid);
  }

  function setData(pair) {
    data = pair || { status: "off", members: [], nodes: [], inflight: [], recent: [] };
    ensureLoop();
  }

  /* ----- layout: requests (left, by model) → machines (right) ----- */

  // A machine shows when it is serving models (its inventory is non-empty) or
  // is executing a current request; model-less machines (the bridge node, a
  // laptop) stay off the view.
  function machines() {
    const executors = new Set((data.inflight || []).filter(x => x.scheduledOn).map(x => x.scheduledOn));
    return (data.nodes || [])
      .filter(n => (n.models && n.models.length) || executors.has(n.hostUuid))
      .sort((a, b) => String(a.name || "").localeCompare(String(b.name || "")));
  }

  function jobY(i, n, h) {
    return n <= 1 ? h / 2 : h * 0.16 + h * 0.68 * (i / (n - 1));
  }

  /* ----- drawing ----- */

  function roundRectDot(x, y, r, hueV, glow) {
    ctx2d.beginPath();
    ctx2d.arc(x, y, r, 0, Math.PI * 2);
    ctx2d.fillStyle = hueV;
    ctx2d.fill();
    if (glow > 0) {
      ctx2d.beginPath();
      ctx2d.arc(x, y, r + 6 + glow * 6, 0, Math.PI * 2);
      ctx2d.strokeStyle = hueV;
      ctx2d.globalAlpha = 0.35 * (1 - glow * 0.5);
      ctx2d.lineWidth = 2;
      ctx2d.stroke();
      ctx2d.globalAlpha = 1;
    }
  }

  // card-style label: bold title, muted sub-lines beneath; align "right"
  // mirrors the left column so labels hug their dots from either side
  function drawCardLabel(title, x, y, lines, align) {
    ctx2d.textAlign = align || "left";
    ctx2d.fillStyle = "#ffffff";
    ctx2d.font = "600 18px system-ui, sans-serif";
    ctx2d.fillText(title, x, y + 7);
    ctx2d.fillStyle = "#898781";
    ctx2d.font = "500 16px system-ui, sans-serif";
    (lines || []).forEach((line, i) => {
      if (line) ctx2d.fillText(line, x, y + 30 + i * 20);
    });
    ctx2d.textAlign = "left";
  }

  function curve(x0, y0, x1, y1) {
    const mx = (x0 + x1) / 2;
    return { cx: mx, cy: Math.min(y0, y1) - Math.min(60, Math.abs(y1 - y0) * 0.3 + 18) };
  }

  function draw(now) {
    const wrap = canvas.parentNode.getBoundingClientRect();
    const dpr = devicePixelRatio || 1;
    if (canvas.width !== Math.round(wrap.width * dpr) || canvas.height !== Math.round(wrap.height * dpr)) {
      canvas.width = Math.round(wrap.width * dpr);
      canvas.height = Math.round(wrap.height * dpr);
    }
    ctx2d.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx2d.clearRect(0, 0, wrap.width, wrap.height);

    const w = wrap.width, h = wrap.height;
    const jobs = data.inflight || [];
    const ms = machines();
    const jx = Math.max(w * 0.16, 130), mx = w * 0.84;
    const beam = (y0, y1, hueV, alpha) => {
      const cx = (jx + mx) / 2;
      const cy = Math.min(y0, y1) - Math.min(60, Math.abs(y1 - y0) * 0.3 + 18);
      ctx2d.strokeStyle = hueV;
      ctx2d.globalAlpha = alpha;
      ctx2d.beginPath();
      ctx2d.moveTo(jx, y0);
      ctx2d.quadraticCurveTo(cx, cy, mx, y1);
      ctx2d.stroke();
      ctx2d.globalAlpha = 1;
      return { cx, cy };
    };

    // connector beams: running requests solid, queued requests dashed to the
    // machine they are already scheduled on
    ctx2d.lineWidth = 1.5;
    jobs.forEach((j, ji) => {
      if (!j.scheduledOn || (j.state !== "running" && j.state !== "queued")) return;
      const mi = ms.findIndex(m => m.hostUuid === j.scheduledOn);
      if (mi < 0) return;
      const queued = j.state === "queued";
      ctx2d.setLineDash(queued ? [5, 5] : []);
      beam(jobY(ji, jobs.length, h), jobY(mi, ms.length, h), hue(j.scheduledOn), queued ? 0.3 : 0.55);
      ctx2d.setLineDash([]);
    });

    // left column: one entry per active request, labeled by model
    jobs.forEach((j, ji) => {
      const y = jobY(ji, jobs.length, h);
      const running = j.state === "running" && j.scheduledOn;
      const pulse = running || reduceMotion ? 0 : ((Math.sin(now / 300) + 1) / 2) * 0.6;
      roundRectDot(jx, y, 7, j.scheduledOn ? hue(j.scheduledOn) : "#898781", pulse);
      const elapsed = fmtElapsed(Date.now() - (j.startedAt || j.createdAt || Date.now()));
      drawCardLabel(j.model || "?", jx + 16, y, [
        "from " + machineName(j.originatedFrom).toLowerCase(),
        running ? elapsed : "queued",
      ]);
    });
    if (!jobs.length && data.status === "joined") {
      ctx2d.fillStyle = "#898781";
      ctx2d.font = "500 18px system-ui, sans-serif";
      ctx2d.textAlign = "left";
      ctx2d.fillText("no active requests", jx - 60, h / 2);
    }

    // right column: machines of the cluster that are serving models
    ms.forEach((m, mi) => {
      const y = jobY(mi, ms.length, h);
      const held = jobs.filter(x => x.scheduledOn === m.hostUuid).length;
      const run = jobs.filter(x => x.scheduledOn === m.hostUuid && x.state === "running").length;
      const pulse = reduceMotion ? (run > 0 ? 0.4 : 0) : (run > 0 ? (Math.sin(now / 260) + 1) / 2 : 0);
      roundRectDot(mx, y, 11, hue(m.hostUuid), pulse);
      const gpu = m.gpus && m.gpus[0] && m.gpus[0].name;
      drawCardLabel(machineName(m.hostUuid), mx - 16, y, [
        [m.ip, gpu].filter(Boolean).join(" · "),
        held > 0 ? run + " running · " + (held - run) + " queued" : "ready · " + (m.models ? m.models.length : 0) + " models",
      ], "right");
    });

    // drifting particles along each running request's beam
    if (!reduceMotion) {
      jobs.forEach((j, ji) => {
        if (j.state !== "running" || !j.scheduledOn) return;
        const mi = ms.findIndex(m => m.hostUuid === j.scheduledOn);
        if (mi < 0) return;
        const { cx, cy } = beam(jobY(ji, jobs.length, h), jobY(mi, ms.length, h), hue(j.scheduledOn), 0);
        const t = (now / 1400 + (hash(j.id || ji + "") % 100) / 100) % 1;
        ctx2d.beginPath();
        ctx2d.arc(q(jx, cx, mx, t), q(jobY(ji, jobs.length, h), cy, jobY(mi, ms.length, h), t), 3.5, 0, Math.PI * 2);
        ctx2d.fillStyle = hue(j.scheduledOn);
        ctx2d.fill();
      });
    }

    if (data.status === "joined" || jobs.length) {
      if (!reduceMotion) raf = requestAnimationFrame(draw);
    } else {
      raf = 0;
    }
  }

  const q = (a, b, c, t) => (1 - t) * (1 - t) * a + 2 * (1 - t) * t * b + t * t * c;
  const hash = s => { let h = 0; for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) | 0; return Math.abs(h); };

  function ensureLoop() {
    if (!raf && !reduceMotion) raf = requestAnimationFrame(draw);
    else if (reduceMotion) draw(performance.now());
  }

  return { setData };
})();

/* ---------- pairing status DOM ---------- */

function renderPair(pair) {
  const statusEl = $("#pair-status");
  Flow.setData(pair);

  // status line / pairing form
  if (!pair || pair.status === "off") {
    statusEl.textContent = "bridge disabled (--pair-enabled)";
  } else if (pair.status === "unpaired") {
    statusEl.textContent = "not joined — invite this node from a PAIR GUI, then enter its PIN here";
  } else if (pair.status === "pairing" && pair.invite) {
    if (!statusEl.querySelector("form")) {
      statusEl.replaceChildren(el(`<form id="pair-form">
        <span>PIN shown on <b>${pair.invite.fromNodeName || "the inviting node"}</b>:</span>
        <input id="pair-pin" inputmode="numeric" autocomplete="off" placeholder="••••••" maxlength="6" required>
        <button type="submit">Join</button>
      </form>`));
      $("#pair-form").addEventListener("submit", async ev => {
        ev.preventDefault();
        const pin = $("#pair-pin").value.trim();
        try {
          const res = await fetch("/api/pair/respond", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ inviteId: pair.invite.inviteId, pin }),
            signal: AbortSignal.timeout(30000),
          });
          if (!res.ok) throw new Error(await res.text());
          $("#pair-pin").value = "";
          statusEl.textContent = "joining…";
        } catch (err) {
          statusEl.textContent = "join failed: " + err.message;
        }
      });
      $("#pair-pin").focus();
    }
  } else if (pair.status === "pairing") {
    statusEl.textContent = "waiting for invite…";
  } else if (pair.status === "joined") {
    statusEl.innerHTML = `<span class="live">● ${pair.members.length} member${pair.members.length === 1 ? "" : "s"}</span>`;
  } else if (pair.status === "error") {
    statusEl.textContent = "bridge error: " + (pair.error || "unknown");
  }
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
  $("#stage").hidden = tableOn;
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

let lastState = null;
let misses = 0;

async function tick() {
  try {
    const res = await fetch("/api/state", { signal: AbortSignal.timeout(1800) });
    if (!res.ok) throw new Error("HTTP " + res.status);
    const s = await res.json();
    lastState = s;
    updateHeader(s);
    if (!tableOn) {
      render(s);
      renderPair(s.pair || { status: "off" });
    } else {
      renderTable(s);
    }
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
setInterval(() => {
  $("#clock").textContent = new Date().toLocaleTimeString();
}, 1000);
$("#clock").textContent = new Date().toLocaleTimeString();
