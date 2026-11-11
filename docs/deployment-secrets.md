# Deployment & secrets

How the two binaries take their configuration, and where secrets belong so they
stay out of `argv` (visible in `systemctl status` / `ps` / `/proc`) and shell
history.

## Two mechanisms, by run context

| Context | Mechanism | Why |
| --- | --- | --- |
| **systemd service** (production) | `EnvironmentFile=` with an **absolute** path, file owned `root:root`, mode `0600` | systemd's CWD is `WorkingDirectory=` or, if unset, `/` — so a CWD-relative `.env` is unreliable here. `EnvironmentFile` is CWD-independent and auditable. |
| **interactive / dev** (`go run`, `run_operator_agent.sh`) | a `.env` file in the working directory | Convenient; keeps secrets out of shell history. |

These compose: the agent's built-in `.env` loader only sets a variable if it is
**not already set**, so when systemd injects vars via `EnvironmentFile`, a stray
`.env` in CWD never overrides them (and is a no-op if absent).

> Precedence (both binaries): explicit **flag** (where one exists) > **env var** >
> built-in default. Put secrets in the env var, not the flag.

---

## Backend — `horstreporter` (systemd)

Reads these from the environment (flags still win when passed):

| Env var | Flag equivalent | Notes |
| --- | --- | --- |
| `DX_POSTGRES_DSN` | `-dx-postgres-dsn` | Postgres DSN incl. password. Logged **masked** (`maskDSN`), with a `DSN source:` provenance line. |
| `RBN_CALLSIGN` | `-rbn-callsign` | Callsign sent to the RBN relay. Legacy fallback: `DXCLUSTER_USERNAME` (the removed DX cluster ingest's login) is still read when this is unset. |
| `QRZ_USERNAME` / `QRZ_PASSWORD` | `-qrz-username` / `-qrz-password` | QRZ.com XML login (needs an XML subscription for the grid field). Used with `-rbn-enable` to look up the locators of RBN stations that no PSKReporter / WSPR report showed. Optional: without it, RBN uses the skimmer list and the PSKReporter / WSPR callsign cache only. Answers are kept in the `callsign_locators` table. |
| `PUSH_VAPID_PRIVATE_KEY` / `PUSH_VAPID_PUBLIC_KEY` | `-push-vapid-private-key` / `-push-vapid-public-key` | Web Push signing pair; only used with `-push-enable`. Create with `scripts/generate-vapid-keys.sh` (see below). Rotating the pair invalidates every browser subscription. |

`/etc/default/horstreporter` (`root:root`, `0600`) holds both: the secrets as
env vars and the non-secret flags in `ARGS`:

```
# horstreporter args (non-secret) + secrets as env vars; mode 0600
DX_POSTGRES_DSN=postgres://dxuser:CHANGEME@localhost:5432/dxdata?sslmode=disable
RBN_CALLSIGN=YOURCALL
QRZ_USERNAME=YOURCALL
QRZ_PASSWORD=…
PUSH_VAPID_PRIVATE_KEY=…
PUSH_VAPID_PUBLIC_KEY=…
ARGS="-port 443 -domain horstreporter.kgbvax.net -pprof -compress -max-clients 150 \
  -log-file /home/hk/horst.log -log-level=INFO \
  -push-enable -push-vapid-subscriber https://horstreporter.kgbvax.net …"
```

Unit `[Service]` section (`/etc/systemd/system/horstreporter.service`):

```ini
[Service]
User=hk
Group=hk
WorkingDirectory=/opt/horstreporter
EnvironmentFile=-/etc/default/horstreporter
ExecStart=/opt/horstreporter/horstreporter-linux-x64 $ARGS
```

The VAPID pair has no generator on the host (no Go toolchain); create it locally
and stream it in over SSH so the private key is never printed:

```
scripts/generate-vapid-keys.sh | ssh root@horstreporter.kgbvax.net 'cat >> /etc/default/horstreporter'
```

`deploy.sh` ships the binary only; it does **not** touch the unit or the env
file. Edit those on the host, then `systemctl daemon-reload && systemctl restart
horstreporter`. Keep a `.bak` of the unit / env file and verify `systemctl is-active`
before walking away.

---

## Operator agent — `horstoperator-agent`

On the Windows shack PC you can run the agent with a **tray icon** so you can
see at a glance whether it's alive: green = everything healthy, **amber = rotator
OK but a configured link (backend/…) is down**, red = starting or the
rotator poll is failing. The amber state is driven by a background readiness
probe (every 30 s). Hover for a tooltip, right-click for *Open opmode UI*,
*Settings…*, *Diagnostics…*, *Log UDP traffic* (toggle), and *Quit*. When
readiness flips, a **Windows desktop toast** fires (“attention needed” with the
down links, or “ready” on recovery) — raised via the Win10/11 toast runtime, no
extra modules needed.

**Settings…** opens a small local page (`http://127.0.0.1:9955/config`, served by
the agent) where you edit station / PSTrotator settings in the browser
instead of hand-editing `.env`. *Save* writes the `.env`; *Save & Restart*
writes it and restarts the agent to apply. When installed via the scheduled task (with
`-task-name`), restart bounces the task cleanly so there's no duplicate process.
The `.env` is the single source of truth: the agent's settings all fall back to
env vars (`STATION_LOCATOR`, `PST_HOST`, `PST_PORT`, `BACKEND_URL`,
…), so the page and the file stay in sync.

**Readiness panel.** The Settings page opens with a one-click diagnostics report
(`GET /v1/diagnostics`) that probes every external dependency concurrently and
shows a green/red dot + latency per system, plus an overall **READY / NOT READY**
badge — so you can assess each link without reading logs. Checks:

| Check | Probe | Required |
| --- | --- | --- |
| PSTrotator rotator | one-shot `AZ?` UDP query | yes |
| HorstReporter backend | `GET <BACKEND_URL>/api/stats` (<500 = up) | no |
| WaveLogGate (rig) | HTTP GET callback URL (any reply = up) | no |

`ready` is true when every **required** check passes and nothing you've
**configured** is failing; optional systems you haven't set up don't count
against readiness. There's also a *Test PSTrotator connection* button that probes
the host/port currently in the form (before saving).

Build the GUI (no-console) tray exe and run it with `-tray`:

```
scripts/build-operator-agent-win64.sh        # -> dist/horstoperator-agent-windows-amd64.exe (windowsgui)
# on the Windows box:
horstoperator-agent-windows-amd64.exe -tray -station-locator JO62qm -pst-host 192.168.1.142
```

The tray is Windows-only; `-tray` is ignored (logs a warning, runs headless) on
macOS/Linux, so dev and systemd workflows are unchanged. For a console build
(visible logs when debugging the headless path) use `GUI=0 scripts/build-operator-agent-win64.sh`.

### One-time setup + remote redeploy (Windows shack PC)

1. Enable OpenSSH Server on the box (PowerShell as Admin):
   ```powershell
   Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0
   Start-Service sshd; Set-Service sshd -StartupType Automatic
   ```
2. From your dev machine, one command does everything — build, bootstrap, ship,
   start:
   ```
   HORSTOP_HOST=shack.lan ./deploy_operator_agent_win.sh
   ```
   On first run it creates `C:\horstoperator` and the `HorstOperatorAgent` logon
   task (which launches the agent with `-tray`); on every run it ends the task,
   scp's the fresh exe in, and starts it again.
3. Configure **interactively**: once logged in, open the tray → **Settings** and
   set the locator / PSTrotator host. There is nothing to seed or
   edit on disk — the agent persists what you save and the *Diagnostics* panel
   shows readiness. The agent does still read these as env vars under the hood
   (so a `.env` or systemd `EnvironmentFile` works too), but the tray is the
   intended way to configure a shack PC:

If the agent ever runs as a **systemd service** on the shack box, use the same
`EnvironmentFile` pattern instead of relying on CWD:

```ini
[Service]
WorkingDirectory=/opt/horstoperator
EnvironmentFile=/etc/horstoperator/horstoperator.env   # root:root, 0600
ExecStart=/opt/horstoperator/horstoperator-agent -listen 127.0.0.1:9955 \
  -station-locator JO62qm \
  -rig-transport waveloggate -backend-url https://horstreporter.kgbvax.net
```

(Rig/rotor settings are not secrets — flags are fine.)

---

## Rotation

Moving a secret to an `EnvironmentFile` stops *future* `argv` exposure but does
not undo past exposure (old `systemctl status` output, logs, shell history). If a
secret was ever printed, **rotate it**: change it at the source (Postgres role), update the env file, and restart the service.
