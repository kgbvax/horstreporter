# Typical HF openings from JO62 (Berlin area)

Researched 2026-09-28 from several web sources for use as a reference and as the verification benchmark for the per-QTH propagation almanac.

**Scope:** this covers **JO62 / central-northern Germany only**. It is not a general reference. Other QTHs will need their own sources, which is deferred to later work.

**Applicability to JO32 (the operator's actual area, roughly 52–53°N, 6–8°E).** The research anchor is Berlin (JO62, ~13.4°E), about 6° further east. For JO32:
- Local solar time runs about 25 minutes behind JO62. Windows tied to the operator's own sunrise or sunset (VK long path, 40/80 m dawn paths to NA-West, the greyline edges) arrive about 20–30 minutes later in UTC.
- Mid-day windows and windows tied to the far end's sunrise or sunset hardly move.
- The UK sources [6,11] are now only about 500 km west, so their timings apply almost directly.
- The acceptance tests below are coarse enough (multi-hour windows, percentage bands) to apply to JO32 unchanged.

**Solar context at time of writing:** SFI was 96–107 on 15–21 Sep 2026 [5] and about 109 on 12 Sep [14]. The NASA smoothed forecast for Sep 2026 is about 123 [14]. DARC's October 2026 prediction uses R12 = 75, roughly SFI 120 [1]. The high-band results below (10/12/15 m) scale with SFI.

**Source independence:** [1]–[3] are three runs of the same prediction model (DARC HF-Referat, Proppy/ITURHFPROP, 50 W CW, 500 Hz, SNR 0 dB). Count them as one independent source. In the DARC charts, "AS" means Bangkok (SE Asia), and there is no Japan chart.

## 1. Reference table

All times UTC. "Peak" is the best hour or hours.

| Band | Region | Season | Window (peak) | Confidence | Sources |
|---|---|---|---|---|---|
| 40/30 | EU | all | 24 h by day, 40 m long-skip at night | high | 1,2,3,13 |
| 20–15 | EU / Middle East | Oct/Dec | 06–18; 7–14 MHz to Middle East near 24 h | high | 1,2 |
| 20 | NA-East | Oct | 11–20 (14–17) | high | 1,4,6 |
| 20 | NA-East | Jul | 00–08 and 18–24, weak by day in the model | med, **disputed** (3 vs 13) | 3,13 |
| 15/17 | NA-East | Oct/Dec | 12–18 (13–16), 80–90% | high | 1,2,4,6 |
| 10 | NA-East | Oct–Feb | 12–17 (15) | med (depends on SFI) | 1,2,4,6 |
| 40/80 | NA-East | Oct–Dec | 20–08 (NA-sunset evening and 03–06) | high | 1,2,4,6 |
| 160 | NA | Dec–Feb | 02–06 (04–05) | med | 4 |
| 40/80 | NA-West | Oct–Dec | 00–10 (05–08, DL sunrise) | high | 1,2,6 |
| 20–15 | NA-West | Oct–Dec | 15–18 (16), short window | med | 1,2,6 |
| 20 | NA-West | Mar | from ~12 (VE7 at 1223) | low | 6 |
| 15/20 | CAR | Mar | 11–23 | low–med | 6 |
| 40 | CAR | Mar | 22–06 | low–med | 6 |
| 40/30 | SA-East (Rio) | Oct/Dec | 00–08 (03–07) | high | 1,2 |
| 15/10 | SA-East | Oct/Dec | 10–18 (12–15), 50–70% | med | 1,2 |
| 20/17 | SA-East | Oct/Dec | 18–24 | high | 1,2 |
| any | SA | Jul | 19–06 only; closed by day | med | 3 |
| 30/40 | SA-West (Lima) | Oct/Dec | ~08 greyline (60–80%), otherwise weak | low | 1,2 |
| 20/30/40 | AF (ZS) | Oct/Dec | 15–24 (19–22), 80–90% | high | 1,2,6 |
| 15/10 | AF | Oct/Dec | 08–16; 30–40% in Oct to 50–70% in Dec | med | 1,2,6,11 |
| 20–15 | AF | Jul | 12–20 (16–18), ~60% | med | 3 |
| 20/17 | AS (SE Asia) | Oct | 08–20 (13–17) | high | 1,10 |
| 20–15 | AS | Dec | 08–18 (11–14) | med | 2,10 |
| 20 | AS | Jul | 16–22 (19) | med | 3 |
| 20 | JA / East Asia | Feb/Oct | ~06/07–14, morning best | med–low | 10,13 |
| 10 | JA | summer | ~08–09 (single report) | low | 9 |
| 40 | JA | Dec–Jan | hours before European midday | low (unsourced) | 12 |
| 20 | VK short path | Oct | 10–18 (15–17) | high | 1,6 |
| 20 | VK short path | Dec | 11–17 (12–16) | med | 2 |
| 20 | VK short path | Jul | 17–22 (20–21) | med | 3 |
| 15/10 | VK short path | Oct–Mar | 07–13 (08–10) | med | 1,6,9 |
| 40–20 | VK long path | Oct | 07–09 (08) | high | 1,7,8 |
| 40–20 | VK long path | Dec | 08–10 (09) | med | 2 |
| 40–20 | VK long path | Mar–Apr | 04–08 | med | 6,7 |
| 40–20 | VK long path | Jul | 00–07 (05) | med | 3,8 |
| 40 | ZL/VK short path | Mar | ~17–19 around DL sunset | low | 6 |
| 40/30 | KH6 | Oct/Dec | 06–09, 50–70% | low–med | 1,2 |
| 20/17 | KH6 | Oct/Dec | 16–18 | low–med | 1,2 |
| any | KH6 | Jul | ≤20% | med | 3 |
| 40–20 | AN (Neumayer) | Oct/Dec | 17–24 and 00–04 (model) | low | 1,2 |
| any | AN | Jul | negligible | med | 3 |
| 6 m Es | EU | May–Aug (peak around 21 Jun), minor Dec | single hop 500–2,500 km; roughly 13–20 is statistically most active | med | 15 |

**AN:** the model shows good paths, but there are only a handful of real stations. Expect near-zero spot counts and treat AN as negligible in empirical data.

**NA-East in July:** the DARC model shows a weak daytime path, but DARC's own bulletins [13] say 20 m is "almost always open at night" in summer. FT8 decodes far below the model's 0 dB/500 Hz threshold, so real summer daytime openings will be more frequent than the model says.

## 2. Rules of thumb

- **20 m to NA-East:** at the equinox it opens around 11–12z and is reliable until about 20z. The FT8 tail runs to about 22z. RBN activity peaks at 19z [1,4,6,13].
- **40/80 m to NA-East** is a whole-night path, from NA sunset (about 21–23z) to DL sunrise. **NA-West** is dawn-weighted, 04–08z. RBN peaks at 05–06z on 40 m and 03–04z on 80 m [1,2,4,6].
- **10 m to SA/AF** is a midday path, 10–16z, not an afternoon one. At SFI about 100–120 it is marginal, and it needs SFI ≳ 130 to be routine [1,2,6].
- **In the evening, AF moves down to 20/30/40 m** (17–23z). This is the most reliable DX path from DL [1,2,6].
- **JA on 20 m** is roughly 06–14z in winter and at the equinox, with the morning best. It is a polar path and sensitive to Kp. It is only weakly sourced, so verify it empirically [10,13].
- **VK/ZL long path** follows DL sunrise, not a fixed clock time: 08–10z in December, 07–09z in October, 04–08z in March/April, 00–07z in July [1,2,3,6,7].
- **VK/ZL short path on 20 m** is 12–17z in winter, 15–18z in October and 17–22z in summer [1,2,3,6].
- **In summer, long-haul paths (SA, NA, AS) move into the DL night.** Daytime DX is poor [3].
- **Paths that cross the equator (AF, SA) survive geomagnetic storms.** Polar paths (JA, NA-West, KH6) fail first [13].

## 3. Acceptance tests for the almanac

These are expected results for "opened N of 30 days" from JO62 in late Sep/Oct 2026:

1. 20 m NA-East, 13–18z: usually open, ≥80%.
2. 15 m NA-East, 13–16z: usually open, ≥60%.
3. 40 m NA-East, 00–05z: usually open, ≥70%.
4. 20 m AF, 18–21z: usually open, ≥85%.
5. 40 m or 30 m SA, 01–05z: usually open, ≥70%.
6. 20 m AS (India, SE Asia or Middle East), 11–16z: usually open, ≥80%.
7. 20 m OC (VK), 14–18z: usually open, ≥50%.
8. 20 m NA-West, 08–13z: rarely, ≤20%. The same for 10 m NA-West at any hour.
9. 10 m NA-East, 13–16z: sometimes, 30–70%. This should track SFI, and it is the most sensitive check of solar dependence.
10. 20 m JA, 06–10z: usually open, ≥50%, but with medium confidence. Check a negative result before treating it as a bug.
11. KH6 on 15 m or 10 m, any hour: rarely, ≤20%.
12. 160 m to any non-EU region in late September: rarely. This should rise in Nov–Jan (02–06z).
13. AN, any band: rarely, because there are so few stations. Exclude from pass/fail.

## 4. Caveats

- **Solar cycle:** judge the 10/12 m tests against the actual daily SFI.
- **Model vs FT8:** the DARC model assumes 50 W CW at SNR 0 dB/500 Hz. FT8 decodes to about −20 dB/2.5 kHz, so real openings are longer and more frequent than the model's reliability figures, especially at the edges of a window and in summer daytime. RBN and contest data [4] are skewed toward big stations and contest weekends.
- **Observer bias:** spot counts follow where operators are (NA-East vs NA-West, few in KH6 and AN). No spots does not mean the band is closed.
- **Time zones:** all times are UTC. DL local time is UTC+2 (CEST) until 25 Oct 2026 and UTC+1 (CET) after that. Greyline windows move in UTC with the season, not with the clock change.
- **Geomagnetic activity:** Kp ≥ 5 hits polar paths hardest (JA, NA-West, KH6).
- **Distance offset:** the UK sources [6,11] are about 900 km west of JO62. At JO62, morning windows arrive roughly 20–40 minutes earlier and evening windows close slightly earlier.
- **Point vs region:** the model uses single cities (NY, SF, Rio, Lima, Bangkok, Sydney), while the app uses region boxes.

## 5. Sources

1. [DARC HF-Referat, Ausbreitungsvorhersage für 10/2026 (R12 = 75)](https://www.darc.de/nachrichten/meldungen/aktuelles-details/news/ausbreitungsvorhersage-fuer-102026)
2. [DARC, Propagation Planner Dezember 2025 (R12 = 109)](https://www.darc.de/nachrichten/meldungen/aktuelles-details/news/propagation-planner-dezember-2025)
3. [DARC, Ausbreitungsvorhersage Juli 2026 (R12 = 87)](https://www.darc.de/nachrichten/meldungen/aktuelles-details/news/ausbreitungsvorhersage-juli-2026)
4. [Ham-Stats (IONIS, KI7MT), ARRL DX CW 2026 RBN hourly profile, SFI ~110](https://ham-stats.com/contests/arrl-dx-cw-2026/)
5. [Ham-Stats current solar page (NOAA SFI, 15–21 Sep 2026)](https://ham-stats.com/solar/current/)
6. [G3XTT, "The Basics of HF Propagation", PW May 2014: hour-by-hour UK contest log, March, SFI ~140](https://www.veron.nl/wp-content/uploads/2018/08/HF-Propagation-PW.pdf)
7. SOTA Reflector threads on VK/ZL long path (seen via search results): [20 m to VK/ZL](https://reflector.sota.org.uk/t/20m-to-vk-zl-to-eu-open-daily-to-qrp-give-20m-a-go-part-1/28797?page=5), [40 m long path at 08:35z](https://reflector.sota.org.uk/t/vk-zl-long-path-on-40m-at-8h35z/22271)
8. [VK5FO, working the EU long path on 20 m (seen via search results)](https://vk5fo.com/345/working-the-eu-long-path-on-20m)
9. [EI7GL, Japan heard on 28 MHz (seen via search results)](https://ei7gl.blogspot.com/2018/08/japan-heard-on-28-mhz-sun-5th-aug-2018.html)
10. [DR2W VOACAPL SNR maps, 20 m, East Asia, Feb 2026](https://propagation.dr2w.de/dxprop.php?area=asia&band=20M&mode=1&time=utc)
11. [RSGB GB2RS Propagation News, 15 Feb 2026 (seen via search results)](https://rsgb.org/main/blog/news/gb2rs/propagation-news/2026/02/13/propagation-news-15-february-2026/)
12. [Wikipedia, 40-meter band (JA window marked "citation needed")](https://en.wikipedia.org/wiki/40-meter_band)
13. [DARC Deutschland-Rundspruch / DF5JL Funkwetter (seen via search results)](https://www.fading.de/funkwetter/das-aktuelle-funkwetter)
14. [NASA Feb 2026 F10.7 forecast table (seen via search results)](https://www.nasa.gov/wp-content/uploads/2026/02/feb2026f10-prd.txt)
15. [DXRadar sporadic-E season guide (secondary, seen via search results)](https://dxradar.com/blog/sporadic-e-season)

"Seen via search results" means the page was not fetched in full, and the claim rests on the search snippet.
