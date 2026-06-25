# HorstProp Link-Qualitäts-Scoring — Domänensicht

Dieses Dokument beschreibt auf Domänenebene, *was* der **horstprop**-Score
bedeutet und *wie* er einen KW-Funkweg (HF-Pfad) bewertet. Implementierungs-
details wie Code-Struktur, Datentypen oder Speicher-Interna werden bewusst
weggelassen. Die vollständige technische Spezifikation steht in
[`horstprop.md`](./horstprop.md). Das separate **DX-Potential**-Panel in der
HorstReporter-Hauptoberfläche (ein gänzlich anderes Scoring-System) ist in
[`../static/dxscore.md`](../static/dxscore.md) beschrieben.

> **Zwei unterschiedliche Scorer, nicht verwechseln.** Das *DX-Potential*-Panel
> von HorstReporter bewertet, *welche Bänder von einem Ziel aus gerade
> allgemein vielversprechend aussehen*. Der eigenständige **horstprop**-Dienst
> beantwortet eine engere, spot-bezogene Frage: *Wie gut ist — bei dieser
> konkreten DX-Station auf dieser konkreten Frequenz — der Funkweg von meiner
> Heimstation?* Dieses Dokument behandelt ausschließlich **horstprop**.

## 1. Welche Frage der Score beantwortet

> *Eine DX-Station ist gerade im Cluster aufgetaucht. Wie gut ist von **meiner**
> Heimstation aus, auf **diesem** Band, dieser Funkweg — genau jetzt?*

Eingabe ist ein einzelner Spot (DX-Rufzeichen, Frequenz, optional ein
Locator). Ausgabe ist ein kompaktes Urteil für genau diesen einen Pfad:

- ein **Score** 0–100 (höher = besser arbeitbar),
- eine **Buchstaben-Note** A / B / C / D (oder `?`, wenn unbekannt),
- eine **Konfidenz** 0–1 und
- eine **Begründung** plus eine **Aufschlüsselung nach Schichten (Layern)**,
  die erklärt, wie das Ergebnis zustande kam.

Es ist eine **Entscheidungshilfe** für die Frage „lohnt sich dieser Spot?",
keine Garantie.

## 2. Erster Schritt: DX verorten und Geometrie aufspannen

Bevor bewertet wird, muss die Engine wissen, *wo* das DX steht:

- trägt der Spot einen Locator, wird dieser verwendet;
- andernfalls wird das Rufzeichen auf den Schwerpunkt (Centroid) seiner
  DXCC-Entität aufgelöst (über cty.dat);
- klappt beides nicht, gilt der Pfad als **nicht auflösbar** — es wird ein
  neutraler Wert von 50 mit sehr geringer Konfidenz zurückgegeben und genau das
  auch ausgewiesen, statt zu raten.

Sobald das DX verortet ist, werden **Distanz und Peilung (Bearing)** von der
Heimstation zum DX berechnet. Diese Werte teilen sich alle Scoring-Layer.

## 3. Die geschichtete Methode

horstprop bewertet, indem es bis zu drei unabhängige **Layer** an Evidenz
kombiniert. Jeder Layer darf sich *enthalten*, wenn er nichts Brauchbares
beizutragen hat. Stille wird nie als schlechter Score gewertet — ein Layer ohne
Daten tritt einfach zur Seite.

### Layer 1 — Empirisch („wird dieser Pfad gerade tatsächlich gehört?")

Die stärkste Evidenz ist tatsächlicher Empfang. Ein rollierender Speicher wird
schreibgeschützt aus dem öffentlichen HorstReporter-Stream gespeist und hält
aktuelle Empfangsmeldungen im **Interessensgebiet** der Heimstation. Trifft ein
Spot ein, sucht Layer 1 nach aktuellen Meldungen auf demselben Band, deren Pfade
**nahe der DX-Region** liegen (innerhalb einer konfigurierbaren Anzahl von
Locator-Ringen, „Grid-Square-Rings").

Existieren solche Meldungen, wird der Pfad anhand ihrer **Signalstärke**
bewertet — mit Betonung auf der besten Meldung, gedämpft durch den Median — und
anschließend **nach Alter abgewertet**: eine Meldung von vor 25 Minuten zählt
weniger als eine von vor 2 Minuten. Mehr und frischere Meldungen erhöhen die
Konfidenz.

> Hinweis: Der öffentliche Feed ist **locator-genau**, daher denkt Layer 1 in
> *Pfaden/Regionen*, nicht in einzelnen Rufzeichen. Er beantwortet die Frage „wird
> dieser Teil der Welt gerade in meiner Nähe auf diesem Band gehört?" — also genau
> die empirische Frage, auf die es ankommt.

### Layer 2 — MUF-Gate („ist das Band auf diesem Pfad physikalisch offen?")

Der zweite Layer ist eine **physikalische Plausibilitätsprüfung**, kein Score.
Mithilfe der KC2G-Echtzeit-MUF-Vorhersage werden mehrere **Kontrollpunkte**
entlang des Pfades abgetastet (der Mittelpunkt sowie Punkte etwa 1500 km von
jedem Ende entfernt), und es wird die **niedrigste** dort gefundene maximal
nutzbare Frequenz (MUF) genommen — das schwächste Glied bestimmt den Pfad.

Anschließend wird die Arbeitsfrequenz mit dieser Pfad-MUF verglichen und ein
**Gate-Multiplikator** im Bereich 0–1 erzeugt:

- deutlich unter der MUF → **offen**, Gate ≈ 1 (keine Strafe);
- nahe an der MUF → **grenzwertig**, Gate sinkt allmählich;
- über der MUF → **above_muf**, Gate fällt gegen 0 (Pfad wahrscheinlich
  geschlossen).

Das Gate wird **multiplikativ** auf den Basis-Score angewendet: Es kann einen
ansonsten vielversprechenden Pfad herunterstufen, der physikalisch zu ist, aber
es kann allein keinen guten Score erzeugen.

VHF und höher (≥ 50 MHz, z. B. 6 m / 4 m / 2 m) ist **ausgenommen** — diese
Bänder öffnen über sporadisches E, Tropo und Meteorscatter, was das
F-Schicht-MUF-Modell nicht beschreibt — dort stützt sich das Scoring stattdessen
auf empirische Evidenz.

### Layer 3 — Ausbreitungsmodell (vorbereitet, enthält sich derzeit)

Ein Modell-Layer (Punkt-zu-Punkt-Vorhersage im Stil von ITU-HFProp / VOACAP) ist
in dieselbe Mischung eingebunden, aber in diesem Binary **noch nicht gebaut**; er
enthält sich heute immer. Wäre er vorhanden, lieferte er für Pfade ohne
Live-Beobachtungen einen Basis-Score und eine Konfidenz aus einem physikalischen
Modell.

## 4. Wie die Layer gemischt werden

Die Layer werden **empirisch zuerst** kombiniert:

1. **Basis-Score**: Layer 1 (empirisch) ist die Basis, wenn vorhanden;
   andernfalls dient das Modell (Layer 3) als Rückfall-Basis; andernfalls ein
   neutraler Wert von 50.
2. **Übereinstimmungs-Anpassung**: Sind Empirik und Modell beide vorhanden und
   *stimmen überein*, steigt die Konfidenz; *widersprechen* sie sich, wird die
   Konfidenz gekürzt.
3. **Gate anwenden**: Das MUF-Gate multipliziert den Basis-Score — sein Biss wird
   jedoch **durch die empirische Stärke gemildert**. Sichere, frische
   Beobachtungen heben eine Gate-Untergrenze an, sodass eine 30 Minuten alte,
   interpolierte MUF einen Pfad nicht zerdrücken kann, der *nachweislich gehört
   wird*. Schwache oder fehlende Empirik lässt das Gate voll wirken.
4. **Konfidenz**: abgeleitet aus der jeweils dominierenden Evidenz — starke
   Empirik, ein eindeutiges (geschlossenes) Gate oder das MUF-Gate allein, wenn
   es das einzige Signal ist.

Der Leitgedanke: **Echte Beobachtungen schlagen das Modell.** Das Physik-Gate ist
eine Kontrolle gegen übertriebenen Optimismus, kein Veto gegen die Realität.

## 5. Vom Score zur Note

Der finale 0–100-Score wird zum schnellen Lesen in einen Buchstaben einsortiert:

| Note | Score | Bedeutung |
|------|-------|-----------|
| **A** | ≥ 75  | Starker Pfad — los geht's |
| **B** | 55–74 | Gut / arbeitbar |
| **C** | 35–54 | Grenzwertig |
| **D** | < 35  | Schlecht |
| **?** | —     | Unbekannt: kein Score oder Konfidenz unter 0,2 |

Eine Note wird nur vergeben, wenn die Konfidenz hoch genug ist, um etwas
auszusagen; andernfalls wird der Pfad als **unbekannt** gemeldet statt geraten.

## 6. Ein Ergebnis lesen

- **Hoher Score + hohe Konfidenz** → Pfad ist aktiv und offen; ran an die Taste.
- **Hohe Basis, aber heruntergegatet** → Band ist auf diesem Pfad physikalisch
  grenzwertig/geschlossen trotz Interesse; die Begründungszeile sagt es.
- **Hinweis auf empirisches Override** → Beobachtungen schlagen ein veraltetes
  MUF-Gate; vertraue der On-Air-Evidenz.
- **Neutrale 50 / `?`** → DX konnte nicht verortet werden, oder kein Layer hatte
  etwas; das ist ein ehrliches „weiß nicht", kein Urteil.

## 7. Bewusste Grenzen

- Der Score beurteilt **einen Pfad von einer Heimstation**, nicht die
  allgemeinen Bandbedingungen.
- Empirische Evidenz ist **regions-/pfadbezogen** (locator-basiert) und erbt die
  Abdeckungslücken des öffentlichen Feeds, die je nach Geografie, Zeit und
  aktiven Hörern schwanken.
- Das MUF-Gate ist ein **F-Schicht**-Modell und gilt bewusst nicht für VHF+.
- Konfidenz und das „Enthalten-bei-Stille"-Design führen dazu, dass der Dienst
  lieber *„unbekannt"* sagt, als eine selbstsichere Falschaussage abzugeben.
- Ein guter Score ist eine Einladung zum Hören und Rufen — die Bestätigung auf
  dem Band gewinnt immer.
