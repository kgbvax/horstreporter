# HorstSuite

*A family of small, focused tools for HF amateur radio operators — real-time
propagation awareness, antenna control, link scoring, and award tracking, built
to run on your own hardware.*

---

## What it is

HorstSuite is a set of cooperating programs that help you answer the questions
that matter at the rig, right now:

- **Where are my signals being heard, and which bands are open?**
- **Is this opening unusual, or just an ordinary day?**
- **Is this DX spot actually workable from my station?**
- **Is it a new one — a new entity, band, mode, state, or park?**
- **Point my antenna there.**

Each capability is a separate, self-contained program. You can run just the part
you want, or run them together for a connected workflow. There is no central
cloud service to sign up for, no telemetry, and no account. You point a tool at
your callsign or grid square and it starts working.

The suite is written in Go and ships as small single binaries with an embedded
web interface. A component runs comfortably on a Raspberry Pi, a home server, or
the shack PC, and you reach it from any browser on your network.

---

## Design principles

A few choices shape the whole suite:

- **Local-first.** The tools run on your hardware. Your station's log, your
  hunted-parks list, and your operating habits stay on your machine.
- **Small and composable.** Each program does one thing and exposes a plain HTTP
  interface. The core stays small; new capabilities are added as separate
  binaries rather than by growing one monolith.
- **Empirical before model.** Where the suite scores or judges conditions, it
  prefers real reception reports — what is actually being heard — over
  theoretical prediction, and is honest about confidence when data is thin.
- **No lock-in.** Plain web pages, standard protocols (MQTT, UDP, HTTP), and
  open formats. It sits alongside your existing logger and rotator software
  rather than replacing them.

---

## The components

### HorstReporter — live propagation viewer

The centrepiece. HorstReporter ingests live reception reports from the worldwide
**PSK Reporter** network (and, optionally, a DX cluster) and shows you current
propagation on an interactive world map.

- **Two map projections:** a classic **Mercator** world view for global context,
  and an **Azimuthal (polar)** view centred on your station for judging bearings
  at a glance.
- **Filtering that matches how you operate:** any of 14 bands from 160 m to 2 m,
  with High/Low/SSB presets; signal-strength thresholds for CW/digital or phone;
  and a rolling history window you set from 1 to 60 minutes.
- **Context overlays:** a live grey-line terminator, DXCC country colouring, and
  entity labels with an adjustable density.
- **Band Stats panel:** per-band distance-vs-SNR scatter and reports-over-time
  charts, each compared against a historical baseline, plus a colour-coded
  green/yellow/red/grey assessment with a confidence figure.
- **Hands-free scanning:** an optional band-cycle mode that rotates through your
  selected bands automatically.

*Benefit:* you can see, within seconds of opening a page, which paths are open
from your location — without transmitting and without guesswork.

### DXPulse — band × region matrix

A compact, non-geographic companion view inside HorstReporter. DXPulse lays out
**bands against world regions** in a coloured grid so you can scan the entire
spectrum in a glance.

- **Quality mode** answers *"what's open now?"* by absolute activity.
- **Anomaly mode** answers *"is today unusual?"* by comparing each band–region
  cell against its historical baseline (e.g. "×2.8 baseline" flags a genuine
  opening worth acting on).
- **Summary cards** highlight the best bands, top regions, and strongest
  band–region combinations.

*Benefit:* a fast triage view for spotting the openings that are actually out of
the ordinary, before you dig into the map.

### DXLens — baseline analytics

A visualization companion that reads HorstReporter's accumulated propagation
**baseline** and presents the longer view: band-by-hour heatmaps, region
calendars, and openings recommendations through its own web interface.

*Benefit:* helps you learn the diurnal and seasonal rhythm of each band from your
own observed data, so you know when a path *usually* opens.

### HorstProp — HF link-quality scoring

A standalone service that scores the quality of a specific path — from your home
station to a spotted DX station — as a single **0–100 value with a grade,
confidence, and a plain-language reason**. Spots to score come from your own DX
cluster client.

It scores in layers, in order of trust:

1. **Empirical** — real reception reports, via HorstReporter.
2. **MUF gate** — near-real-time ionospheric state (KC2G).
3. **Model** — a self-hosted propagation model (ITU-R P.533 / VOACAP) for paths
   with no live data.

*Benefit:* turns a wall of cluster spots into a ranked, explainable "is this
workable from *here*?" judgement, grounded in what's being heard rather than a
model alone.

### HorstOperator agent — local antenna control & operating aid

A small program that runs **on your shack computer**, alongside your existing
software. The browser talks to it directly on the local network; commands never
travel through a remote server.

- **Antenna control** through **PSTRotator** over UDP: read the current heading,
  set forward/backward/bidirectional mode, and point the rotator at a clicked
  spot or bearing.
- **Chase Queue enrichment:** for DX-cluster spots, it resolves what a station is
  (via Wavelog) and flags whether it's needed.
- **Operator conveniences on Windows:** a system-tray indicator that shows status
  at a glance (running / attention / error), a built-in settings page so you
  configure it in the browser instead of editing files, a one-click **readiness
  check** that probes every connected system (rotator, backend, Wavelog, rig
  control), and desktop notifications when something drops or recovers.

*Benefit:* a single, visible local hub for control and operating assistance that
keeps your station data on your own machine and cuts diagnostic time when
something isn't connected.

### HorstAwards — award progress, kept private

An award-progress engine that runs **in-process inside the HorstOperator agent**.
It folds your own log (via Wavelog) and your POTA hunted-parks export into an
in-memory index and marks cluster spots as **new entity (ATNO), new band, new
mode, new state (WAS), or new park (POTA)**.

Because it runs locally, **your log and hunted-parks list never leave your
machine**.

*Benefit:* the "do I need this one?" answer, on the spot, without uploading your
logbook anywhere.

---

## How the pieces fit together

```
   PSK Reporter (MQTT)          DX cluster client            KC2G / model
          │                            │                          │
          ▼                            ▼                          ▼
   ┌──────────────┐             ┌──────────────┐          (HorstProp layers 2–3)
   │ HorstReporter│──baseline──►│   DXLens     │
   │  + DXPulse   │             └──────────────┘
   │  (map + API) │
   └──────┬───────┘──empirical feed──►┌──────────────┐
          │                           │  HorstProp   │──score──► your cluster view
          │ browser UI                └──────────────┘
          ▼
   ┌──────────────────────────┐   local-only, on your shack PC
   │  HorstOperator agent      │◄──── browser talks here directly
   │   ├─ PSTRotator (UDP)     │────► your rotator
   │   └─ HorstAwards (in-proc)│────► Wavelog (your log) + POTA CSV  [stays local]
   └──────────────────────────┘
```

Every link is optional. HorstReporter is useful entirely on its own; the other
components attach to it (or to your existing tools) when you want them.

---

## At a glance

| Component | Role | Runs where | Needs |
| --- | --- | --- | --- |
| **HorstReporter** | Live propagation map + band stats | Pi / server / PC | Nothing (a callsign or grid) |
| **DXPulse** | Band × region opening matrix | with HorstReporter | — |
| **DXLens** | Long-term baseline analytics | Pi / server | HorstReporter baseline |
| **HorstProp** | Per-path link-quality score | Pi / server | HorstReporter feed; your cluster client |
| **HorstOperator** | Local antenna control + operating aid | shack PC | PSTRotator (and optionally Wavelog) |
| **HorstAwards** | "New one?" flags | inside HorstOperator | Wavelog log / POTA CSV |

---

## Who it's for

- **The casual operator** who wants a propagation map open next to the logger and
  a nudge when a new region lights up on a band worth trying.
- **The DXer and contester** who wants to know whether an opening is genuinely
  unusual, whether a spot is workable from their QTH, and whether it's a new one
  — fast.
- **The station builder** who wants to measure the real reach of an antenna by
  watching how far reception reports extend, band by band.
- **The tinkerer** who prefers software that runs on their own Pi or server,
  reads plain formats, and keeps their data local.

---

## What it asks of you

- No account, and nothing to sign up for.
- A modern browser. For control and award features, the shack computer that
  already runs your rotator and (optionally) your logger.
- A little willingness to run a small program — the binaries are self-contained,
  and the operator agent has an in-browser settings page and readiness check to
  make setup and troubleshooting straightforward.

---

## In short

HorstSuite is a connected set of small, honest tools for seeing HF propagation as
it happens, judging whether a path is worth your time, knowing when you've found
a new one, and turning the antenna toward it — all on hardware you control, with
your data staying where it belongs.

*HorstSuite is open-source software. Feedback, bug reports, and contributions are
welcome.*
