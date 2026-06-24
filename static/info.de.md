Horstreporter ist ein Werkzeug für Funkamateure. Ziel ist es, die **aktuellen** Ausbreitungsbedingungen auf den KW-Bändern einfach erkundbar zu machen.  
Es ist im Grunde ein alternatives Frontend für <a href="https://www.pskreporter.info" target="_blank">pskreporter</a>.  


## Erste Schritte
1. Gib deinen **Locator** (z. B. JO32, FN31AB) in das Eingabefeld oben ein. Du kannst auch auf das Marker-Symbol <i class="fas fa-map-marker-alt"></i> klicken, um deinen Standort automatisch zu bestimmen.
2. Klicke auf **Go**. HorstReporter verbindet sich mit dem Live-Stream und holt die jüngsten FT8/FT4-Meldungen, bei denen du oder Stationen in deinem Grid als Sender oder Empfänger auftauchen.
3. Sieh zu, wie sich die Karte füllt! Die Daten aktualisieren sich in Echtzeit, solange der Stream läuft.

## Funktionen & Bedienung
* **Filter:** Einzelne Bänder ein- oder ausblenden oder eine Mindest-SNR-Schwelle setzen, um nur Spots zu zeigen, die für CW- oder SSB-Verbindungen stark genug sind.
* **Projektion:** Wechsle zwischen *Mercator* (die gewohnte flache Weltkarte) und *Azimuthal* (eine großkreisbezogene Ansicht zentriert auf deinen Standort — praktisch zum Ablesen von Antennenrichtungen und Entfernungen).
* **Stile:** Wähle, wie die Daten dargestellt werden:
    * *Grid:* Fasst Spots in Maidenhead-Feldern zusammen. Die Farbe zeigt das dominierende Band, die Deckkraft die Signalstärke.
    * *Active Area:* Zeichnet dynamische Polygone um geclusterte Spots, um den gesamten Ausbreitungs-„Fußabdruck" zu zeigen.

* **Band-Cycler:** Klicke auf das Play-Symbol <i class="fas fa-play"></i> unter der Bandliste, um automatisch durch die aktuell offenen Bänder zu schalten.
* **Optionen:** Klappe das Optionen-Panel auf, um maximales Spot-Alter, Cluster-Abstand, Auto-Zoom-Verhalten und mehr zu ändern.
* Wenn für dein Feld zu wenig Daten erscheinen, aktiviere **Adj. Squares** — damit werden auch die 8 umliegenden Felder einbezogen.

## Karte bedienen
* Fahre mit der Maus über eingefärbte Kartenelemente, um detaillierte Statistiken (Min/Max/Durchschnitts-SNR) und die besten Meldungen für diesen Bereich zu sehen.
* Klicke irgendwo auf die Karte, um schnell einen neuen Ziel-Locator zu setzen und die Daten zu aktualisieren.  



# Hintergrund
Gemacht von DL9ET (<a href="https://mastodon.radio/@dl9et" target="_blank">English Mastodon</a>, <a href="https://radiosocial.de/@dl9et" target="_blank">German Mastodon</a>, <a href="https://www.qrz.com/db/DL9ET" target="_blank"> auf QRZ</a>), um ~~ein Jucken zu kratzen~~ SSB-DX-Gelegenheiten zu entdecken.


Die Idee entstand, als ich DK3JF in Morokulien mit WSJT-X die Bänder absuchen sah. Zuerst habe ich etwas auf Basis unseres SDR-Feeds gebaut, was sehr nützlich war — später kam die Erkenntnis: man braucht gar kein SDR. Das WSJT-X des OM von nebenan reicht völlig. Daher der heutige Ansatz: pskreporter-Daten aus deinem Grid.

Wenn dich dieser Ansatz interessiert, schau dir <a href="https://hf.dxview.org" target="_blank">https://hf.dxview.org</a> an.

Wie alles im Amateurfunk ist auch das hier als experimentell zu betrachten und kann jederzeit implodieren. 95 % davon wurde mit Google Gemini erstellt. Für Feedback oder Wünsche gerne melden.  
Mehr Infos gibt es eventuell in meinem Blog: <a href="https://dl9et.darc.de/tags/horstreporter/" target="_blank">https://dl9et.darc.de/tags/horstreporter/</a>

Der grüne Plüschdrache unten links heißt „Horst-Kevin".

## Implementierungshinweis
Im Kern abonniert horstreporter den pskreporter-Firehose *einmal* und übernimmt das 1:n-Fan-out / Filtern für jeden Client selbst. Das ist eine bewusste Entscheidung, um die pskreporter-Server nicht zu überlasten. Horstreporter hält außerdem eine kurze Historie vor, damit neue Clients nicht erst auf Daten aus dem Event-Stream warten müssen. Die meiste Visualisierungs- und Interaktionslogik läuft im Browser, während das abgeleitete DX-Conditions-Scoring serverseitig berechnet und über `/api/dx_conditions` bereitgestellt wird.

Dieses Projekt ist Open Source unter der Affero GPL.

# IMPRESSUM + DATENSCHUTZ

Ingomar Otter DL9ET, Vortlager Damm 6, Lengerich, Deutschland

Der Server horstreporter.kgbvax.net verarbeitet keine personenbezogenen Daten im Sinne der DSGVO. Nutzereinstellungen werden im Browser des Nutzers über „Local Storage" gespeichert.  
