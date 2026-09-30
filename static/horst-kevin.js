// Horst-Kevin — the little green dragon who heckles you onto the air.
//
// Three behaviours, all driven by polling /api/hot_bands (the same "is it
// worth turning on the radio" oracle the hot-band indicator uses):
//
//   1. Dare ladder    — when a band is worth it, Horst-Kevin nudges. The
//                        longer you ignore a *sustained* opening, the harder
//                        he escalates (rungs 0..2; the rung-3 "gone" lines are
//                        only ever spoken once the opening has actually faded).
//   2. The grudge      — openings you never answered are remembered (localStorage)
//                        so he can hold them against you, repeat offenders first.
//   3. Eating his words — every dare is graded: did the opening actually persist?
//                        A public banger/lie scoreboard with the duds on display.
//
// The poll → speech logic is a pure reducer (`reduce`) so it can be tested; the
// DOM, timers and localStorage live in `initHorstKevin`. Every timing knob sits
// in `TUNING`. Kevin asks /api/hot_bands for his own dare set (bands=, max=)
// and for `holding[]` on the bands he is tracking (include=), so a band that
// merely stopped *rising* is not mistaken for a band that closed. He never
// sends current_band: the band you are on must stay visible so answering a dare
// counts as answered instead of as a fade.

import { bandColors } from './utils.js';

export const TUNING = {
    POLL_MS: 30_000,
    // With desktop nags on (and permission granted) he keeps polling, slower,
    // while the tab is hidden; otherwise a hidden tab stops polling.
    HIDDEN_POLL_MS: 60_000,
    STALE_TIMEOUT_MS: 2 * 60_000,
    // A poll gap longer than this (tab hidden, laptop asleep, outage) triggers a
    // quiet resync. Wider than 2 polls so the 60 s hidden cadence plus browser
    // timer throttling does not resync on every hidden tick.
    RESYNC_GAP_MS: 150_000,
    FADE_GRACE_MS: 5 * 60_000,       // a band gone this long (from lastSeenAt) has faded
    EPISODE_MERGE_MS: 10 * 60_000,   // back within this after a fade → same episode
    GRUDGE_MIN_RUNG: 1,
    GRUDGE_MIN_OBSERVED_MS: 5 * 60_000,
    VERDICT_BANGER_SHARE: 0.6,       // share of window polls the band must be alive
    VERDICT_MIN_COVERAGE: 0.4,       // polls seen / polls expected, else void
    VERDICT_STALE_POLLS: 3,          // pending this long past its window → void
    // Between two spoken lines, except a rung escalation of the band that spoke
    // last: that one is paced by its own profile's rung timings, so the fast
    // ladder really runs 0 / 2 / 5 min instead of 0 / 5 / 10.
    GLOBAL_MIN_GAP_MS: 5 * 60_000,
    // A poll closer than this to the last counted sighting (app.js refresh() on a
    // band click, stream restart, surroundings toggle) does not count toward the
    // debounce or the verdict coverage; timed facts still update.
    MIN_SIGHTING_GAP_MS: 24_000,
    DAILY_DARE_CAP: 6,               // new spoken episodes per UTC day (survives reloads)
    DAILY_SURPRISE_CAP: 10,          // …a `surprise` may still go up to this
    HOLD_MIN_SPM: 0.3,               // holding[] below max(this, share×peak) = faded
    HOLD_PEAK_SHARE: 0.4,
    ALMANAC_REFRESH_MS: 30 * 60_000,
    MAX_RECS: 10,
    SIX_METRE_MONTHS: [5, 6, 7, 8],  // UTC months 6m joins the dare set (Es season)
    // Per-band profiles: debounce polls before the first dare, rung timings in
    // observed ms (rung 0 at confirmation), verdict window, cooldown after an
    // episode that spoke has ended.
    PROFILES: {
        fast: { bands: ['12m', '10m', '6m'], confirmPolls: 2, rungsMs: [0, 2 * 60_000, 5 * 60_000], verdictMs: 5 * 60_000, cooldownMs: 30 * 60_000 },
        mid: { bands: ['20m', '17m', '15m'], confirmPolls: 3, rungsMs: [0, 8 * 60_000, 20 * 60_000], verdictMs: 10 * 60_000, cooldownMs: 90 * 60_000 },
        low: { bands: ['80m', '40m', '30m'], confirmPolls: 3, rungsMs: [0, 12 * 60_000, 30 * 60_000], verdictMs: 10 * 60_000, cooldownMs: 90 * 60_000 },
    },
};

const MAX_LIVE_RUNG = 2;
const GRUDGE_WINDOW_MS = 7 * 24 * 60 * 60_000; // repeat-offender lookback
const MAX_GRUDGES = 60;
const MAX_CALLS = 60;
const BUBBLE_HIDE_MS = 14_000;

// Horst-Kevin only comments on HF 80m to 10m (60m excluded: channelised, few
// FT8 reports), plus 6m in the Sporadic-E season. The backend also recommends
// 160m and VHF/UHF bands, which he must not dare anyone onto.
const BASE_DARE_BANDS = ['80m', '40m', '30m', '20m', '17m', '15m', '12m', '10m'];

function norm(band) {
    return String(band || '').trim().toLowerCase();
}

export function dareBandsAt(now = Date.now()) {
    const bands = new Set(BASE_DARE_BANDS);
    if (TUNING.SIX_METRE_MONTHS.includes(new Date(now).getUTCMonth() + 1)) bands.add('6m');
    return bands;
}

export function isDareBand(band, now = Date.now()) {
    return dareBandsAt(now).has(norm(band));
}

export function bandProfile(band) {
    const b = norm(band);
    for (const p of Object.values(TUNING.PROFILES)) {
        if (p.bands.includes(b)) return p;
    }
    return TUNING.PROFILES.mid;
}

function desiredRung(band, observedMs) {
    const rungs = bandProfile(band).rungsMs;
    let desired = 0;
    for (let i = rungs.length - 1; i >= 0; i--) {
        if (observedMs >= rungs[i]) { desired = i; break; }
    }
    return Math.min(desired, MAX_LIVE_RUNG);
}

// A holding[] entry still counts as open unless the band went red/grey or its
// rate dropped well under the episode's peak.
export function isHoldingOpen(entry, peakRate = 0) {
    if (!entry) return false;
    const status = String(entry.status || '').toLowerCase();
    if (status === 'red' || status === 'grey') return false;
    const spm = Number(entry.spots_per_minute) || 0;
    return spm >= Math.max(TUNING.HOLD_MIN_SPM, TUNING.HOLD_PEAK_SHARE * (Number(peakRate) || 0));
}

// ── almanac gate ──────────────────────────────────────────────────────────
//
// A `rising` / `dx_surge` on a band that is usually open at this UTC slot from
// this area is no news; Kevin stays silent about it (the indicator pill still
// shows it). Reads GET /api/almanac (no min_snr: `share` is an SNR share, not
// the open rate). Unknown cells (m < m_min) and a missing almanac never gate.

export function almanacSlot(now, slotMinutes = 30) {
    const d = new Date(now);
    const sm = Number(slotMinutes) > 0 ? Number(slotMinutes) : 30;
    return Math.floor((d.getUTCHours() * 60 + d.getUTCMinutes()) / sm);
}

// Best n/m over the band's lanes at `slot`, known cells only; null if none.
export function usualShareNow(almanac, band, slot) {
    if (!almanac || !Array.isArray(almanac.lanes)) return null;
    const b = norm(band);
    const mMin = Math.max(1, Number(almanac.m_min) || 0);
    let best = null;
    for (const lane of almanac.lanes) {
        if (norm(lane?.band) !== b) continue;
        const m = Number(lane.m?.[slot]);
        const n = Number(lane.n?.[slot]);
        if (!Number.isFinite(m) || !Number.isFinite(n) || m < mMin) continue;
        const share = n / m;
        if (best === null || share > best) best = share;
    }
    return best;
}

function usuallyShare(almanac) {
    const v = Number(almanac?.usually_share);
    return Number.isFinite(v) && v > 0 ? v : 0.5;
}

export function isUsualNow(almanac, band, slot) {
    const usual = usualShareNow(almanac, band, slot);
    return usual !== null && usual >= usuallyShare(almanac);
}

// ── persistence ───────────────────────────────────────────────────────────

// Demo mode drives the engine with synthetic openings; it must never write to
// the operator's real grudge/confession history.
let demoMode = false;

const LS = {
    calls: 'hk:calls',
    grudges: 'hk:grudges',
    meanness: 'hk:meanness',
    lang: 'hk:lang',
    desktop: 'hk:desktop',
};

function loadJSON(key, fallback) {
    try {
        const raw = localStorage.getItem(key);
        if (raw == null) return fallback;
        const v = JSON.parse(raw);
        return Array.isArray(fallback) ? (Array.isArray(v) ? v : fallback) : v;
    } catch {
        return fallback;
    }
}

function saveJSON(key, value) {
    if (demoMode) return;
    try {
        localStorage.setItem(key, JSON.stringify(value));
    } catch {
        /* private mode / quota — Horst-Kevin forgets, life goes on */
    }
}

// ── the dragon's vocabulary (EN + DE) ───────────────────────────────────────
//
// Lines are picked by language ('en' | 'de'), meanness ('soft'|'buzzed'|'drill')
// and rung (0..3). Each leaf is (B) => [variants]; `B` is the band label (e.g.
// "20m"). Sharp, never cruel. Horst-Kevin is a German dragon, so he's fluent in
// both.

const LINES = {
    en: {
        dare: {
            soft: [
                (B) => [`hey, ${B} looks promising — might be worth a listen.`, `${B}'s perking up. whenever you fancy it.`, `psst — ${B}'s starting to move.`, `little opening on ${B}, if you're curious.`, `${B}'s warming up nicely. no pressure.`, `something gentle stirring on ${B}.`, `${B} just winked at you. rude not to look.`, `quiet little lift on ${B} — your call.`],
                (B) => [`${B}'s holding up nicely. no rush.`, `still going on ${B}. the rig's right there if you want it.`, `${B} keeps looking good. just a thought.`, `nice steady ${B} here. up to you.`, `${B}'s being very patient with you.`, `still a lovely little run on ${B}.`, `${B} hasn't given up on you yet.`, `${B}'s still on. take your time, maestro.`],
                (B) => [`${B}'s still good from your grid! shame to miss it.`, `${B} keeps on giving. just saying.`, `${B}'s been lovely for a while now.`, `honestly ${B} from your spot is a treat right now.`, `${B}'s practically gift-wrapped for you.`, `you could work the world on ${B} right now, you know.`, `${B}'s been open so long it's getting comfortable.`, `it'd be a kindness to ${B} to actually answer it.`],
                (B) => [`${B} had a lovely run. catch the next one!`, `that was a nice ${B} opening. there'll be others.`, `${B} faded, but no worries — they come back.`, `you missed a sweet ${B} window. happens to everyone.`, `${B}'s tucked itself in. sleep well, little band.`, `ah well. ${B} will forgive you. probably.`, `the ${B} curtain's down. lovely while it lasted.`, `${B}'s gone quiet — we'll always have the sparkline.`],
            ],
            buzzed: [
                (B) => [`${B}'s awake. which is more than I can say for you.`, `oh look, ${B}'s doing something. unlike SOME of us.`, `${B} just lit up. quick, ruin it by ignoring it.`, `something's stirring on ${B}. don't all rush at once.`, `${B}'s got a pulse. you got an excuse?`, `ladies and gentlemen, ${B} has entered the building. exit: you.`, `${B}'s opening. this is the good part. you're missing it.`, `${B}. it's happening. boooo if you don't.`, `psst — ${B}'s hot. I'd applaud but my hands are tired from waiting.`, `${B} just came alive. the bar's that low and you still tripped.`],
                (B) => [`${B}'s STILL going. you gonna do something or just heckle from the cheap seats with me?`, `still hot on ${B}. why do we even come here?`, `${B} hasn't quit. why have you?`, `${B}'s holding. unlike my patience, which left ten minutes ago.`, `still ${B}, still nothing from you. riveting theatre.`, `${B}'s an open mic and you've got stage fright.`, `bravo, ${B}! ...and nothing. the crowd goes mild.`, `${B}'s been on a while. so has my disappointment.`, `${B} keeps performing. tough crowd of one, eh?`, `they don't make openings like ${B} anymore. and you don't make QSOs. perfect match.`],
                (B) => [`${B} workable from YOUR grid. from YOUR grid, Kevin.`, `${B}'s been open for ages. I'm not gonna ask again. (I will.)`, `you KNOW ${B}'s still going, right? RIGHT?`, `${B} from your own backyard and you're reading THIS. bold.`, `I've seen glaciers move faster than you onto ${B}.`, `${B}'s practically begging. it's getting embarrassing — for you.`, `this ${B} opening deserves a better operator. sadly it has you.`, `${B}: open. you: a cautionary tale.`, `still ${B}! I've reviewed the footage. you do nothing. consistently.`, `${B}'s a standing ovation and you're checking your phone.`],
                (B) => [`openings came and went on ${B}. I watched 'em leave. told 'em you were busy.`, `${B} was wide open. hope the chair's comfy — it's your only catch today.`, `${B} gave you twenty minutes. you gave it a blank stare.`, `that was a museum-grade ${B} opening. and you, the guard who slept through the heist.`, `${B}'s gone. that's showbiz. terrible, terrible showbiz.`, `and ${B} exits stage left. the only thing you worked today was my nerves.`, `${B} closed. round of applause for doing absolutely nothing.`, `the great ${B} opening of today: unattended. like a sad buffet.`, `${B}'s done. I'd say 'next time' but we both know.`, `${B} is history. so's your reputation in this shack.`],
            ],
            drill: [
                (B) => [`${B}. ON THE AIR. NOW.`, `${B} IS LIVE. WHAT ARE YOU WAITING FOR.`, `MOVEMENT ON ${B}. RESPOND.`, `EYES ON ${B}, OPERATOR.`, `${B} HOT. BOOTS ON. MOVE.`, `THIS IS NOT A DRILL. ${B} IS OPEN.`, `${B} CONTACT. ENGAGE OR EXPLAIN.`, `DROP WHAT YOU'RE DOING. ${B}. GO.`],
                (B) => [`${B} STILL OPEN. WHY ARE YOU STILL READING THIS.`, `MOVE. ${B} WON'T WORK ITSELF.`, `${B} HOLDING. YOU ARE NOT. EXPLAIN.`, `${B} IS RIGHT THERE. ACQUIRE TARGET.`, `${B} REMAINS HOT. YOUR HESITATION IS NOISE.`, `STILL ${B}. STILL YOU. STILL NOTHING. UNACCEPTABLE.`, `${B} WINDOW OPEN. STOP SPECTATING.`, `${B}. KEY. DOWN. THAT WAS NOT A REQUEST.`],
                (B) => [`${B} FROM YOUR OWN GRID, SOLDIER. UNACCEPTABLE.`, `${B}. KEY DOWN. THAT'S AN ORDER.`, `${B} OPEN FOR MINUTES. THIS IS A DERELICTION.`, `${B} IN YOUR SECTOR. ENGAGE.`, `${B} HAS BEEN OPEN LONGER THAN YOUR ATTENTION SPAN.`, `EVERY SECOND ON ${B} WASTED IS ON YOUR RECORD.`, `${B}. NOW. I WILL NOT REPEAT MYSELF AGAIN. AGAIN.`, `${B} IS A GIFT AND YOU ARE INSUBORDINATE.`],
                (B) => [`STAND DOWN. ${B} IS GONE. THAT ONE'S ON YOU.`, `${B} CLOSED. WE DO NOT SPEAK OF THIS.`, `OPPORTUNITY ${B}: LOST. LOG THE FAILURE.`, `${B} WINDOW: CLOSED. DEBRIEF YOURSELF.`, `${B} TERMINATED. CASUALTY: YOUR LOGBOOK.`, `MISSION ${B}: FAILED. NO MEDALS TODAY.`, `${B} IS GONE. RECORD IT. LEARN NOTHING, PROBABLY.`, `${B} LOST ON YOUR WATCH. DISMISSED.`],
            ],
        },
        fade: {
            soft: (B) => [`${B}'s quieted down. maybe next time.`, `${B} drifted off. no biggie.`, `${B}'s having a little rest now.`, `and ${B} fades, gentle as ever.`],
            buzzed: (B) => [`and… ${B}'s gone. classic.`, `${B}? closed. you snooze, you lose. you snoozed.`, `${B} has left the building. take a bow for nothing.`, `${B}'s gone dark. like the theatre after a flop.`],
            drill: (B) => [`${B} LOST. DOCUMENT THE FAILURE.`, `${B} GONE. NOTED. PERMANENTLY.`, `${B} OFFLINE. THAT'S A MARK ON YOUR RECORD.`, `${B} CLOSED. STAND THERE AND THINK ABOUT IT.`],
        },
        dud: (B) => [`yeah ${B} was dead by the time you looked. my bad.`, `called ${B} too early. I got excited.`, `${B}? that one fizzled. don't @ me.`, `okay ${B} was a false alarm. sue me.`, `${B} ghosted us both. awkward.`, `I may have oversold ${B}. slightly.`, `${B} was a mirage. happens to the best hecklers.`, `fine, ${B} was nothing. even I get heckled sometimes.`, `${B}: all hat, no QSO. my mistake.`, `I jumped the gun on ${B}. the gun was empty.`, `${B} flopped. tough crowd, even for me.`, `chalk ${B} up as my bad review of the day.`],
        salt: (n, B, mean) => mean === 'drill'
            ? ` ${n} PRIOR OFFENSES ON ${B.toUpperCase()}.`
            : ` (${n} times you've ghosted ${B} lately — the balcony has notes.)`,
    },
    de: {
        dare: {
            soft: [
                (B) => [`hey, ${B} sieht vielversprechend aus — mal reinhören?`, `${B} zieht an. wann immer du magst.`, `psst — auf ${B} bewegt sich was.`, `kleine Öffnung auf ${B}, falls du neugierig bist.`, `${B} wärmt sich auf. kein Druck.`, `da regt sich was Sanftes auf ${B}.`, `${B} hat dir gerade zugezwinkert. unhöflich, nicht hinzusehen.`, `leichtes Lüftchen auf ${B} — deine Entscheidung.`],
                (B) => [`${B} hält sich schön. keine Eile.`, `läuft noch auf ${B}. das Gerät steht bereit.`, `${B} sieht weiter gut aus. nur so ein Gedanke.`, `schön gleichmäßig hier auf ${B}. ganz wie du willst.`, `${B} ist sehr geduldig mit dir.`, `immer noch ein netter Lauf auf ${B}.`, `${B} hat dich noch nicht aufgegeben.`, `${B} läuft weiter. lass dir Zeit, Maestro.`],
                (B) => [`${B} ist immer noch gut aus deinem Locator! schade drum.`, `${B} gibt einfach weiter. nur so.`, `${B} ist schon 'ne ganze Weile richtig nett.`, `ehrlich, ${B} von deinem Standort ist gerade ein Genuss.`, `${B} ist praktisch Geschenkpapier für dich.`, `du könntest gerade die Welt auf ${B} arbeiten, weißt du.`, `${B} ist so lange offen, es macht's sich schon gemütlich.`, `wäre nett zu ${B}, mal zu antworten.`],
                (B) => [`${B} hatte einen schönen Lauf. nimm die nächste mit!`, `das war eine nette ${B}-Öffnung. es kommen weitere.`, `${B} ist abgeflaut, aber kein Stress — die kommen wieder.`, `du hast ein feines ${B}-Fenster verpasst. passiert jedem.`, `${B} hat sich zugedeckt. schlaf gut, kleines Band.`, `na ja. ${B} verzeiht dir. wahrscheinlich.`, `der ${B}-Vorhang ist gefallen. schön war's.`, `${B} ist still geworden — die Sparkline bleibt uns.`],
            ],
            buzzed: [
                (B) => [`${B} ist wach. mehr als man von dir behaupten kann.`, `oh, ${B} tut was. im Gegensatz zu GEWISSEN Leuten.`, `${B} leuchtet auf. schnell, ignorier es kaputt.`, `auf ${B} regt sich was. nur nicht alle auf einmal.`, `${B} hat 'nen Puls. und du 'ne Ausrede?`, `meine Damen und Herren, ${B} betritt die Bühne. Abgang: du.`, `${B} öffnet. das ist der gute Teil. du verpasst ihn.`, `${B}. es passiert. buuuh, wenn nicht.`, `psst — ${B} ist heiß. ich würd klatschen, aber meine Hände sind müde vom Warten.`, `${B} lebt auf. die Latte liegt am Boden und du stolperst trotzdem.`],
                (B) => [`${B} läuft IMMER noch. machst du was, oder heckeln wir zwei aus der Loge?`, `weiter heiß auf ${B}. warum kommen wir überhaupt her?`, `${B} gibt nicht auf. warum du?`, `${B} hält durch. anders als meine Geduld, die ging vor zehn Minuten.`, `immer noch ${B}, immer noch nichts von dir. mitreißendes Theater.`, `${B} ist 'ne offene Bühne und du hast Lampenfieber.`, `bravo, ${B}! ...und nichts. das Publikum tobt verhalten.`, `${B} läuft schon 'ne Weile. meine Enttäuschung auch.`, `${B} spielt weiter. zähes Publikum von einem, was?`, `solche Öffnungen wie ${B} gibt's kaum noch. und QSOs machst du auch keine. passt.`],
                (B) => [`${B} machbar aus DEINEM Locator. aus DEINEM, Kevin.`, `${B} ist seit Ewigkeiten offen. ich frag nicht nochmal. (doch.)`, `du WEISST, dass ${B} noch läuft, oder? ODER?`, `${B} direkt vor der Haustür und du liest DAS hier. mutig.`, `ich hab Gletscher schneller Richtung ${B} kriechen sehen als dich.`, `${B} bettelt praktisch. wird langsam peinlich — für dich.`, `diese ${B}-Öffnung verdient einen besseren Operator. leider hat sie dich.`, `${B}: offen. du: ein warnendes Beispiel.`, `immer noch ${B}! ich hab die Aufzeichnung geprüft. du tust nichts. zuverlässig.`, `${B} sind stehende Ovationen und du checkst dein Handy.`],
                (B) => [`Öffnungen kamen und gingen auf ${B}. ich hab zugesehen. hab gesagt, du hast zu tun.`, `${B} war sperrangelweit offen. hoffentlich sitzt du bequem — das war dein einziger Fang.`, `${B} gab dir zwanzig Minuten. du gabst einen leeren Blick.`, `das war eine museumsreife ${B}-Öffnung. und du der Wachmann, der den Coup verschlief.`, `${B} ist weg. das ist Showbusiness. furchtbares Showbusiness.`, `und ${B} geht ab nach links. das Einzige, was du heute gearbeitet hast, sind meine Nerven.`, `${B} zu. Applaus dafür, dass du absolut nichts getan hast.`, `die große ${B}-Öffnung heute: unbeaufsichtigt. wie ein trauriges Buffet.`, `${B} ist durch. ich würd 'nächstes Mal' sagen, aber wir wissen beide Bescheid.`, `${B} ist Geschichte. dein Ruf in diesem Shack auch.`],
            ],
            drill: [
                (B) => [`${B}. AUF SENDUNG. SOFORT.`, `${B} IST LIVE. WORAUF WARTEST DU.`, `BEWEGUNG AUF ${B}. REAGIEREN.`, `AUGEN AUF ${B}, FUNKER.`, `${B} HEISS. STIEFEL AN. BEWEGUNG.`, `DAS IST KEINE ÜBUNG. ${B} IST OFFEN.`, `${B}-KONTAKT. ANGREIFEN ODER ERKLÄREN.`, `ALLES STEHEN UND LIEGEN LASSEN. ${B}. LOS.`],
                (B) => [`${B} IMMER NOCH OFFEN. WARUM LIEST DU DAS NOCH.`, `BEWEGUNG. ${B} ARBEITET NICHT VON ALLEIN.`, `${B} HÄLT. DU NICHT. ERKLÄRUNG.`, `${B} IST GENAU DA. ZIEL ERFASSEN.`, `${B} WEITER HEISS. DEIN ZÖGERN IST NUR RAUSCHEN.`, `IMMER NOCH ${B}. IMMER NOCH DU. IMMER NOCH NICHTS. INAKZEPTABEL.`, `${B}-FENSTER OFFEN. HÖR AUF ZU GLOTZEN.`, `${B}. TASTE. RUNTER. DAS WAR KEINE BITTE.`],
                (B) => [`${B} AUS DEINEM EIGENEN LOCATOR, SOLDAT. INAKZEPTABEL.`, `${B}. TASTE RUNTER. DAS IST EIN BEFEHL.`, `${B} SEIT MINUTEN OFFEN. DAS IST PFLICHTVERGESSEN.`, `${B} IN DEINEM SEKTOR. ANGREIFEN.`, `${B} IST LÄNGER OFFEN ALS DEINE AUFMERKSAMKEITSSPANNE.`, `JEDE VERGEUDETE SEKUNDE AUF ${B} GEHT IN DEINE AKTE.`, `${B}. JETZT. ICH WIEDERHOLE MICH NICHT NOCHMAL. NOCHMAL.`, `${B} IST EIN GESCHENK UND DU VERWEIGERST DEN BEFEHL.`],
                (B) => [`RÜCKZUG. ${B} IST WEG. DAS GEHT AUF DEINE KAPPE.`, `${B} GESCHLOSSEN. WIR REDEN NICHT DARÜBER.`, `CHANCE ${B}: VERLOREN. VERSAGEN PROTOKOLLIEREN.`, `FENSTER ${B}: ZU. SELBST-DEBRIEFING.`, `${B} BEENDET. VERLUST: DEIN LOGBUCH.`, `MISSION ${B}: GESCHEITERT. HEUTE KEINE ORDEN.`, `${B} IST WEG. NOTIEREN. VERMUTLICH NICHTS LERNEN.`, `${B} VERLOREN UNTER DEINER AUFSICHT. WEGTRETEN.`],
            ],
        },
        fade: {
            soft: (B) => [`${B} ist ruhiger geworden. vielleicht nächstes Mal.`, `${B} hat sich verabschiedet. halb so wild.`, `${B} macht jetzt ein Päuschen.`, `und ${B} verklingt, sanft wie immer.`],
            buzzed: (B) => [`und… ${B} ist weg. typisch.`, `${B}? zu. wer zu spät kommt… du kamst zu spät.`, `${B} hat das Gebäude verlassen. Verbeugung für nichts.`, `${B} ist dunkel. wie das Theater nach 'nem Flop.`],
            drill: (B) => [`${B} VERLOREN. VERSAGEN DOKUMENTIEREN.`, `${B} WEG. NOTIERT. FÜR IMMER.`, `${B} OFFLINE. DAS IST EIN EINTRAG IN DEINER AKTE.`, `${B} ZU. STELL DICH HIN UND DENK DRÜBER NACH.`],
        },
        dud: (B) => [`ja, ${B} war schon tot, als du geguckt hast. mein Fehler.`, `${B} zu früh gerufen. ich war aufgeregt.`, `${B}? ist verpufft. nicht meckern.`, `okay, ${B} war ein Fehlalarm. verklag mich.`, `${B} hat uns beide versetzt. peinlich.`, `ich hab ${B} vielleicht leicht überverkauft.`, `${B} war 'ne Fata Morgana. passiert den besten Hecklern.`, `gut, ${B} war nichts. auch ich werd mal ausgebuht.`, `${B}: viel Lärm, kein QSO. mein Fehler.`, `ich hab bei ${B} vorgeprescht. die Kammer war leer.`, `${B} ist gefloppt. zähes Publikum, sogar für mich.`, `verbuch ${B} als meine Verrisskritik des Tages.`],
        salt: (n, B, mean) => mean === 'drill'
            ? ` ${n} FRÜHERE VERSTÖSSE AUF ${B.toUpperCase()}.`
            : ` (${n}× hast du ${B} zuletzt versetzt — die Loge führt Buch.)`,
    },
};

// With `rings=auto` widened past the home square, lines claiming the opening is
// from YOUR grid would be a lie; they are skipped then.
const LOCAL_GRID_RE = /\byour (own )?grid\b|\byour spot\b|\byour own backyard\b|\bdeinem (eigenen )?locator\b|\bdeinem standort\b|\bvor der haustür\b/i;

function pick(arr, opts = {}) {
    let pool = arr;
    if (opts.widened) {
        const kept = pool.filter((t) => !LOCAL_GRID_RE.test(t));
        if (kept.length) pool = kept;
    }
    const r = typeof opts.rand === 'function' ? opts.rand() : Math.random();
    return pool[Math.floor(r * pool.length)] || pool[0];
}

function langPack(lang) {
    return LINES[lang] || LINES.en;
}

function ladderFor(lang, meanness) {
    return langPack(lang).dare[meanness] || langPack(lang).dare.buzzed;
}

function dareLine(opts, rung, band) {
    const ladder = ladderFor(opts.lang, opts.meanness);
    return pick(ladder[Math.min(rung, MAX_LIVE_RUNG)](band), opts);
}

// ladder[3]: the "that opening is gone" lines, spoken as the fade line for an
// unanswered episode that reached rung 2.
function goneLine(opts, band) {
    const ladder = ladderFor(opts.lang, opts.meanness);
    return pick(ladder[ladder.length - 1](band), opts);
}

function fadeLine(opts, band) {
    const fn = langPack(opts.lang).fade[opts.meanness] || langPack(opts.lang).fade.buzzed;
    return pick(fn(band), opts);
}

function dudLine(lang, band, opts = {}) {
    return pick(langPack(lang).dud(band), opts);
}

function saltLine(lang, n, band, meanness) {
    return langPack(lang).salt(n, band, meanness);
}

function defaultLang() {
    try {
        return (navigator.language || 'en').toLowerCase().startsWith('de') ? 'de' : 'en';
    } catch {
        return 'en';
    }
}

// Localised panel chrome (Horst-Kevin's voice, so it follows the language too).
const UI = {
    en: {
        confTitle: 'Eating my words',
        grudgeTitle: 'The grudge',
        noGraded: 'No graded dares yet. Give me a chance.',
        honesty: (t, b, d, p) => `Last ${t} dares: <strong>${b}</strong> bangers · <strong>${d}</strong> ${d === 1 ? 'lie' : 'lies'} · <strong>${p}%</strong> honest`,
        grudgeHead: (n) => `You've ghosted me <strong>${n}</strong> ${n === 1 ? 'time' : 'times'} this week.`,
        cleanSlate: 'Clean slate. For now.',
        ghosted: (n) => `ghosted ${n}×`,
        nag: 'let me nag your desktop',
        meanLabels: { soft: 'Supportive', buzzed: 'In character', drill: 'Drill Sergeant' },
        demoDone: 'demo done. reload to reset.',
        demo: {
            blip: 'demo: 15m shows up for one poll and vanishes. a blip. I say nothing.',
            open: 'demo: 20m opens and holds. watch me confirm it, then climb.',
            flap: 'demo: 20m drops out for one poll. grace period, same opening.',
            fade: 'demo: 20m is gone for good now. you never answered.',
            answer: 'demo: 10m opens, and this time you switch to it.',
            gap: 'demo: the tab was hidden for 30 minutes. I resync quietly.',
        },
    },
    de: {
        confTitle: 'Wort gehalten?',
        grudgeTitle: 'Nachtragend',
        noGraded: "Noch keine bewerteten Ansagen. Gib mir 'ne Chance.",
        honesty: (t, b, d, p) => `Letzte ${t} Ansagen: <strong>${b}</strong> Volltreffer · <strong>${d}</strong> ${d === 1 ? 'Lüge' : 'Lügen'} · <strong>${p}%</strong> ehrlich`,
        grudgeHead: (n) => `Du hast mich diese Woche <strong>${n}</strong>× versetzt.`,
        cleanSlate: 'Weiße Weste. Vorerst.',
        ghosted: (n) => `${n}× versetzt`,
        nag: 'nerv meinen Desktop',
        meanLabels: { soft: 'Aufmunternd', buzzed: 'Echt Horst-Kevin', drill: 'Ausbilder' },
        demoDone: 'Demo fertig. zum Zurücksetzen neu laden.',
        demo: {
            blip: 'Demo: 15m taucht eine Abfrage lang auf und ist wieder weg. ein Ausreißer. ich sag nichts.',
            open: 'Demo: 20m öffnet und hält. erst bestätige ich, dann lege ich nach.',
            flap: 'Demo: 20m fehlt eine Abfrage lang. Schonfrist, dieselbe Öffnung.',
            fade: 'Demo: 20m ist jetzt wirklich weg. du hast nie geantwortet.',
            answer: 'Demo: 10m öffnet, und diesmal wechselst du hin.',
            gap: 'Demo: der Tab war 30 Minuten versteckt. ich synchronisiere mich still.',
        },
    },
};

function uiPack(lang) {
    return UI[lang] || UI.en;
}


// ── the poll → dare-ladder reducer (pure) ─────────────────────────────────
//
// State is plain JSON. An episode is one opening on one band:
//   { band, firstSeenAt, lastSeenAt, seenPolls, confirmed, observedMs, rung,
//     spoken, answered, grudged, peakRate, kind, reason, callId }
// `rung` is the highest rung actually spoken (-1 = nothing said yet), so an
// episode only climbs when a line goes out, one rung per poll at most.
// `observedMs` accrues min(gap, 2 polls) per sighting, so a hidden-tab gap is
// never counted as time you spent ignoring him. `seenPolls` only counts
// sightings at least MIN_SIGHTING_GAP_MS apart (`lastCountedAt`), so extra
// refresh() polls cannot rush the debounce.
//
// The speech budget outlives the page: every spoken rung-0 dare is a persisted
// call, so a reload (or an SW update) re-derives the daily dare count, the last
// spoken time and a per-band cooldown floor (call ts + profile cooldown) from
// the restored calls instead of starting from a fresh budget.

export function createState({ calls = [], grudges = [] } = {}) {
    const kept = Array.isArray(calls) ? calls.slice(-MAX_CALLS) : [];
    const cooldownUntil = {};
    let lastSpokeAt = 0;
    for (const c of kept) {
        const ts = Number(c?.ts) || 0;
        const band = norm(c?.band);
        if (!ts || !band) continue;
        lastSpokeAt = Math.max(lastSpokeAt, ts);
        cooldownUntil[band] = Math.max(cooldownUntil[band] || 0, ts + bandProfile(band).cooldownMs);
    }
    return {
        lastPollAt: 0,
        ctxKey: null,
        episodes: {},
        recent: {},         // band → { endedAt, episode } for EPISODE_MERGE_MS
        cooldownUntil,      // band → ts before which no new rung-0 dare
        lastSpokeAt,
        lastSpokeBand: '',  // band of the last spoken line (rung escalations skip the gap)
        day: '',            // UTC day the dare count belongs to
        dareCount: 0,
        polls: [],          // [{ ts, present: [band], grace: [band] }] for verdicts
        calls: kept,
        grudges: Array.isArray(grudges) ? grudges.slice(-MAX_GRUDGES) : [],
    };
}

// Spoken rung-0 dares on UTC day `day`, whatever their verdict.
function daresOn(calls, day) {
    return calls.filter((c) => (c.rung ?? 0) === 0 && Number(c.ts) > 0 && utcDay(Number(c.ts)) === day).length;
}

function cloneState(state) {
    return JSON.parse(JSON.stringify(state));
}

function utcDay(now) {
    return new Date(now).toISOString().slice(0, 10);
}

function setCooldown(s, band, from) {
    const until = from + bandProfile(band).cooldownMs;
    s.cooldownUntil[band] = Math.max(s.cooldownUntil[band] || 0, until);
}

function voidCall(s, ep) {
    if (!ep?.callId) return;
    const c = s.calls.find((x) => x.id === ep.callId);
    if (c && c.verdict === 'pending') c.verdict = 'void';
}

function answerEpisode(s, ep) {
    if (ep.answered) return;
    ep.answered = true;
    if (!ep.callId) return;
    const c = s.calls.find((x) => x.id === ep.callId);
    if (c && c.verdict === 'pending') c.verdict = 'answered';
}

// Close every episode without grudges or speech, void pending calls. Spoken
// episodes still start their band cooldown, counted from when they were last
// seen, so a long absence does not suppress a fresh opening on return.
function resyncInPlace(s) {
    for (const ep of Object.values(s.episodes)) {
        if (ep.spoken) setCooldown(s, ep.band, ep.lastSeenAt);
    }
    s.episodes = {};
    s.recent = {};
    s.polls = [];
    for (const c of s.calls) {
        if (c.verdict === 'pending') c.verdict = 'void';
    }
}

// Quiet reset (no QTH, stale feed): the next poll is treated as a first poll.
export function resync(state) {
    const s = cloneState(state);
    resyncInPlace(s);
    s.lastPollAt = 0;
    s.ctxKey = null;
    return s;
}

// The operator took the dare from the bubble (switchToBand succeeded).
export function markAnswered(state, band) {
    const s = cloneState(state);
    const ep = s.episodes[norm(band)];
    if (ep) answerEpisode(s, ep);
    return s;
}

export function moodOf(state) {
    const eps = Object.values(state?.episodes || {});
    if (eps.some((e) => e.spoken && !e.answered)) return 'fired';
    return eps.length ? 'stirring' : 'dormant';
}

export function grudgeCountFor(grudges, band, now) {
    return grudges.filter((g) => g.band === band && now - g.fadedAt < GRUDGE_WINDOW_MS).length;
}

function addGrudge(s, entry, now) {
    s.grudges.push(entry);
    s.grudges = s.grudges.filter((g) => now - g.fadedAt < GRUDGE_WINDOW_MS);
    if (s.grudges.length > MAX_GRUDGES) s.grudges = s.grudges.slice(-MAX_GRUDGES);
}

function recordCall(s, ep, now) {
    const id = `${now}-${ep.band}`;
    s.calls.push({ id, band: ep.band, rung: 0, kind: ep.kind || '', rate: ep.peakRate || 0, ts: now, verdict: 'pending' });
    if (s.calls.length > MAX_CALLS) s.calls = s.calls.slice(-MAX_CALLS);
    return id;
}

function accrue(ep, now) {
    ep.observedMs += Math.max(0, Math.min(now - ep.lastSeenAt, 2 * TUNING.POLL_MS));
}

// One more sighting toward the debounce, unless the last counted one was only
// seconds ago (an extra refresh() poll).
function countSighting(ep, now) {
    if (now - (ep.lastCountedAt || 0) < TUNING.MIN_SIGHTING_GAP_MS) return;
    ep.seenPolls += 1;
    ep.lastCountedAt = now;
}

// Usual share when the almanac gates this kind on this band now, else null.
// Only `rising` / `dx_surge` are gated; a `surprise` always passes.
function almanacGate(almanac, band, kind, now) {
    if (!almanac || (kind !== 'rising' && kind !== 'dx_surge')) return null;
    const usual = usualShareNow(almanac, band, almanacSlot(now, almanac.slot_minutes));
    return usual !== null && usual >= usuallyShare(almanac) ? usual : null;
}

function newEpisode(band, rec, now) {
    return {
        band,
        firstSeenAt: now,
        lastSeenAt: now,
        lastCountedAt: now,
        seenPolls: 1,
        confirmed: false,
        observedMs: 0,
        rung: -1,
        spoken: false,
        answered: false,
        grudged: false,
        peakRate: Number(rec.spots_per_minute) || 0,
        kind: rec.kind || '',
        reason: rec.reason || '',
        callId: null,
    };
}

// Grade pending calls whose verdict window has passed: banger when the band was
// alive in ≥ VERDICT_BANGER_SHARE of the window's polls. Grace polls count only
// once the gap is bridged (the band is back now); a gap still open at grading
// time counts as gone, so a dare that vanished at once is a dud, not a banger
// propped up by its own grace period. Low poll coverage or a call pending long
// past its window (restored from storage, say) is void.
function gradeCalls(s, now, note) {
    const duds = [];
    for (const c of s.calls) {
        if (c.verdict !== 'pending') continue;
        const win = bandProfile(c.band).verdictMs;
        const age = now - c.ts;
        if (age < win) continue;
        if (age > win + TUNING.VERDICT_STALE_POLLS * TUNING.POLL_MS) {
            c.verdict = 'void';
            note(c.band, 'call void (stale)');
            continue;
        }
        const inWin = s.polls.filter((p) => p.ts >= c.ts && p.ts <= c.ts + win);
        const expected = Math.floor(win / TUNING.POLL_MS);
        if (inWin.length < TUNING.VERDICT_MIN_COVERAGE * expected) {
            c.verdict = 'void';
            note(c.band, `call void (coverage ${inWin.length}/${expected})`);
            continue;
        }
        const ep = s.episodes[c.band];
        const bridged = Boolean(ep && ep.lastSeenAt === now);
        const alive = inWin.filter((p) => p.present.includes(c.band) || (bridged && p.grace.includes(c.band))).length;
        c.verdict = alive / inWin.length >= TUNING.VERDICT_BANGER_SHARE ? 'banger' : 'dud';
        note(c.band, `call ${c.verdict} (${alive}/${inWin.length})`);
        if (c.verdict === 'dud') duds.push(c.band);
    }
    return duds;
}

function rankCandidate(a, b) {
    if (b.rung !== a.rung) return b.rung - a.rung;
    const sa = a.ep.kind === 'surprise' ? 1 : 0;
    const sb = b.ep.kind === 'surprise' ? 1 : 0;
    if (sb !== sa) return sb - sa;
    return (b.ep.peakRate || 0) - (a.ep.peakRate || 0);
}

/**
 * One poll. Returns { state, speech[], trace }; never mutates `state`.
 *
 * ctx: {
 *   ctxKey        'qth|surroundings' — a change resyncs quietly
 *   answeredBands bands the operator is on (map focus, rig band)
 *   enabledBands  Set of enabled bands, or null for no filter
 *   holding       /api/hot_bands holding[] for the tracked bands
 *   almanac       /api/almanac payload, or null (never gates)
 *   widened       response area.widened — skip "YOUR grid" lines
 *   panelOpen     dud lines are only spoken while the panel is open
 *   lang, meanness, rand
 * }
 * speech items: { type: 'dare'|'fade'|'dud', band, rung, kind, text, nag }
 */
export function reduce(state, recs, now, ctx = {}) {
    const T = TUNING;
    const s = cloneState(state);
    const speech = [];
    const decisions = [];
    const note = (band, what) => decisions.push(band ? `${band}: ${what}` : what);
    const lineOpts = { lang: ctx.lang || 'en', meanness: ctx.meanness || 'buzzed', widened: Boolean(ctx.widened), rand: ctx.rand };

    // A new UTC day, or the first poll after a reload: count today's dares from
    // the (persisted) calls rather than starting at zero.
    const day = utcDay(now);
    if (s.day !== day) {
        s.day = day;
        s.dareCount = daresOn(s.calls, day);
    }

    // Quiet resync: first poll after load, a poll gap, or a new area.
    const ctxKey = ctx.ctxKey ?? null;
    let silent = '';
    if (!s.lastPollAt) silent = 'first poll';
    else if (now - s.lastPollAt > T.RESYNC_GAP_MS) silent = `gap ${Math.round((now - s.lastPollAt) / 1000)}s`;
    else if (ctxKey !== null && s.ctxKey !== null && ctxKey !== s.ctxKey) silent = 'area changed';
    if (silent && s.lastPollAt) {
        resyncInPlace(s);
        note('', `resync (${silent})`);
    }
    if (ctxKey !== null) s.ctxKey = ctxKey;
    s.lastPollAt = now;

    const inScope = dareBandsAt(now);
    const enabled = ctx.enabledBands ? new Set([...ctx.enabledBands].map(norm)) : null;
    const allowed = (b) => inScope.has(b) && (!enabled || enabled.has(b));
    const answered = new Set((ctx.answeredBands || []).map(norm).filter(Boolean));
    const holding = new Map();
    for (const h of Array.isArray(ctx.holding) ? ctx.holding : []) {
        const b = norm(h?.band);
        if (b) holding.set(b, h);
    }

    const present = new Map();
    for (const rec of Array.isArray(recs) ? recs : []) {
        const b = norm(rec?.band);
        if (!allowed(b) || present.has(b)) continue;
        present.set(b, rec);
    }

    // Episodes on bands that left scope (disabled, 6m out of season): drop quietly.
    for (const [b, ep] of Object.entries(s.episodes)) {
        if (allowed(b)) continue;
        voidCall(s, ep);
        delete s.episodes[b];
        note(b, 'out of scope, dropped');
    }
    for (const [b, r] of Object.entries(s.recent)) {
        if (now - r.endedAt > T.EPISODE_MERGE_MS || !allowed(b)) delete s.recent[b];
    }
    // Answered before the fade loop: an answered band counts as present.
    for (const [b, ep] of Object.entries(s.episodes)) {
        if (answered.has(b) && !ep.answered) {
            answerEpisode(s, ep);
            note(b, 'answered');
        }
    }

    // Bands in this poll's recommendations.
    for (const [b, rec] of present) {
        let ep = s.episodes[b];
        if (!ep) {
            const r = s.recent[b];
            if (r) {
                ep = r.episode;
                delete s.recent[b];
                delete s.cooldownUntil[b];
                accrue(ep, now);
                countSighting(ep, now);
                note(b, 'back within merge window, episode continues');
            } else {
                const usual = almanacGate(ctx.almanac, b, rec.kind, now);
                if (usual !== null) {
                    note(b, `${rec.kind} but usually open now (${usual.toFixed(2)}), ignored`);
                    continue;
                }
                ep = newEpisode(b, rec, now);
                note(b, `new (${rec.kind || '?'}, ${(Number(rec.spots_per_minute) || 0).toFixed(2)}/min)`);
            }
            s.episodes[b] = ep;
        } else {
            accrue(ep, now);
            countSighting(ep, now);
        }
        ep.lastSeenAt = now;
        ep.peakRate = Math.max(ep.peakRate || 0, Number(rec.spots_per_minute) || 0);
        ep.kind = rec.kind || ep.kind;
        ep.reason = rec.reason || ep.reason;
        if (answered.has(b) && !ep.answered) answerEpisode(s, ep);
        if (!ep.confirmed && ep.seenPolls >= bandProfile(b).confirmPolls) {
            ep.confirmed = true;
            note(b, 'confirmed');
        }
    }

    // Bands missing from the recommendations: answered, holding, grace, or faded.
    const faded = [];
    for (const [b, ep] of Object.entries(s.episodes)) {
        if (present.has(b)) continue;
        if (ep.answered && answered.has(b)) {
            accrue(ep, now);
            ep.lastSeenAt = now;
            note(b, 'answered, counts as present');
            continue;
        }
        if (!ep.confirmed) {
            delete s.episodes[b];
            note(b, 'blip, dropped');
            continue;
        }
        const h = holding.get(b);
        if (h && isHoldingOpen(h, ep.peakRate)) {
            accrue(ep, now);
            countSighting(ep, now);
            ep.lastSeenAt = now;
            note(b, `holding (${h.status || '?'}, ${(Number(h.spots_per_minute) || 0).toFixed(2)}/min)`);
            continue;
        }
        const away = now - ep.lastSeenAt;
        if (away < T.FADE_GRACE_MS) {
            note(b, `grace ${(away / 60_000).toFixed(1)}/${T.FADE_GRACE_MS / 60_000} min`);
            continue;
        }
        delete s.episodes[b];
        s.recent[b] = { endedAt: now, episode: ep };
        if (ep.spoken) setCooldown(s, b, now);
        const grudge = !ep.answered && ep.spoken && !ep.grudged
            && ep.rung >= T.GRUDGE_MIN_RUNG && ep.observedMs >= T.GRUDGE_MIN_OBSERVED_MS;
        if (grudge) {
            addGrudge(s, { band: b, kind: ep.kind, reason: ep.reason, peakRate: Math.round((ep.peakRate || 0) * 100) / 100, firstSeenAt: ep.firstSeenAt, fadedAt: now }, now);
            ep.grudged = true;
        }
        note(b, `faded${grudge ? ', grudge' : ''}`);
        if (!ep.answered && ep.spoken) faded.push(ep);
    }

    // Almanac gate, re-applied to every episode that has not spoken yet: the
    // almanac usually lands a poll or two after load (or after a QTH change), so
    // episodes created before it arrived are checked again here and dropped
    // quietly if the band turns out to be usually open now.
    for (const [b, ep] of Object.entries(s.episodes)) {
        if (ep.spoken || ep.answered) continue;
        const usual = almanacGate(ctx.almanac, b, ep.kind, now);
        if (usual === null) continue;
        delete s.episodes[b];
        note(b, `${ep.kind} but usually open now (${usual.toFixed(2)}), dropped`);
    }

    // Rung candidates: confirmed, unanswered, seen this poll, next rung earned.
    const candidates = [];
    for (const ep of Object.values(s.episodes)) {
        if (!ep.confirmed || ep.answered || ep.lastSeenAt !== now) continue;
        const next = ep.rung + 1;
        if (next > MAX_LIVE_RUNG || desiredRung(ep.band, ep.observedMs) < next) continue;
        candidates.push({ ep, rung: next });
    }
    candidates.sort(rankCandidate);

    const gapOk = !s.lastSpokeAt || now - s.lastSpokeAt >= T.GLOBAL_MIN_GAP_MS;
    // The band that spoke last may climb on its own rung timings.
    const escalation = (c) => c.rung > 0 && c.ep.band === s.lastSpokeBand;
    let spoke = false;
    let spokeBand = '';
    if (silent) {
        if (candidates.length || faded.length) note('', `silent tick (${silent})`);
    } else {
        for (const c of candidates) {
            const b = c.ep.band;
            if (!gapOk && !escalation(c)) {
                note(b, `rung ${c.rung} held (min gap)`);
                continue;
            }
            if (c.rung === 0) {
                if ((s.cooldownUntil[b] || 0) > now) {
                    note(b, `cooldown ${Math.ceil((s.cooldownUntil[b] - now) / 60_000)} min`);
                    continue;
                }
                const cap = c.ep.kind === 'surprise' ? T.DAILY_SURPRISE_CAP : T.DAILY_DARE_CAP;
                if (s.dareCount >= cap) {
                    note(b, `daily cap (${s.dareCount}/${cap})`);
                    continue;
                }
            }
            c.ep.rung = c.rung;
            c.ep.spoken = true;
            if (c.rung === 0) {
                s.dareCount += 1;
                c.ep.callId = recordCall(s, c.ep, now);
            }
            let text = dareLine(lineOpts, c.rung, b);
            const n = grudgeCountFor(s.grudges, b, now);
            if (n >= 2 && c.rung >= 2) text += saltLine(lineOpts.lang, n, b, lineOpts.meanness);
            speech.push({ type: 'dare', band: b, rung: c.rung, kind: c.ep.kind, text, nag: c.rung >= 1 || c.ep.kind === 'surprise' });
            note(b, `dare rung ${c.rung}`);
            spoke = true;
            spokeBand = b;
            break;
        }
        if (!spoke && faded.length && !gapOk) {
            for (const ep of faded) note(ep.band, 'fade line held (min gap)');
        } else if (!spoke && faded.length) {
            faded.sort((a, b) => b.rung - a.rung);
            const ep = faded[0];
            const text = ep.rung >= MAX_LIVE_RUNG ? goneLine(lineOpts, ep.band) : fadeLine(lineOpts, ep.band);
            speech.push({ type: 'fade', band: ep.band, rung: ep.rung, kind: ep.kind, text, nag: false });
            note(ep.band, `fade line (rung ${ep.rung})`);
            spoke = true;
            spokeBand = ep.band;
        }
    }

    // Verdicts: log which bands were alive this poll, then grade.
    const presentNow = [];
    const graceNow = [];
    for (const ep of Object.values(s.episodes)) (ep.lastSeenAt === now ? presentNow : graceNow).push(ep.band);
    // An extra poll seconds after the last one replaces it, so refresh() polls
    // do not inflate the verdict coverage.
    const pollRec = { ts: now, present: presentNow, grace: graceNow };
    const lastRec = s.polls[s.polls.length - 1];
    if (lastRec && now - lastRec.ts < T.MIN_SIGHTING_GAP_MS) s.polls[s.polls.length - 1] = pollRec;
    else s.polls.push(pollRec);
    const longestWin = Math.max(...Object.values(T.PROFILES).map((p) => p.verdictMs));
    const keepMs = longestWin + (T.VERDICT_STALE_POLLS + 2) * T.POLL_MS;
    s.polls = s.polls.filter((p) => now - p.ts <= keepMs);
    const duds = gradeCalls(s, now, note);
    if (duds.length && !spoke && !silent && gapOk && ctx.panelOpen) {
        speech.push({ type: 'dud', band: duds[0], rung: -1, kind: '', text: dudLine(lineOpts.lang, duds[0], lineOpts), nag: false });
        spoke = true;
        spokeBand = duds[0];
    }
    if (spoke) {
        s.lastSpokeAt = now;
        s.lastSpokeBand = spokeBand;
    }

    const trace = {
        at: new Date(now).toISOString(),
        silent: silent || null,
        recs: [...present.values()].map((r) => `${norm(r.band)} ${r.kind || '?'} ${(Number(r.spots_per_minute) || 0).toFixed(2)}`),
        holding: [...holding.values()].map((h) => `${norm(h.band)} ${h.status || '?'} ${(Number(h.spots_per_minute) || 0).toFixed(2)}`),
        decisions,
        speech: speech.map((x) => `${x.type} ${x.band}: ${x.text}`),
        dareCount: s.dareCount,
    };
    return { state: s, speech, trace };
}


// ── main ────────────────────────────────────────────────────────────────────
//
// The avatar is the sidebar picture (.sidebar-hk .hk-avatar); the bubble and
// panel live in the body-level #hk-layer (position: fixed, so the sidebar's
// overflow cannot clip them), anchored next to the picture on every show,
// resize and sidebar scroll. With the sidebar hidden the layer falls back to
// the map's bottom-left corner; at ≤ 640 px it is a full-width bottom sheet.

export function initHorstKevin({ getQth, getSurroundings, getCurrentBand, getRigBand, getEnabledBands, onBandSwitch }) {
    const avatar = document.querySelector('.sidebar-hk .hk-avatar');
    const layer = document.getElementById('hk-layer');
    if (!avatar || !layer) return null;

    const bubble = layer.querySelector('.hk-bubble');
    const bubbleText = layer.querySelector('.hk-bubble-text');
    const bubbleAct = layer.querySelector('.hk-bubble-action');
    const panel = layer.querySelector('.hk-panel');
    const controls = document.getElementById('controls');
    const showSidebarBtn = document.getElementById('show-sidebar');
    const mapStack = document.getElementById('map-stack');

    // Only now does the picture become Horst-Kevin (hover, focus, click).
    avatar.classList.add('is-live');
    avatar.tabIndex = 0;
    avatar.removeAttribute('aria-hidden');
    avatar.setAttribute('aria-label', 'Horst-Kevin, band heckler');
    avatar.setAttribute('aria-haspopup', 'dialog');
    layer.hidden = false;

    let params;
    try {
        params = new URLSearchParams(window.location.search || '');
    } catch {
        params = new URLSearchParams('');
    }
    const traceOn = params.has('hk-trace');

    let pollTimer = null;
    let abortCtl = null;
    let lastGoodTs = 0;
    let lastFetchAt = 0;
    let bubbleTimer = null;
    let positionQueued = false;

    let state = createState({ calls: loadJSON(LS.calls, []), grudges: loadJSON(LS.grudges, []) });
    let savedCalls = JSON.stringify(state.calls);
    let savedGrudges = JSON.stringify(state.grudges);
    let meanness = loadJSON(LS.meanness, 'buzzed');
    let lang = loadJSON(LS.lang, defaultLang());
    let desktopNags = loadJSON(LS.desktop, false) === true;
    if (!LINES.en.dare[meanness]) meanness = 'buzzed';
    if (!LINES[lang]) lang = 'en';

    // Almanac for the gate: fetched every ALMANAC_REFRESH_MS per QTH.
    const almanac = { qth: '', data: null, fetchedAt: 0 };

    function persist() {
        const c = JSON.stringify(state.calls);
        const g = JSON.stringify(state.grudges);
        if (c !== savedCalls) { saveJSON(LS.calls, state.calls); savedCalls = c; }
        if (g !== savedGrudges) { saveJSON(LS.grudges, state.grudges); savedGrudges = g; }
    }

    // ── anchoring ────────────────────────────────────────────────────────────

    function sidebarHidden(rect) {
        if (showSidebarBtn && showSidebarBtn.style.display !== 'none' && getComputedStyle(showSidebarBtn).display !== 'none') return true;
        return rect.width === 0 || rect.height === 0 || rect.right <= 0;
    }

    function positionLayer() {
        positionQueued = false;
        if (layer.hidden) return;
        const mobile = typeof window.matchMedia === 'function' && window.matchMedia('(max-width: 640px)').matches;
        const rect = avatar.getBoundingClientRect();
        const detached = sidebarHidden(rect);
        layer.classList.toggle('is-detached', detached);
        layer.classList.toggle('is-mobile', mobile);
        if (mobile) {
            layer.style.removeProperty('--hk-left');
            layer.style.removeProperty('--hk-bottom');
            return;
        }
        let left;
        let bottom;
        if (detached) {
            const m = mapStack ? mapStack.getBoundingClientRect() : { left: 0, bottom: window.innerHeight };
            left = m.left + 14;
            bottom = window.innerHeight - m.bottom + 14;
        } else {
            // Bottom-aligned with the picture, kept within the sidebar's visible
            // band when the picture is scrolled out of view.
            const c = controls ? controls.getBoundingClientRect() : { top: 0, bottom: window.innerHeight, right: rect.right };
            const anchor = Math.min(Math.max(rect.bottom, c.top + 60), c.bottom);
            left = Math.max(rect.right, c.right) + 10;
            bottom = window.innerHeight - anchor;
        }
        layer.style.setProperty('--hk-left', `${Math.round(Math.max(8, left))}px`);
        layer.style.setProperty('--hk-bottom', `${Math.round(Math.max(8, bottom))}px`);
    }

    function schedulePosition() {
        if (positionQueued) return;
        positionQueued = true;
        if (typeof requestAnimationFrame === 'function') requestAnimationFrame(positionLayer);
        else setTimeout(positionLayer, 16);
    }

    // ── speech bubble (the dare ladder surface) ──────────────────────────────

    function setAccent(band) {
        const color = bandColors[band] || bandColors.all;
        layer.style.setProperty('--hk-accent', color);
        avatar.style.setProperty('--hk-accent', color);
    }

    function speak(text, band, withAction = false) {
        bubbleText.textContent = text;
        if (band) setAccent(band);
        if (band && withAction) {
            bubbleAct.textContent = `→ ${band}`;
            bubbleAct.hidden = false;
            bubbleAct.dataset.band = band;
        } else {
            bubbleAct.hidden = true;
            bubbleAct.removeAttribute('data-band');
        }
        bubble.hidden = false;
        schedulePosition();
        bubble.classList.remove('hk-pop');
        // reflow to restart the pop animation
        void bubble.offsetWidth;
        bubble.classList.add('hk-pop');
        if (bubbleTimer) clearTimeout(bubbleTimer);
        bubbleTimer = setTimeout(hideBubble, BUBBLE_HIDE_MS);
    }

    function hideBubble() {
        bubble.hidden = true;
        if (bubbleTimer) {
            clearTimeout(bubbleTimer);
            bubbleTimer = null;
        }
    }

    function nagsActive() {
        return desktopNags && typeof Notification !== 'undefined' && Notification.permission === 'granted';
    }

    function maybeDesktopNag(text, band) {
        if (demoMode || !nagsActive()) return;
        if (document.visibilityState === 'visible') return; // already in your face
        try {
            new Notification('Horst-Kevin', { body: text, icon: 'hk.jpg', tag: `hk-${band}` });
        } catch {
            /* notifications can throw on some platforms; ignore */
        }
    }

    function deliver(items) {
        for (const item of items) {
            speak(item.text, item.band, item.type === 'dare');
            if (item.nag) maybeDesktopNag(item.text, item.band);
        }
    }

    function setMood(forced) {
        const mood = forced || moodOf(state);
        avatar.classList.toggle('is-fired', mood === 'fired');
        avatar.classList.toggle('is-stirring', mood === 'stirring');
        avatar.classList.toggle('is-dormant', mood === 'dormant');
        avatar.title = mood === 'fired'
            ? 'Horst-Kevin is judging you'
            : mood === 'stirring'
                ? 'Horst-Kevin is watching the bands'
                : 'Horst-Kevin is napping';
    }

    function step(recs, now, ctx) {
        const out = reduce(state, recs, now, {
            lang,
            meanness,
            panelOpen: !panel.hidden,
            ...ctx,
        });
        state = out.state;
        if (traceOn) console.log('[hk-trace]', out.trace);
        persist();
        deliver(out.speech);
        setMood();
        renderPanel();
        return out;
    }

    // ── confession + grudge panel ────────────────────────────────────────────

    // Keep the panel's selects/labels in sync with current language + settings.
    function syncChrome() {
        const u = uiPack(lang);
        const meanSel = panel.querySelector('.hk-meanness');
        if (meanSel) {
            meanSel.value = meanness;
            for (const opt of meanSel.options) {
                const lbl = u.meanLabels[opt.value];
                if (lbl) opt.textContent = lbl;
            }
        }
        const langSel = panel.querySelector('.hk-lang');
        if (langSel) langSel.value = lang;
        const foot = panel.querySelector('.hk-panel-foot-text');
        if (foot) foot.textContent = u.nag;
        const deskToggle = panel.querySelector('.hk-desktop');
        if (deskToggle) deskToggle.checked = desktopNags;
    }

    function renderPanel() {
        syncChrome();
        if (panel.hidden) return;
        const u = uiPack(lang);
        const now = Date.now();
        const { calls, grudges } = state;
        // Answered and void calls are left out of the honesty score.
        const graded = calls.filter((c) => c.verdict === 'banger' || c.verdict === 'dud');
        const bangers = graded.filter((c) => c.verdict === 'banger').length;
        const duds = graded.filter((c) => c.verdict === 'dud').length;
        const total = bangers + duds;
        const pct = total > 0 ? Math.round((bangers / total) * 100) : null;

        const recentGrudges = grudges.filter((g) => now - g.fadedAt < GRUDGE_WINDOW_MS);

        const dudList = graded
            .filter((c) => c.verdict === 'dud')
            .slice(-6)
            .reverse()
            .map((c) => `<li><span class="hk-tag" style="--hk-tag:${bandColors[c.band] || bandColors.all}">${c.band}</span> <span class="hk-time">${fmtTime(c.ts)}</span> <span class="hk-dud-note">${dudLine(lang, c.band)}</span></li>`)
            .join('');

        // Grudges grouped by band, worst offenders first.
        const byBand = new Map();
        for (const g of recentGrudges) byBand.set(g.band, (byBand.get(g.band) || 0) + 1);
        const grudgeRows = [...byBand.entries()]
            .sort((a, b) => b[1] - a[1])
            .slice(0, 6)
            .map(([band, n]) => `<li><span class="hk-tag" style="--hk-tag:${bandColors[band] || bandColors.all}">${band}</span> <span class="hk-grudge-n${n >= 3 ? ' is-repeat' : ''}">${u.ghosted(n)}</span></li>`)
            .join('');

        const honestyLine = pct == null
            ? `<span class="hk-muted">${u.noGraded}</span>`
            : u.honesty(total, bangers, duds, pct);

        panel.querySelector('.hk-panel-body').innerHTML = `
            <section class="hk-section">
                <h4>${u.confTitle}</h4>
                <p class="hk-honesty">${honestyLine}</p>
                ${pct != null ? `<div class="hk-bar"><div class="hk-bar-fill" style="width:${pct}%"></div></div>` : ''}
                ${dudList ? `<ul class="hk-list">${dudList}</ul>` : ''}
            </section>
            <section class="hk-section">
                <h4>${u.grudgeTitle}</h4>
                <p class="hk-grudge-head">${recentGrudges.length ? u.grudgeHead(recentGrudges.length) : `<span class="hk-muted">${u.cleanSlate}</span>`}</p>
                ${grudgeRows ? `<ul class="hk-list">${grudgeRows}</ul>` : ''}
            </section>`;
    }

    function fmtTime(ts) {
        const d = new Date(ts);
        return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
    }

    function togglePanel(force) {
        const open = force != null ? force : panel.hidden;
        panel.hidden = !open;
        avatar.setAttribute('aria-expanded', String(open));
        if (open) {
            renderPanel();
            schedulePosition();
        }
    }

    // ── networking ───────────────────────────────────────────────────────────

    async function refreshAlmanac(qth) {
        const now = Date.now();
        if (almanac.qth === qth && now - almanac.fetchedAt < TUNING.ALMANAC_REFRESH_MS) return;
        if (almanac.qth !== qth) almanac.data = null; // never gate with another area's almanac
        almanac.qth = qth;
        almanac.fetchedAt = now;
        try {
            const resp = await fetch(`/api/almanac?qth=${encodeURIComponent(qth)}`);
            if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
            const data = await resp.json();
            if (almanac.qth === qth) almanac.data = data;
        } catch (err) {
            // Fail open: without an almanac nothing is gated.
            if (almanac.qth === qth) almanac.data = null;
            if (traceOn) console.log('[hk-trace] almanac unavailable:', err?.message || err);
        }
    }

    function trackedBands() {
        return Object.keys(state.episodes);
    }

    async function fetchOnce() {
        if (demoMode) return; // demo drives the engine itself
        const qth = (getQth?.() || '').trim();
        if (!qth) {
            state = resync(state);
            persist();
            setMood();
            renderPanel();
            return;
        }
        if (abortCtl) abortCtl.abort();
        abortCtl = new AbortController();
        lastFetchAt = Date.now();
        void refreshAlmanac(qth);

        const surroundings = Boolean(getSurroundings?.());
        const params = new URLSearchParams();
        params.set('qth', qth);
        if (surroundings) params.set('surroundings', 'true');
        params.set('rings', 'auto');
        params.set('bands', [...dareBandsAt(Date.now())].join(','));
        params.set('max', String(TUNING.MAX_RECS));
        const tracked = trackedBands();
        if (tracked.length) params.set('include', tracked.join(','));

        try {
            const resp = await fetch(`/api/hot_bands?${params.toString()}`, { signal: abortCtl.signal });
            if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
            const data = await resp.json();
            const recs = Array.isArray(data?.recommendations) ? data.recommendations : [];
            lastGoodTs = Date.now();
            step(recs, Date.now(), {
                ctxKey: `${qth}|${surroundings ? 1 : 0}`,
                answeredBands: [getCurrentBand?.() || '', getRigBand?.() || ''],
                enabledBands: typeof getEnabledBands === 'function' ? getEnabledBands() : null,
                holding: Array.isArray(data?.holding) ? data.holding : [],
                almanac: almanac.qth === qth ? almanac.data : null,
                widened: Boolean(data?.area?.widened),
            });
        } catch (err) {
            if (err.name === 'AbortError') return;
            console.warn('horst-kevin hot_bands fetch failed:', err);
            // The next good poll sees the gap and resyncs quietly.
            if (lastGoodTs > 0 && Date.now() - lastGoodTs > TUNING.STALE_TIMEOUT_MS) setMood('dormant');
        }
    }

    function start() {
        stop();
        fetchOnce();
        pollTimer = setInterval(() => {
            if (document.visibilityState === 'visible') {
                fetchOnce();
            } else if (nagsActive() && Date.now() - lastFetchAt >= TUNING.HIDDEN_POLL_MS - 1000) {
                fetchOnce();
            }
        }, TUNING.POLL_MS);
    }

    function stop() {
        if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
        if (abortCtl) { abortCtl.abort(); abortCtl = null; }
    }

    // ── wiring ───────────────────────────────────────────────────────────────

    avatar.addEventListener('click', () => togglePanel());
    bubbleAct.addEventListener('click', () => {
        const band = bubbleAct.dataset.band;
        if (band && typeof onBandSwitch === 'function' && onBandSwitch(band) === true) {
            state = markAnswered(state, band);
            persist();
        }
        hideBubble();
        setMood();
    });
    // Sidebar hidden: the bubble sits on the map; its text reopens the sidebar
    // (the existing #show-sidebar path) so the picture is back in view.
    bubbleText.addEventListener('click', () => {
        if (layer.classList.contains('is-detached') && showSidebarBtn) showSidebarBtn.click();
    });

    panel.addEventListener('click', (e) => {
        if (e.target.closest('.hk-panel-close')) togglePanel(false);
    });
    panel.addEventListener('change', (e) => {
        if (e.target.classList.contains('hk-meanness')) {
            meanness = LINES.en.dare[e.target.value] ? e.target.value : 'buzzed';
            saveJSON(LS.meanness, meanness);
            renderPanel();
        } else if (e.target.classList.contains('hk-lang')) {
            lang = LINES[e.target.value] ? e.target.value : 'en';
            saveJSON(LS.lang, lang);
            renderPanel();
        } else if (e.target.classList.contains('hk-desktop')) {
            if (e.target.checked && typeof Notification !== 'undefined') {
                Notification.requestPermission().then((perm) => {
                    desktopNags = perm === 'granted';
                    saveJSON(LS.desktop, desktopNags);
                    e.target.checked = desktopNags;
                });
            } else {
                desktopNags = false;
                saveJSON(LS.desktop, false);
            }
        }
    });

    window.addEventListener('resize', schedulePosition);
    controls?.addEventListener('scroll', schedulePosition, { passive: true });
    controls?.addEventListener('transitionend', schedulePosition);
    document.getElementById('hide-sidebar')?.addEventListener('click', schedulePosition);
    showSidebarBtn?.addEventListener('click', schedulePosition);

    document.addEventListener('visibilitychange', () => {
        if (!demoMode && document.visibilityState === 'visible') fetchOnce();
    });

    // ── demo driver (?hk-demo) ───────────────────────────────────────────────
    //
    // Drives the real reducer with a synthetic 30 s poll clock through a short
    // scenario: a blip (silence), an opening that flaps for one poll and climbs
    // to rung 2, its rung-2 fade, an answered dare, and a hidden-tab gap (quiet
    // resync). Silent polls run fast; a poll that speaks pauses so the line can
    // be read. Writes nothing to localStorage (demoMode guards saveJSON).
    function runDemo() {
        demoMode = true;
        stop();
        state = createState();
        togglePanel(true);

        const P = TUNING.POLL_MS;
        const rec = (band) => ({ band, kind: 'surprise', reason: 'demo', spots_per_minute: 3.2 });
        const poll = (bands, extra = {}) => ({ bands, ...extra });
        const rep = (n, p) => Array.from({ length: n }, () => p);
        const script = [
            poll([]),                                        // first poll after load: silent
            { say: 'blip' }, poll(['15m']), poll([]), poll([]), // one-poll blip: never confirmed
            { say: 'open' }, ...rep(17, poll(['20m'])),      // 20m confirms on poll 3 → rung 0, rung 1 at 8 min
            { say: 'flap' }, poll([]), ...rep(26, poll(['20m'])), // one missing poll: grace, same episode → rung 2
            { say: 'fade' }, ...rep(11, poll([])),           // gone past the 5 min grace → rung-2 fade line + grudge
            ...rep(10, poll([])),
            { say: 'answer' }, ...rep(2, poll(['10m'])),     // 10m confirms (fast profile) → dare
            ...rep(12, poll([], { answered: ['10m'] })),     // you're on 10m, response omits it: no grudge
            { say: 'gap' }, poll(['15m'], { gapMs: 30 * 60_000 }), // tab hidden 30 min: quiet resync
            ...rep(3, poll(['15m'])),
        ];
        let dnow = Date.now();
        let i = 0;

        const tick = () => {
            if (i >= script.length) {
                speak(uiPack(lang).demoDone);
                renderPanel();
                return;
            }
            const item = script[i++];
            if (item.say) {
                speak(uiPack(lang).demo[item.say]);
                setTimeout(tick, 3500);
                return;
            }
            dnow += P + (item.gapMs || 0);
            const out = step(item.bands.map(rec), dnow, {
                ctxKey: 'demo',
                answeredBands: item.answered || [],
                enabledBands: null,
                holding: [],
                almanac: null,
                widened: false,
                panelOpen: true,
            });
            setTimeout(tick, out.speech.length ? 5500 : 60);
        };
        tick();
    }

    syncChrome();
    setMood();
    if (params.has('hk-demo')) {
        runDemo();
    } else {
        start();
    }

    return {
        refresh: () => { if (!demoMode && document.visibilityState === 'visible') fetchOnce(); },
        stop,
    };
}
