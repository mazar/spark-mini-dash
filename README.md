# spark-mini-dash

A compact wall dashboard for a cluster of NVIDIA DGX Spark machines: CPU,
GPU, and unified-memory utilization plus temperatures and power, laid out
like the Resource Monitor built into [NVIDIA Sync](https://docs.nvidia.com/sync/latest/resource-monitor.html)
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

## What it shows (per node row)

| Column | Source | Notes |
|---|---|---|
| CPU % + per-core heatmap | `/proc/stat` (delta vs previous tick) | 20 cells, hue intensity by load |
| MEMORY %, `N GB of M GB` | unified pool: DGX stream or `/proc/meminfo` | `used = total − available` (reclaimable cache counted); swap shown as a secondary readout, never folded in |
| GPU % | DGX Dashboard stream or `nvidia-smi` | GPU memory is never shown — GB10 unified memory makes every GPU-memory query report Not Supported |
| TEMP | `nvidia-smi` GPU temp + "SoC" max of the unlabeled `acpitz` zones | severity colors above thresholds (config) |
| POWER | `power.draw` | informational |

**Units:** decimal GB everywhere (kiB×1024/1e9), matching NVIDIA Sync's
"125 GB of 131 GB" display. The total shows ~131 GB, not 128 — that is the
usable pool as the kernel reports it, consistently converted.

**Status:** `● LIVE` / `▲ STALE` / `■ OFFLINE` (glyph + word; stale/offline
rows keep their last-good values dimmed). Staleness is judged by the
dashboard's receive clock, never the agent's, so clock skew can't flip a
live node.

### About the DGX Dashboard stream source

Every DGX Spark ships the closed-source `dgx-dashboard-service`
(`127.0.0.1:11000`) whose frontend consumes `GET /api/v1/gpu_telemetry/stream`
(JWT-protected — NVIDIA Sync tunnels it with its own token). The agent can
consume that same stream (`--source stream|auto`): it is exactly the data
Sync's Resource Monitor shows. Because the API is undocumented and may
change across DGX OS updates, the default `--source auto` falls back to
`nvidia-smi` + `/proc` whenever the stream is unavailable or refuses auth,
so the dashboard keeps working either way.

On a machine without an NVIDIA GPU (or anywhere else), `spark-agent --mock`
serves synthetic values for demos.

## Build

```sh
make test          # unit tests (cpu delta math, parsers, poller, rings)
make build         # native binaries in dist/native/
make build-all     # linux/arm64, linux/amd64, windows/amd64, darwin/arm64
make deb           # self-contained Ubuntu .deb installers (native arch)
make deb-all       # .deb installers for arm64 + amd64
make dist          # build-all + tarballs with deploy/ files
```

## Install on Ubuntu (recommended: .deb installers)

Each component ships as a **single self-contained .deb**: binary, systemd
unit, and (for spark-dash) a default config. Copy the one file over and
install it — the service is enabled and started automatically.

```sh
# on the build machine:
make deb           # -> dist/deb/spark-agent_<ver>_arm64.deb, spark-dash_...deb

# install the agent on each DGX Spark node:
scp dist/deb/spark-agent_*_arm64.deb user@node:/tmp/
ssh node sudo apt install /tmp/spark-agent_*_arm64.deb

# install the dashboard anywhere (a node, a laptop, a Pi):
scp dist/deb/spark-dash_*_arm64.deb dashhost:/tmp/
ssh dashhost sudo apt install /tmp/spark-dash_*_arm64.deb
```

That's the whole install. Verify with `systemctl status spark-agent` and
open `http://<dashhost>:8080`. Notes:

- The packaged spark-dash config lives at `/etc/spark-mini-dash/config.json`
  (marked as a conffile, so upgrades keep your edits). It defaults to the
  local agent; add your nodes and `sudo systemctl restart spark-dash`:
  `{"nodes":[{"name":"spark-5a4e","url":"http://10.0.0.11:9105"},
             {"name":"spark-8a27","url":"http://10.0.0.12:9105"}]}`
- To pass agent flags (e.g. `--dashboard-token`, `--thermal-zone`), edit
  `ExecStart=` in `/lib/systemd/system/spark-agent.service`, then
  `sudo systemctl daemon-reload && sudo systemctl restart spark-agent`.
- Uninstall: `sudo apt remove spark-agent` (stops the service);
  `sudo apt purge spark-dash` also removes the config.
- If a firewall is active: `sudo ufw allow 9105/tcp` on nodes (and 8080 for
  the dashboard).

## Quickstart (on a DGX Spark, no install)

```sh
make run-local     # agent on :9105 + dashboard on :8080, then browse
                   # http://localhost:8080
```

## Manual install (no dpkg, any Linux)

```sh
scp dist/linux-arm64/spark-agent user@node:
ssh node 'sudo install -m0755 spark-agent /usr/local/bin/
          sudo cp deploy/spark-agent.service /etc/systemd/system/
          sudo systemctl daemon-reload && sudo systemctl enable --now spark-agent
          sudo ufw allow 9105/tcp'   # or your firewall equivalent
```

Point the dashboard at the nodes (flags or `--config` JSON — see
`deploy/config.example.json`):

```sh
spark-dash --nodes spark-5a4e=http://10.0.0.11:9105,spark-8a27=http://10.0.0.12:9105
```

## Wall display (kiosk)

Linux (X11): `chromium-browser --kiosk --noerrdialogs http://localhost:8080`
Windows: `msedge --kiosk http://<dash-host>:8080 --edge-kiosk-type=fullscreen --no-first-run`
Raspberry Pi Ubuntu: same as Linux (Wayland: use a maximized window rule if
`--kiosk` doesn't size correctly).

The dashboard host itself can be any machine that can reach the agents:
the display laptop, a Pi, or one of the Spark nodes.

## Design notes

- The agent samples on its own clock and serves a cached snapshot — pollers
  never distort the CPU delta windows; multiple dashboards can share a node.
- History rings append every tick including failures, so sparklines show
  gaps, never fake zeros. History lives in the dashboard (10 min), not on
  the nodes, and survives page reloads but not dashboard restarts.
- GPU temperature comes from `nvidia-smi`; the "SoC" figure is the max of
  the unlabeled `acpitz` thermal zones (NVIDIA documents no CPU/GPU mapping)
  and the label is configurable (`soc_label`) plus a zone pin
  (`--thermal-zone`) if you identify yours.
- Node row colors follow the node (validated CVD-safe order, configurable
  via `node_colors`); status/temperature colors are reserved for status only.
