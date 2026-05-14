Horstreporter's goal is to make **current** band-openings easy to explore.  
It is effectively an alternate frontend for <a href="https://www.pskreporter.info" target="_blank">pskreporter</a>. 
## Getting Started
1. Enter your **Locator** (e.g., JO32, FN31AB) or **Callsign** (e.g., W1AW) into the top input box. You can also click the map marker icon <i class="fas fa-map-marker-alt"></i> to auto-detect your location.
2. Click **Go**. HorstReporter will connect to the live stream and fetch recent FT8/FT4 reports where you or stations in your grid are either the sender or receiver.
3. Watch the map populate! Data updates in real-time as long as the stream is running.

## Features & Controls
* **Filters:** Enable or disable specific bands, or apply minimum SNR thresholds to only show spots strong enough for CW or SSB voice contacts.
* **Styles:** Choose how to visualize the data:
    * *Grid:* Groups spots into Maidenhead squares. Color indicates the dominant band, opacity indicates signal strength.
    * *Active Area:* Draws dynamic polygons enclosing clustered spots to show the overall propagation footprint.
    * *Heatmap:* Shows an intensity heatmap of activity.

* **Band Cycler:** Click the play icon <i class="fas fa-play"></i> under the band list to automatically cycle through currently open bands.
* **Options:** Expand the Options panel to change maximum spot age, cluster grouping distance, auto-zoom behavior, and more.
* If you don't see enough data for your square, try enabling "Include adjacent squares" which will also pull data from the 8 squares around you.

## Map Interaction
* Hover over colored map features to see detailed statistics (Min/Max/Avg SNR) and top reports for that area.
* Click anywhere on the map to quickly set a new target locator and refresh the data.  


# BACKSTORY
Made by DL9ET (<a href="https://mastodon.radio/@dl9et" target="_blank">English Mastodon</a>, <a href="https://radiosocial.de/@dl9et" target="_blank">German Mastodon</a>, <a href="https://www.qrz.com/db/DL9ET" target="_blank"> on QRZ</a>) to ~~scratch an itch~~ discover SSB DX opportunities. 


This is based on me watching DK3JF scout the airwaves with WSJTX in Morokulien. I whipped somrthing up based on our SDR's feed which was very useful but later came to the realization that you don't need an SDR: the WSJT-X of the OM next door is good enough. So this is the current approach: using pskreporter data from your grid.

If you are intrested in this approach, have a look at <a href="https://hf.dxview.org" target="_blank">https://hf.dxview.org</a> which follows the same approach (just more refined).

As with everything ham radio, this should be considered experimental and may implode any moment.  95% of this was created with Google Gemini.
Contact DL9ET for feedback or requests.  
More information may be available in my blog: <a href="https://dl9et.darc.de/tags/horstreporter/" target="_blank">https://dl9et.darc.de/tags/horstreporter/</a>

The green plushy dragon in the lower left is called "Horst-Kevin".

## Implementation note
In it's core horstreporter subscribes to the pskreporter firehose *once* and does the 1:n fan-out / filtering for each client by itself. This is a explicit choice to not overload the pskreporter servers. Horstreporter also maintains a short history so that new clients don't have to wait for data to arrive from the event stream. Everything else is done in the browser.

If this should become more than a short experiment, I will release it as  open-source. 

# IMPRINT + PRIVACY

Ingomar Otter DL9ET, Vortlager Damm 6, Lengerich, Germany

The server horstreporter.kgbvax.net does not process any personal data as defined by GDPR.  User preferences are remembered in the user's browser using "local storage".  
