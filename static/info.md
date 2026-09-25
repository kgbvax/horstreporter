Horstreporter is a tool for HAM radio operators. Its goal is to make **current** HF band conditions easy to explore.  
It is effectively an alternate frontend for <a href="https://www.pskreporter.info" target="_blank">pskreporter</a>.  


## Getting started
1. Enter your **locator** (e.g. JO32, FN31AB) or your callsign into the top input box. You can also click the map marker icon to auto-detect your location.
2. Click **Go**. HorstReporter will connect to the live stream and fetch recent FT8/FT4 reports where you or stations in your grid are either the sender or receiver.
3. Watch the map populate! Data updates in real-time as long as the stream is running.

## Features and controls
* **Filters:** Enable or disable specific bands, or apply minimum SNR thresholds to only show spots strong enough for CW or SSB voice contacts.
* **Projection:** Switch between *Mercator* (the familiar flat world map) and *Azimuthal* (a great-circle view centered on your location — handy for reading beam headings and distances).
* **Styles:** Choose how to visualize the data:
    * *Grid:* Groups spots into Maidenhead squares. Color indicates the dominant band, opacity indicates signal strength.
    * *Active area:* Draws dynamic polygons enclosing clustered spots to show the overall propagation footprint.

* **Settings:** Below the band list, **Layers** switches map overlays and spot sources (RBN, WSPR, DX cluster, DXCC labels, country colors, forecast), **Display** sets spot age, SNR thresholds, cluster distance, auto-zoom and more, and **Notifications** manages push alerts.
* If you don't see enough data for your square, try enabling **Adj. squares** which also pulls data from the 8 squares around you.
* **Operator mode** (when running behind the local operator agent with an UltraBeam antenna): the map gains a **Beam direction** control — three buttons, *Forward* / *180°* / *Bi-dir* — that show and set the antenna's pattern live over MQTT. When the beam is left in **180°**, that button pulses an escalating red with a **REVERSE** warning; this is intentional, since a forgotten reverse beam is easy to miss.

## Map interaction
* Hover over colored map features to see detailed statistics (Min/Max/Avg SNR) and top reports for that area.
* Click anywhere on the map to quickly set a new locator and refresh the data.  

## Time travel
The **Time travel** button (under **Display**, below the spot-age slider) rewinds the map to past propagation. It uses the same locator, band filters and SNR thresholds as the live map, so a past moment looks exactly like the live moment did.

* **Scrub:** Drag the timeline to move through the past (snaps to 5-minute steps). The playhead shows a *trailing 15-minute window* — the same view the live map gives you.
* **Animate:** Press play to relive a period as a time lapse (60×/240×/600× — a full day in under 3 minutes at 600×). Spots **fade in as they happen and glow out** in data-time (≈5 min decay), so you see when activity actually occurred: a band opening floods the map, a closing band's glow dies away. Pause, and the map settles into a plain snapshot of that moment.
* **Ranges:** 1 h / 6 h / 24 h. On the production server (with its spot archive) the full 24 hours is available; without an archive only the server's in-memory history (~60 min) can be replayed.
* **Exit:** The *Live* button in the bar returns you to the live stream.
* **Share:** While in time travel the URL carries the window (`?tl=1&t0=…&t1=…`), so you can send someone the exact past view.



# Origin
Made by DL9ET (<a href="https://mastodon.radio/@dl9et" target="_blank">English Mastodon</a>, <a href="https://radiosocial.de/@dl9et" target="_blank">German Mastodon</a>, <a href="https://www.qrz.com/db/DL9ET" target="_blank"> on QRZ</a>) to ~~scratch an itch~~ discover SSB DX opportunities. 


This is based on me watching DK3JF scout the airwaves with WSJTX in Morokulien. I whipped somrthing up based on our SDR's feed which was very useful but later came to the realization that you don't need an SDR: the WSJT-X of the OM next door is good enough. So this is the current approach: using pskreporter data from your grid.

If you are intrested in this approach, have a look at <a href="https://hf.dxview.org" target="_blank">https://hf.dxview.org</a>.

As with everything ham radio, this should be considered experimental and may implode any moment.  95% of this was created with Google Gemini. Contact me for feedback or requests.  
More information may be available in my blog: <a href="https://dl9et.darc.de/tags/horstreporter/" target="_blank">https://dl9et.darc.de/tags/horstreporter/</a>

The green plushy dragon in the lower left is called "Horst-Kevin".

## Implementation note
In its core horstreporter subscribes to the pskreporter firehose *once* and does the 1:n fan-out / filtering for each client by itself. This is an explicit choice to not overload the pskreporter servers. Horstreporter also maintains a short history so that new clients don't have to wait for data to arrive from the event stream. Most visualization and interaction logic is done in the browser, while derived DX condition scoring is computed server-side and exposed via `/api/dx_conditions`.

Time travel serves its past views from the same raw-spot archive the DX baseline uses (`/api/history`, one gzipped bundle per requested window; immutable windows are cached server-side). When no archive is configured it falls back to the in-memory rolling history and reports its reach honestly.

This project is open-source under Affero GPL.

# IMPRINT + PRIVACY

Ingomar Otter DL9ET, Vortlager Damm 6, Lengerich, Germany

The server horstreporter.kgbvax.net does not process any personal data as defined by GDPR.  User preferences are remembered in the user's browser using "local storage".  
