// Horst-Kevin — the little green dragon who heckles you onto the air.
//
// Three behaviours, all driven by polling /api/hot_bands (the same "is it
// worth turning on the radio" oracle the hot-band indicator uses):
//
//   1. Dare ladder    — when a band is worth it, Horst-Kevin nudges. The
//                        longer you ignore a *sustained* opening, the harder
//                        he escalates. If the opening fades, he de-escalates.
//   2. The grudge      — openings you never answered are remembered (localStorage)
//                        so he can hold them against you, repeat offenders first.
//   3. Eating his words — every dare is graded: did the opening actually persist?
//                        A public banger/lie scoreboard with the duds on display.
//
// All state is client-side; no backend changes. Mirrors hot-band-indicator.js.

import { bandColors } from './utils.js';

const POLL_INTERVAL_MS = 30_000;
const STALE_TIMEOUT_MS = 2 * 60_000;

// Dare ladder: ms an opening must stay continuously hot to reach each rung.
// Rung 0 fires immediately on first sighting.
const RUNG_THRESHOLDS_MS = [0, 3 * 60_000, 8 * 60_000, 20 * 60_000];

// A dare is graded a banger if the band is still hot this long after the call,
// otherwise it was a lie (Horst-Kevin got excited over nothing).
const VERDICT_AFTER_MS = 60_000;

const GRUDGE_WINDOW_MS = 7 * 24 * 60 * 60_000; // repeat-offender lookback
const MAX_GRUDGES = 60;
const MAX_CALLS = 60;
const BUBBLE_HIDE_MS = 14_000;

const LS = {
    calls: 'hk:calls',
    grudges: 'hk:grudges',
    meanness: 'hk:meanness',
    lang: 'hk:lang',
    desktop: 'hk:desktop',
};

// ── persistence ───────────────────────────────────────────────────────────

// Demo mode drives the engine with synthetic openings; it must never write to
// the operator's real grudge/confession history.
let demoMode = false;

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
                (B) => [`${B}'s still good from your grid! shame to miss it.`, `${B} keeps on giving. just saying. 🙂`, `${B}'s been lovely for a while now.`, `honestly ${B} from your spot is a treat right now.`, `${B}'s practically gift-wrapped for you.`, `you could work the world on ${B} right now, you know.`, `${B}'s been open so long it's getting comfortable.`, `it'd be a kindness to ${B} to actually answer it.`],
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
                (B) => [`${B} ist immer noch gut aus deinem Locator! schade drum.`, `${B} gibt einfach weiter. nur so. 🙂`, `${B} ist schon 'ne ganze Weile richtig nett.`, `ehrlich, ${B} von deinem Standort ist gerade ein Genuss.`, `${B} ist praktisch Geschenkpapier für dich.`, `du könntest gerade die Welt auf ${B} arbeiten, weißt du.`, `${B} ist so lange offen, es macht's sich schon gemütlich.`, `wäre nett zu ${B}, mal zu antworten.`],
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

function pick(arr) {
    return arr[Math.floor(Math.random() * arr.length)] || arr[0];
}

function langPack(lang) {
    return LINES[lang] || LINES.en;
}

function dareLine(lang, meanness, rung, band) {
    const ladder = langPack(lang).dare[meanness] || langPack(lang).dare.buzzed;
    return pick(ladder[Math.min(rung, ladder.length - 1)](band));
}

function fadeLine(lang, meanness, band) {
    const fn = langPack(lang).fade[meanness] || langPack(lang).fade.buzzed;
    return pick(fn(band));
}

function dudLine(lang, band) {
    return pick(langPack(lang).dud(band));
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
    },
};

function uiPack(lang) {
    return UI[lang] || UI.en;
}

// ── main ────────────────────────────────────────────────────────────────────

export function initHorstKevin({ getTarget, getSurroundings, getCurrentBand, onBandSwitch }) {
    const stage = document.getElementById('horst-kevin');
    if (!stage) return null;

    const avatar = stage.querySelector('.hk-avatar');
    const bubble = stage.querySelector('.hk-bubble');
    const bubbleText = stage.querySelector('.hk-bubble-text');
    const bubbleAct = stage.querySelector('.hk-bubble-action');
    const panel = stage.querySelector('.hk-panel');

    let pollTimer = null;
    let abortCtl = null;
    let lastGoodTs = 0;
    let bubbleTimer = null;

    // Live, in-memory tracking of currently-hot openings, keyed by band.
    // { firstSeenAt, lastSeenAt, rung, answered, peakRate, kind, reason }
    const tracked = new Map();

    let calls = loadJSON(LS.calls, []);
    let grudges = loadJSON(LS.grudges, []);
    let meanness = loadJSON(LS.meanness, 'buzzed');
    let lang = loadJSON(LS.lang, defaultLang());
    let desktopNags = loadJSON(LS.desktop, false) === true;
    if (!LINES.en.dare[meanness]) meanness = 'buzzed';
    if (!LINES[lang]) lang = 'en';

    // ── speech bubble (the dare ladder surface) ──────────────────────────────

    function speak(text, band) {
        bubbleText.textContent = text;
        if (band) {
            const color = bandColors[band] || bandColors.all;
            stage.style.setProperty('--hk-accent', color);
            bubbleAct.textContent = `→ ${band}`;
            bubbleAct.hidden = false;
            bubbleAct.dataset.band = band;
        } else {
            bubbleAct.hidden = true;
            bubbleAct.removeAttribute('data-band');
        }
        bubble.hidden = false;
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

    function maybeDesktopNag(text) {
        if (!desktopNags) return;
        if (typeof Notification === 'undefined' || Notification.permission !== 'granted') return;
        if (document.visibilityState === 'visible') return; // already in your face
        try {
            new Notification('Horst-Kevin', { body: text, icon: 'hk.jpg', tag: 'horst-kevin' });
        } catch {
            /* notifications can throw on some platforms; ignore */
        }
    }

    function setMood() {
        const active = [...tracked.values()].some((t) => t.rung >= 0 && !t.answered);
        const stirring = tracked.size > 0;
        avatar.classList.toggle('is-fired', active);
        avatar.classList.toggle('is-stirring', stirring && !active);
        avatar.classList.toggle('is-dormant', !stirring);
        avatar.title = active
            ? 'Horst-Kevin is judging you'
            : stirring
                ? 'Horst-Kevin is watching the bands'
                : 'Horst-Kevin is napping';
    }

    // ── grudge + confession bookkeeping ──────────────────────────────────────

    function recordCall(band, rung, rate, now) {
        calls.push({ id: `${now}-${band}`, band, rung, rate: rate || 0, ts: now, verdict: 'pending' });
        if (calls.length > MAX_CALLS) calls = calls.slice(-MAX_CALLS);
        saveJSON(LS.calls, calls);
    }

    // Grade pending dares: still hot after VERDICT_AFTER_MS → banger, else lie.
    function gradePending(currentBands, now) {
        let changed = false;
        for (const c of calls) {
            if (c.verdict !== 'pending') continue;
            if (now - c.ts < VERDICT_AFTER_MS) continue;
            c.verdict = currentBands.has(c.band) ? 'banger' : 'dud';
            changed = true;
            if (c.verdict === 'dud') {
                // own it, quietly, only if nothing louder is happening
                if (bubble.hidden) speak(dudLine(lang, c.band));
            }
        }
        if (changed) saveJSON(LS.calls, calls);
        return changed;
    }

    function grudgeCountFor(band, now) {
        return grudges.filter((g) => g.band === band && now - g.fadedAt < GRUDGE_WINDOW_MS).length;
    }

    function addGrudge(entry, now) {
        grudges.push(entry);
        grudges = grudges.filter((g) => now - g.fadedAt < GRUDGE_WINDOW_MS);
        if (grudges.length > MAX_GRUDGES) grudges = grudges.slice(-MAX_GRUDGES);
        saveJSON(LS.grudges, grudges);
    }

    // ── the poll → dare-ladder state machine ─────────────────────────────────

    function ingest(recs, now) {
        const currentBand = demoMode ? '' : (getCurrentBand?.() || '').toLowerCase();
        const present = new Set();
        let topDare = null; // { rung, text, band } — loudest new escalation this tick

        for (const rec of recs) {
            const band = (rec.band || '').toLowerCase();
            if (!band) continue;
            present.add(band);

            let t = tracked.get(band);
            if (!t) {
                t = { firstSeenAt: now, lastSeenAt: now, rung: -1, answered: false, peakRate: 0, kind: rec.kind, reason: rec.reason };
                tracked.set(band, t);
            }
            t.lastSeenAt = now;
            t.peakRate = Math.max(t.peakRate, rec.spots_per_minute || 0);
            t.kind = rec.kind;
            t.reason = rec.reason;
            if (currentBand && currentBand === band) t.answered = true;

            if (t.answered) continue; // you showed up; no heckling

            // Which rung does the elapsed sustained time earn?
            const elapsed = now - t.firstSeenAt;
            let desired = 0;
            for (let i = RUNG_THRESHOLDS_MS.length - 1; i >= 0; i--) {
                if (elapsed >= RUNG_THRESHOLDS_MS[i]) { desired = i; break; }
            }

            if (desired > t.rung) {
                const wasFirst = t.rung < 0;
                t.rung = desired;
                if (wasFirst) recordCall(band, desired, rec.spots_per_minute, now); // one call per opening
                if (!topDare || desired > topDare.rung) {
                    topDare = { rung: desired, band, text: dareLine(lang, meanness, desired, band) };
                }
            }
        }

        // Openings that faded: grudge the unanswered ones, then forget them.
        for (const [band, t] of [...tracked.entries()]) {
            if (present.has(band)) continue;
            if (!t.answered && t.rung >= 0) {
                addGrudge({ band, kind: t.kind, reason: t.reason, peakRate: Math.round((t.peakRate || 0) * 100) / 100, firstSeenAt: t.firstSeenAt, fadedAt: now }, now);
                if (!topDare && bubble.hidden) speak(fadeLine(lang, meanness, band));
            }
            tracked.delete(band);
        }

        if (topDare) {
            // Salt the top dare with grudge history if you're a repeat offender.
            const n = grudgeCountFor(topDare.band, now);
            let text = topDare.text;
            if (n >= 2 && topDare.rung >= 2) {
                text += saltLine(lang, n, topDare.band, meanness);
            }
            speak(text, topDare.band);
            maybeDesktopNag(text);
        }

        gradePending(present, now);
        setMood();
        renderPanel();
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
        const graded = calls.filter((c) => c.verdict !== 'pending');
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
        if (open) renderPanel();
    }

    // ── networking ───────────────────────────────────────────────────────────

    async function fetchOnce() {
        if (demoMode) return; // demo drives the engine itself
        const target = (getTarget?.() || '').trim();
        if (!target) {
            tracked.clear();
            setMood();
            renderPanel();
            return;
        }
        if (abortCtl) abortCtl.abort();
        abortCtl = new AbortController();

        const params = new URLSearchParams();
        params.set('target', target);
        if (getSurroundings?.()) params.set('surroundings', 'true');
        const current = (getCurrentBand?.() || '').toLowerCase();
        if (current && current !== 'all') params.set('current_band', current);

        try {
            const resp = await fetch(`/api/hot_bands?${params.toString()}`, { signal: abortCtl.signal });
            if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
            const data = await resp.json();
            const recs = Array.isArray(data?.recommendations) ? data.recommendations : [];
            lastGoodTs = Date.now();
            ingest(recs, Date.now());
        } catch (err) {
            if (err.name === 'AbortError') return;
            console.warn('horst-kevin hot_bands fetch failed:', err);
            if (lastGoodTs > 0 && Date.now() - lastGoodTs > STALE_TIMEOUT_MS) {
                tracked.clear();
                setMood();
            }
        }
    }

    function start() {
        stop();
        fetchOnce();
        pollTimer = setInterval(() => {
            if (document.visibilityState !== 'visible') return;
            fetchOnce();
        }, POLL_INTERVAL_MS);
    }

    function stop() {
        if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
        if (abortCtl) { abortCtl.abort(); abortCtl = null; }
    }

    // ── wiring ───────────────────────────────────────────────────────────────

    avatar.addEventListener('click', () => togglePanel());
    bubbleAct.addEventListener('click', () => {
        const band = bubbleAct.dataset.band;
        if (band && typeof onBandSwitch === 'function') {
            onBandSwitch(band);
            const t = tracked.get(band);
            if (t) t.answered = true;
        }
        hideBubble();
        setMood();
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

    document.addEventListener('visibilitychange', () => {
        if (!demoMode && document.visibilityState === 'visible') fetchOnce();
    });

    // ── demo driver (?hk-demo) ───────────────────────────────────────────────
    //
    // Drives the real engine with a synthetic accelerated clock so every rung,
    // a banger, a dud, two grudges and the smug fade all fire in ~12s. Writes
    // nothing to localStorage (demoMode guards saveJSON); reload to reset.
    function runDemo() {
        demoMode = true;
        stop();
        calls = [];
        grudges = [];
        tracked.clear();
        togglePanel(true);

        const STEP_MS = 6000; // wall-clock between steps — slow enough to read each line
        const ADV_MS = 7 * 60_000; // 7 simulated minutes per step → crosses every rung
        let dnow = Date.now();
        // Each step lists the bands "currently worth it". 20m runs the full ladder
        // (rung 0→3) then fades; 15m flashes twice (two duds + two grudges), then on
        // its third opening climbs to rung 2 — by which point it's a repeat offender,
        // triggering the grudge "salt" line — before fading into a third grudge.
        const timeline = [
            ['20m'],            //  20m rung0
            ['20m', '15m'],     //  20m rung1 · 15m rung0 (A) · 20m call→banger
            ['20m'],            //  20m rung2 · 15m fades → grudge#1 · A→dud
            ['20m', '15m'],     //  20m rung3 · 15m rung0 (B)
            ['20m'],            //  15m fades → grudge#2 · B→dud
            ['20m', '15m'],     //  15m rung0 (C)
            ['15m'],            //  20m fades → grudge(20m) · 15m rung1
            ['15m'],            //  15m rung2 → repeat-offender salt line
            ['15m'],            //  15m rung3 · C→banger
            [],                 //  15m fades → grudge#3 (repeat-flagged)
        ];
        let i = 0;

        const tick = () => {
            if (i >= timeline.length) {
                speak(uiPack(lang).demoDone);
                renderPanel();
                return;
            }
            const recs = timeline[i].map((b) => ({ band: b, kind: 'surprise', reason: 'demo', spots_per_minute: 3.2 }));
            ingest(recs, dnow);
            dnow += ADV_MS;
            i++;
            setTimeout(tick, STEP_MS);
        };
        tick();
    }

    let isDemo = false;
    try {
        isDemo = new URLSearchParams(window.location.search || '').has('hk-demo');
    } catch {
        isDemo = false;
    }

    syncChrome();
    setMood();
    if (isDemo) {
        runDemo();
    } else {
        start();
    }

    return {
        refresh: () => { if (!demoMode && document.visibilityState === 'visible') fetchOnce(); },
        stop,
    };
}
