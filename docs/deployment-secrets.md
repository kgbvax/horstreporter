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
| `QRZ_USERNAME` / `QRZ_PASSWORD` | `-qrz-username` / `-qrz-password` | QRZ callsign→locator enrichment. |
| `DXCLUSTER_USERNAME` / `DXCLUSTER_PASSWORD` | `-dxcluster-username` / `-dxcluster-password` | DX cluster login. |

`/etc/horstreporter/horstreporter.env` (`root:root`, `0600`):

```
DX_POSTGRES_DSN=postgres://dxuser:CHANGEME@localhost:5432/dxdata?sslmode=disable
QRZ_USERNAME=YOURCALL
QRZ_PASSWORD=CHANGEME
DXCLUSTER_USERNAME=YOURCALL
DXCLUSTER_PASSWORD=CHANGEME
```

Unit `[Service]` section:

```ini
[Service]
EnvironmentFile=/etc/horstreporter/horstreporter.env
# ExecStart carries only NON-secret flags:
ExecStart=/opt/horstreporter/horstreporter-linux-x64 -port 443 -domain horstreporter.kgbvax.net \
  -pprof -compress -max-clients 150 -dxcluster-enable \
  -dxcluster-endpoint dx.da0bcc.de:7300 -log-file /home/hk/horst.log \
  -log-level=INFO -opmode-agent-url http://127.0.0.1:9955
```

`deploy.sh` ships the binary only; it does **not** touch the unit or the env
file. Edit those on the host, then `systemctl daemon-reload && systemctl restart
horstreporter`. Keep a `.bak` of the unit and verify `systemctl is-active`
before walking away.

---

## Operator agent — `horstoperator-agent`

Reads from the environment (loaded from a `.env` in CWD if present):

| Env var | Default | Notes |
| --- | --- | --- |
| `WAVELOG_API_KEY` | — | Wavelog **read** API key. Enables enrichment; empty = disabled. Never logged. |
| `WAVELOG_URL` | `https://log.dclnext.darc.de/index.php` | Wavelog base URL. |
| `HORSTAWARDS_URL` | — | horstawards base URL the agent calls. horstawards runs on the server, so this is the proxied endpoint: `https://horstreporter.kgbvax.net/horstawards`. Also a `-horstawards-url` flag. Enables the award "wanted" merge (`was`/`pota`); empty = disabled. Not a secret. |

`run_operator_agent.sh` runs from the repo root, so a `.env` there is picked up:

```
WAVELOG_API_KEY=xxxxxxxx
WAVELOG_URL=https://log.dclnext.darc.de/index.php
```

`.env` is git-ignored — keep it that way.

If the agent ever runs as a **systemd service** on the shack box, use the same
`EnvironmentFile` pattern instead of relying on CWD:

```ini
[Service]
WorkingDirectory=/opt/horstoperator
EnvironmentFile=/etc/horstoperator/horstoperator.env   # root:root, 0600
ExecStart=/opt/horstoperator/horstoperator-agent -listen 127.0.0.1:9955 \
  -station-lat 52.52 -station-lng 13.40 \
  -rig-transport waveloggate -backend-url https://horstreporter.kgbvax.net
```

(Rig/rotor settings are not secrets — flags are fine. Only `WAVELOG_API_KEY`
belongs in the env file.)

---

## Awards service — `horstawards`

Runs **on the server** (`kgbvax.net`), co-located with the backend and
`horstprop`, bound to `127.0.0.1:9956`; the backend reverse-proxies
`/horstawards/` to it. (The agent is the local piece — it reaches horstawards via
that proxy.) Reads from the environment (`EnvironmentFile` under systemd; a CWD
`.env` for dev). Uses the same read-only Wavelog key the agent uses — the key
simply also lives on the server here.

| Env var | Default | Notes |
| --- | --- | --- |
| `WAVELOG_API_KEY` | — | Wavelog **read** key (must have ADIF-export scope). Empty = the Wavelog ADIF source is disabled and the index stays empty. Never logged. |
| `WAVELOG_URL` | `https://log.dclnext.darc.de/index.php` | Wavelog base URL. |
| `WAVELOG_STATION_ID` | — | Station profile id. **Required for DCLNext** — `get_contacts_adif` returns HTTP 400 without it. Also `-wavelog-station-id`. Not a secret. |
| `POTA_HUNTED_CSV` | — | Path to your POTA hunted-parks CSV export. **Recommended POTA source.** Also `-pota-hunted-csv`. Not a secret. |
| `POTA_CALLSIGN` | — | Enables the optional POTA *API* source (only useful with an authenticated full-list endpoint; the public profile is counts-only). Empty = disabled. Not a secret. |
| `POTA_TOKEN` | — | Optional POTA bearer token, if a POTA endpoint requires auth. Never logged. |
| `HORSTAWARDS_DATA_DIR` | `./horstawards-data` | Snapshot store dir. Also `-data-dir`. Not a secret. |

`install_horstawards_service.sh` generates `/etc/default/horstawards`
(`hk:hk`, `0600`) holding `WAVELOG_API_KEY` + `ARGS`, and provisions a writable
`/opt/horstawards/data`. The unit's `ReadWritePaths=` is limited to that data dir.

```ini
[Service]
WorkingDirectory=/opt/horstawards
EnvironmentFile=-/etc/default/horstawards   # hk:hk, 0600 (holds WAVELOG_API_KEY)
ExecStart=/opt/horstawards/horstawards-linux-x64 $ARGS   # ARGS="-listen 127.0.0.1:9956 -data-dir /opt/horstawards/data"
```

The backend mounts the proxy via `-horstawards-url` (default
`http://127.0.0.1:9956`; the backend and horstawards are on the same host). The
**local agent** then points at the public proxy:
`HORSTAWARDS_URL=https://horstreporter.kgbvax.net/horstawards`. The public mount
exposes only `/v1/wanted` + `/v1/health`; `/v1/refresh` is server-internal. See
`docs/horstawards.md`.

---

## Rotation

Moving a secret to an `EnvironmentFile` stops *future* `argv` exposure but does
not undo past exposure (old `systemctl status` output, logs, shell history). If a
secret was ever printed, **rotate it**: change it at the source (QRZ, Postgres
role, DX cluster, Wavelog), update the env file, and restart the service.
