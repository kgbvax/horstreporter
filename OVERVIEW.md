# HorstReporter — Operator Overview

HorstReporter is a real-time HF propagation viewer for amateur radio operators. It pulls live reception reports from the worldwide PSK Reporter network (and optionally a DX cluster) and shows you — right now — which bands are open, how far your signals are travelling, and which directions are performing best.

No account needed. Point it at your callsign or Maidenhead locator and it starts working immediately.

---

## What You Can Do With It

### See live propagation on a world map

The main view is an interactive map that updates as new reports arrive. Every coloured square represents a Maidenhead grid where your signals have been heard (or where you are hearing others). Colour intensity reflects signal strength.

- **Mercator view** — classic world map; good for a global overview and seeing where signals are reaching.
- **Azimuthal (polar) view** — a circular map centred on your station. Great for judging bearings at a glance and seeing which parts of the world are reachable.

Switch between them with the projection selector. Both support zoom, pan, and click-on-spot for details.

### Filter by band, mode, and signal strength

Choose any combination of the 14 supported bands from 160 m through 2 m. Three quick presets get you started:

| Preset | Bands |
|--------|-------|
| **High** | 20 m and shorter wavelength |
| **Low** | 30 m and longer wavelength |
| **SSB** | Bands suitable for phone operation |

Signal strength filters let you cut the noise:
- **All** — every received report, regardless of how weak
- **≥ CW** — only reports strong enough for CW/digital work (default −15 dB)
- **≥ SSB** — only reports strong enough for phone operation (default 0 dB)

You can also type in your own threshold values if your operating style calls for it.

### Monitor a history window you control

The spot stream keeps a rolling history — anywhere from 1 to 60 minutes. A shorter window shows only the freshest activity; a longer window gives you a broader picture of which paths have been open recently.

### Cycle through bands automatically

Enable the **band cycle** feature and HorstReporter rotates through your selected bands automatically, dwelling on each one for a configurable interval (1–60 seconds). Useful when you want a hands-free scan of the whole HF spectrum.

### Overlay the grey line

A live terminator overlay shows the current sunrise/sunset boundary across the globe. Because propagation often peaks near the grey line, having it on the map helps you time your operating.

### Add geographic context

- **Country colouring** — each DXCC entity gets a distinct colour so borders are immediately visible.
- **DXCC labels** — entity names on the map with a density slider so you can tune how many labels appear before they overlap.

---

## Band Stats Panel

Click the **Stats** button to open the Band Stats panel alongside the map. It gives you a deeper look at each active band without leaving the main view.

### What you see for each band

**Distance vs. SNR chart** — a scatter plot of every reception report in your time window, plotted by distance (km) from your station and signal strength (dB). Dashed lines mark the CW and SSB viability thresholds. A quick glance tells you whether you're only working nearby stations or whether DX is genuinely open.

**Reports over time chart** — a bar chart showing how many reports arrived in each recent time slice, plus:
- An orange trend line indicating whether activity is rising or falling
- A red dashed baseline showing the typical activity level for this band, at this time of day, based on historical data

When the bars are clearly above the baseline, conditions are better than usual. When they're below it, it's a slow day.

### Overall band assessment

At the top of the panel, each band gets a colour-coded status:

| Colour | Meaning |
|--------|---------|
| **Green** | Good to excellent propagation |
| **Yellow** | Fair — usable but not exceptional |
| **Red** | Poor — limited openings |
| **Grey** | Not enough data to judge |

A confidence percentage tells you how much historical data backs up the assessment. Low confidence means the system hasn't seen enough activity yet to be sure.

---

## DXPulse — Quick Band × Region Matrix

Access DXPulse from the **DXPulse** link in the interface (or directly at `/dxpulse/`).

Instead of a geographic map, DXPulse shows a compact grid: **bands on one axis, world regions on the other**. Each cell is coloured by how much propagation is currently observed in that combination.

Supported regions include: Europe, North America, Japan, Oceania, Southeast Asia, South Asia, Middle East/Central Asia, Africa, South America, Central America/Caribbean, and the Pacific.

### Two modes

**Quality mode** answers: *"What's open right now?"*
Cells are coloured by absolute activity level. Brighter means more reports and stronger signals.

**Anomaly mode** answers: *"Is today unusual?"*
Each cell is compared against the historical baseline for this exact time of day. A cell showing "×2.8 baseline" means you're seeing nearly three times as many reports as you'd normally expect right now — a genuine opening worth acting on. "Below baseline" means that path is underperforming.

### Summary cards

Below the matrix, three summary cards give you the key takeaways:
- **Best Bands** — ranked by current activity
- **Top Regions** — which destination areas are most active and on which band
- **Hot Cells** — the single strongest band–region combinations right now

DXPulse is designed for the operator who wants to scan the whole spectrum in a few seconds and then dig into the map for details.

---

## Operator Mode (Antenna Control)

If you run the optional **HorstOperator agent** on the same computer as your station, HorstReporter can control a rotatable antenna through PSTRotator.

When operator mode is active:
- The interface shows your current antenna heading and station name.
- You can set the antenna mode: **forward**, **backward**, or **bidirectional**.
- Clicking a spot or region can be used to command the rotator to that bearing.

The agent runs locally — it communicates directly from your browser to your shack, never through a remote server.

---

## Typical Use Cases

**Before a contest or DXpedition**
Open DXPulse and set your qth locator. Check the anomaly matrix against the contest bands — see at a glance if 15 m is running unusually well to North America, or if 10 m to Japan is in an exceptional opening.

**During casual operating**
Keep HorstReporter open alongside your logging software. The map updates automatically; when a new region lights up on a band you haven't tried yet, it's a cue to spin the dial.

**Evaluating your antenna**
The distance vs. SNR chart in the Band Stats panel tells you the reach of your station on each band. Compare a new antenna against your old one by watching how far the scatter plot extends on the distance axis.

**Propagation monitoring without transmitting**
Use a locator-based qth (your grid square) to see what's being heard by reporters in your area, regardless of whether you're transmitting.

**Remote monitoring**
HorstReporter is a single web page served from a small Go binary. It runs comfortably on a Raspberry Pi or a home server, and you can access it from any browser on your local network.

---

## Getting Started

1. Open HorstReporter in your browser.
2. Type your callsign **or** your 4- or 6-character Maidenhead locator into the qth field (e.g. `DL1ABC` or `JO42`). Use the location button to auto-fill your locator from the browser's GPS.
3. Select the bands you want to watch.
4. The map starts populating within seconds as live reports arrive.

All your settings (qth, bands, projection, thresholds, time window) are saved automatically in the browser and restored on your next visit.

---

## Roadmap — Where HorstReporter Is Headed

The following improvements are planned or under consideration, roughly in priority order.

### Near-term

**Improved mobile experience**
The interface works on phones and tablets today, but the layout is optimised for desktop. A dedicated mobile layout for the DXPulse matrix and a swipe-friendly band selector would make it much more usable in the shack while standing at the rig.

**Per-band opening alerts**
A notification (browser alert or audio cue) when a band that was quiet suddenly shows activity significantly above its historical baseline — especially for bands like 10 m and 6 m that open and close quickly.

**Saved views / presets**
The ability to save named configurations (e.g. "Contest setup — 40/20/15", "6 m monitoring") and switch between them instantly, rather than manually re-selecting bands and thresholds each time.

**Bearing overlay on Mercator**
A great-circle line and distance ring from your station to a clicked point on the map, giving you the bearing to work and an estimate of propagation distance.

### Medium-term

**Cluster spot detail**
When DX cluster spots are shown, display the spotter's note and frequency alongside the grid square marker, so you can see the exact frequency of a rare DX station without leaving the map.

**Solar and geomagnetic indices overlay**
Display current solar flux (SFI), sunspot number (SSN), and K-index directly in the interface. When the K-index spikes, it's immediately visible alongside the degraded propagation on the map.

**Personalised baseline** ✅
The baseline now has three tiers: your own qth history, your **regional** baseline (operators in your DXPulse region — EU, NA, AS, …), and the global average. Scoring falls back through the tiers, so operators with thin qth history get a baseline scoped to their part of the world instead of the global average — giving more accurate "unusual opening" alerts for your specific location. Always on; no setup needed.

### Longer-term

**WSPR integration**
Add WSPR reception data as an additional propagation data source alongside PSK Reporter. WSPR runs at lower power and beacons continuously, providing a steady propagation reference even when no one is actively operating.

**Propagation forecast overlay**
Integrate predicted MUF (Maximum Usable Frequency) contours from ionospheric models, overlaid on the map, so you can see not just what's open now but what's likely to open in the next hour.

**Multi-station / club view**
Allow multiple callsigns or locators to be monitored simultaneously — useful for a club station or for comparing two antenna systems side by side on the same map.

**Logbook integration**
Link to popular logging software (N1MM, WSJT-X, Log4OM) so that worked stations are flagged on the map in real time, helping you spot new multipliers or entities you still need.

---

*HorstReporter is open-source software. Feedback, bug reports, and contributions are welcome on Codeberg: https://codeberg.org/kgbvax/horstreporter*
