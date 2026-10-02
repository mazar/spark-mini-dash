# spark-mini-dash

A compact wall dashboard for a cluster of NVIDIA DGX Spark machines: CPU,
GPU, and unified-memory utilization plus temperatures and power, rendered as
one instrument panel per node — circular gauges, a per-core turbine ring, a
thermometer, and a 10-minute recorder strip (inspired by the Resource Monitor
built into [NVIDIA Sync](https://docs.nvidia.com/sync/latest/resource-monitor.html))
and sized for a **1920x480** display.

```
┌────────────┐  HTTP /metrics   ┌──────────────────┐   dashboard stream    ┌──────────────────────┐
│ spark-dash │ ──── 2s poll ───▶│ spark-agent :9105 │──127.0.0.1:11000────▶│ dgx-dashboard-service │
│  :8080     │  1.5s timeout    │ (per node,        │  (optional source;   │ (preinstalled; JWT)   │
│ poller +   │                  │  systemd)         │   fallback nvidia-smi)└──────────────────────┘
│ 10-min ring│                  └──────────────────┘
└─────┬──────┘
      │  /  (embedded UI)   /api/state
      ▼
  browser kiosk, 1920x480, dark
```

Two small binaries, **Go stdlib only** (zero dependencies, `CGO_ENABLED=0`),
cross-compiled for `linux/arm64` (DGX Spark, Raspberry Pi Ubuntu),
`linux/amd64`, `windows/amd64`, and `darwin/arm64`.

- **`spark-agent`** — runs on each DGX Spark (one systemd service), samples
  every 2s and serves the latest snapshot on `/metrics`. Nothing is computed
  on the request path, so any number of dashboards can poll it.
- **`spark-dash`** — polls every node in parallel (1.5s timeout), keeps a
  10-minute history ring per metric (survives page reloads), and serves the
  embedded UI plus `/api/state`.

## Cluster setup (Ubuntu, end to end)

This walkthrough sets up two nodes, `spark-5a4e` and `spark-8a27`, with the
dashboard on one of them. Adjust names/hosts to your cluster.

### 1. Build the installers

On any machine with Go 1.22+ (the .debs are self-contained — binary, systemd
unit, and default config):

```sh
make deb        # native architecture -> dist/deb/*.deb
# or, for both arm64 and amd64:
make deb-all
```

### 2. Install the agent on every Spark node

```sh
# from the build machine — repeat for each node:
scp dist/deb/spark-agent_*_arm64.deb user@spark-8a27:/tmp/
ssh spark-8a27 sudo apt install /tmp/spark-agent_*_arm64.deb
```

That is the whole install: the service is enabled and started automatically,
listening on `:9105`. Verify on the node:

```sh
systemctl status spark-agent          # active (running)
curl -s localhost:9105/metrics        # JSON snapshot; "gpu" section populated
```

If a firewall is active, allow the agents from your LAN only:

```sh
sudo ufw allow from 192.168.86.0/24 to any port 9105 proto tcp
```

### 3. Install the dashboard

On whichever machine should drive the display (one of the nodes, a laptop,
or a Raspberry Pi — anything that can reach the agents):

```sh
scp dist/deb/spark-dash_*_arm64.deb dashhost:/tmp/
ssh dashhost sudo apt install /tmp/spark-dash_*_arm64.deb
```

It starts immediately with a default config pointing at the local agent
(`http://127.0.0.1:9105`), so a single-node setup already works at
`http://localhost:8080`.

### 4. List the cluster nodes

Edit `/etc/spark-mini-dash/config.json` on the dashboard host (conffile —
your edits survive upgrades), then restart:

```sh
sudo tee /etc/spark-mini-dash/config.json > /dev/null <<'EOF'
{
  "poll_interval_ms": 2000,
  "timeout_ms": 1500,
  "history_len": 300,
  "stale_after_s": 3,
  "offline_after_s": 15,
  "soc_label": "SoC",
  "thresholds": {
    "gpu_temp_warn_c": 80, "gpu_temp_serious_c": 90, "gpu_temp_critical_c": 100,
    "soc_temp_warn_c": 95, "soc_temp_serious_c": 105, "soc_temp_critical_c": 115,
    "mem_warn_pct": 97, "mem_crit_pct": 99
  },
  "nodes": [
    { "name": "spark-5a4e", "url": "http://spark-5a4e.lan:9105" },
    { "name": "spark-8a27", "url": "http://spark-8a27.lan:9105" }
  ]
}
EOF
sudo systemctl restart spark-dash
```

Open `http://<dashhost>:8080` — the header should show `● N Online` matching
your node count. If a `.lan`/`.local` hostname does not resolve from the
dashboard host, use the node's IP instead. (mDNS names can be checked with
`getent hosts spark-8a27.lan`.)

A node entry needs only `url` — the display name then comes from the
agent's hostname. `name`, and `color` (hex) are optional overrides.

### 5. Put it on the wall (kiosk)

```sh
# Linux:
chromium-browser --kiosk --noerrdialogs http://localhost:8080
# Windows:
msedge --kiosk http://<dashhost>:8080 --edge-kiosk-type=fullscreen --no-first-run
# Raspberry Pi Ubuntu: same as Linux
```

Optionally install the dashboard as a service on the display host too
(`deploy/spark-dash.service`), or just autostart the browser.

### 6. Updating and uninstall

```sh
sudo apt install ./spark-agent_<newer>_arm64.deb   # upgrade in place; config kept
sudo apt remove spark-agent                        # stop + remove binary
sudo apt purge spark-dash                          # also removes /etc config
```

## What the dashboard shows (per node panel)

| Element | Source | Notes |
|---|---|---|
| CPU gauge + per-core turbine ring | `/proc/stat` (delta vs previous tick) | 20 radial ticks around the gauge — angle is the core index, length and shade encode load; hover a tick for the per-core value |
| MEMORY gauge, `N / M GB` | unified pool: DGX stream or `/proc/meminfo` | `used = total − available` (reclaimable cache counted) — a healthy GB10 idles near ~96%; the value stays white on purpose and only the arc tints near exhaustion (97%/99% defaults). Swap shown in the tooltip, never folded in |
| GPU gauge (hero, center) | DGX Dashboard stream or `nvidia-smi` | The % is the headline read; GPU memory is never shown — GB10 unified memory makes every GPU-memory query report Not Supported |
| TEMP thermometer | `nvidia-smi` GPU temp + "SoC" max of the unlabeled `acpitz` zones | labeled ticks at the warn/serious/critical thresholds; mercury and value turn yellow/orange/red past them |
| POWER | `power.draw` | current draw plus the 10-minute peak |
| Recorder strip | dashboard history rings | 10-minute lanes for CPU / GPU / MEM % and GPU temp; hover for a crosshair with values at any point in the window |
| Table view | all of the above | press `T` (or open `/#table`) for the WCAG-clean twin |

**Units:** decimal GB everywhere (kiB×1024/1e9), matching NVIDIA Sync's
"125 GB of 131 GB" display. The total shows ~131 GB, not 128 — that is the
usable pool as the kernel reports it, consistently converted.

**Status:** `● LIVE` / `▲ STALE` (~3s without data) / `■ OFFLINE` (~15s),
glyph + word; stale/offline panels keep last-good values dimmed with a
"last seen" note. Staleness is judged by the dashboard's receive clock,
never the agent's, so clock skew can't flip a live node.

## Temperature tuning

GPU temp comes from `nvidia-smi` and is reliable. The "SoC" figure is the
max across `/sys/class/thermal/thermal_zone*` — on DGX Spark these are
seven **unlabeled** `acpitz` zones with no documented CPU/GPU mapping, so
the label is honest and configurable (`soc_label`). Defaults (SoC
95/105/115 °C) are reasoned guesses; observe your units under load and
tune via config. If you identify a specific zone, pin it:

```sh
spark-agent --thermal-zone 0    # report zone 0 only, instead of max
```

## Using NVIDIA's own telemetry stream (optional)

Every DGX Spark ships the closed-source `dgx-dashboard-service`
(`127.0.0.1:11000`) whose frontend consumes `GET /api/v1/gpu_telemetry/stream`
(JWT-protected — NVIDIA Sync tunnels it with its own token). The agent can
consume that same stream — the exact numbers Sync's Resource Monitor shows.
It is the default first choice (`--source auto`); without a token the agent
silently falls back to `nvidia-smi` + `/proc` (same numbers ±~0.1 GB).

To enable the stream, put the JWT in a root-owned file and point the agent
at it:

```sh
sudo install -m 0600 /dev/null /etc/spark-mini-dash/dashboard-token
sudo nano /etc/spark-mini-dash/dashboard-token      # paste the JWT
sudo systemctl edit spark-agent
#   [Service]
#   ExecStart=
#   ExecStart=/usr/bin/spark-agent --addr :9105 --interval 2s \
#       --dashboard-token-file=/etc/spark-mini-dash/dashboard-token
sudo systemctl restart spark-agent
```

**Never pass secrets as command-line flags** (`--dashboard-token`,
`--dashboard-password`): flags are world-readable via `/proc/<pid>/cmdline`
on multi-user machines — that is what the `-file` variants are for. A token
file is also how NVIDIA Sync's minted JWT can be reused. If the stream
misbehaves (auth refused, API changed by a DGX OS update), the agent logs
it once and keeps serving via nvidia-smi; check `journalctl -u spark-agent`.

## Security notes

- **Read-only by design.** Both services expose only GET endpoints serving
  telemetry; there is no way to execute or change anything over HTTP.
- **Unauthenticated on a trusted LAN.** `/metrics` and the dashboard have no
  auth; anyone who can reach the port can read hostnames and utilization.
  Scope with a firewall (see step 2) or bind to a specific interface
  (`--addr 192.168.86.35:9105`). Note agents also listen on VPN interfaces
  (e.g. Tailscale) unless scoped.
- **Least privilege.** The systemd services run unprivileged and sandboxed
  (`DynamicUser`, `ProtectSystem=strict`, no capabilities, filtered address
  families) — neither service needs root. The only exec is `nvidia-smi`
  with fixed arguments.
- **Dash↔agent traffic is plain HTTP.** An on-path host could spoof
  displayed metrics. Fine for a wall display; route agents over Tailscale
  if integrity matters to you.
- **No third-party dependencies** (Go stdlib, `CGO_ENABLED=0` static
  binaries) — no supply-chain surface.
- If GPU metrics disappear after upgrading the agent (sandbox too tight on
  your kernel/NVIDIA combination), relax via a drop-in
  (`systemctl edit spark-agent` → `[Service]` `ProtectSystem=no`) and
  `systemctl restart spark-agent`.

## Troubleshooting

| Symptom | Check |
|---|---|
| Panel shows `▲ STALE` / `■ OFFLINE` | `systemctl status spark-agent` on that node; firewall; `last_error` in `/api/state` |
| GPU gauge `n/a` | `nvidia-smi` missing/failing on the node — `journalctl -u spark-agent -f` |
| Dashboard service won't start | config JSON invalid — `journalctl -u spark-dash`; validation errors are explicit |
| `.lan` name not resolving | use the node IP in `nodes` |
| Numbers frozen, `updated` stamp old | dashboard lost its fetch loop (browser ↔ server); the page will also dim |
| Values look wrong vs `free`/`df` | memory counts reclaimable cache by design (see table above) |

## Design notes

- The agent samples on its own clock and serves a cached snapshot — pollers
  never distort the CPU delta windows; multiple dashboards can share a node.
- History rings append every tick including failures, so the recorder lanes
  show gaps, never fake zeros. History lives in the dashboard (10 min), not on
  the nodes, and survives page reloads but not dashboard restarts.
- Node colors follow the node (validated CVD-safe order, configurable
  via `node_colors`); status/temperature colors are reserved for status only.
- Agent flags: `--mock` (synthetic data for GPU-less demo hosts),
  `--thermal-zone`, `--source auto|stream|smi`, `--dashboard-token-file`,
  `--dashboard-password-file`, `--telemetry-url`, `--addr`, `--interval`.
- Dashboard flags: `--config`, `--nodes name=url,...`, `--addr`,
  `--poll-ms`, `--history-len`.
