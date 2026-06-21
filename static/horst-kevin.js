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
                (B) => [`hey, ${B} looks promising — might be worth a listen.`, `${B}'s perking up. whenever you fancy it.`, `psst — ${B}'s starting to move.`, `little opening on ${B}, if you're curious.`],
                (B) => [`${B}'s holding up nicely. no rush.`, `still going on ${B}. the rig's right there if you want it.`, `${B} keeps looking good. just a thought.`, `nice steady ${B} here. up to you.`],
                (B) => [`${B}'s still good from your grid! shame to miss it.`, `${B} keeps on giving. just saying. 🙂`, `${B}'s been lovely for a while now.`, `honestly ${B} from your spot is a treat right now.`],
                (B) => [`${B} had a lovely run. catch the next one!`, `that was a nice ${B} opening. there'll be others.`, `${B} faded, but no worries — they come back.`, `you missed a sweet ${B} window. happens to everyone.`],
            ],
            buzzed: [
                (B) => [`${B}'s awake. just sayin'.`, `something's stirring on ${B}. don't make me beg.`, `${B} just lit up. tick tock.`, `oi. ${B}. it's happening.`, `${B}'s got a pulse. you got an excuse?`],
                (B) => [`${B}'s STILL going. you gonna do something or just watch me?`, `still hot on ${B}. the radio's right there, champ.`, `${B} hasn't quit. why have you?`, `${B}'s holding. unlike my patience.`],
                (B) => [`${B} workable from YOUR grid. from YOUR grid, Kevin.`, `${B}'s been open for ages now. I'm not gonna ask again. (I will.)`, `you know ${B}'s still going, right? RIGHT?`, `${B} from your own backyard and you're reading this. bold.`],
                (B) => [`openings came and went on ${B}. I watched 'em leave. told 'em you were busy.`, `cool. ${B} was wide open. hope the chair's comfy.`, `${B} gave you twenty minutes. you gave it nothing.`, `that's a museum-grade ${B} opening you're ignoring. respect, almost.`],
            ],
            drill: [
                (B) => [`${B}. ON THE AIR. NOW.`, `${B} IS LIVE. WHAT ARE YOU WAITING FOR.`, `MOVEMENT ON ${B}. RESPOND.`, `EYES ON ${B}, OPERATOR.`],
                (B) => [`${B} STILL OPEN. WHY ARE YOU STILL READING THIS.`, `MOVE. ${B} WON'T WORK ITSELF.`, `${B} HOLDING. YOU ARE NOT. EXPLAIN.`, `${B} IS RIGHT THERE. ACQUIRE TARGET.`],
                (B) => [`${B} FROM YOUR OWN GRID, SOLDIER. UNACCEPTABLE.`, `${B}. KEY DOWN. THAT'S AN ORDER.`, `${B} OPEN FOR MINUTES. THIS IS A DERELICTION.`, `${B} IN YOUR SECTOR. ENGAGE.`],
                (B) => [`STAND DOWN. ${B} IS GONE. THAT ONE'S ON YOU.`, `${B} CLOSED. WE DO NOT SPEAK OF THIS.`, `OPPORTUNITY ${B}: LOST. LOG THE FAILURE.`, `${B} WINDOW: CLOSED. DEBRIEF YOURSELF.`],
            ],
        },
        fade: {
            soft: (B) => [`${B}'s quieted down. maybe next time.`, `${B} drifted off. no biggie.`],
            buzzed: (B) => [`and… ${B}'s gone. classic.`, `${B}? closed. you snooze, you lose.`],
            drill: (B) => [`${B} LOST. DOCUMENT THE FAILURE.`, `${B} GONE. NOTED. PERMANENTLY.`],
        },
        dud: (B) => [`yeah ${B} was dead by the time you looked. my bad.`, `called ${B} too early. I got excited.`, `${B}? that one fizzled. don't @ me.`, `okay ${B} was a false alarm. sue me.`, `${B} ghosted us both. awkward.`, `I may have oversold ${B}. slightly.`],
        salt: (n, B, mean) => mean === 'drill'
            ? ` ${n} PRIOR OFFENSES ON ${B.toUpperCase()}.`
            : ` (${n} times you've ghosted ${B} lately, by the way.)`,
    },
    de: {
        dare: {
            soft: [
                (B) => [`hey, ${B} sieht vielversprechend aus — mal reinhören?`, `${B} zieht an. wann immer du magst.`, `psst — auf ${B} bewegt sich was.`, `kleine Öffnung auf ${B}, falls du neugierig bist.`],
                (B) => [`${B} hält sich schön. keine Eile.`, `läuft noch auf ${B}. das Gerät steht bereit.`, `${B} sieht weiter gut aus. nur so ein Gedanke.`, `schön gleichmäßig hier auf ${B}. ganz wie du willst.`],
                (B) => [`${B} ist immer noch gut aus deinem Locator! schade drum.`, `${B} gibt einfach weiter. nur so. 🙂`, `${B} ist schon 'ne ganze Weile richtig nett.`, `ehrlich, ${B} von deinem Standort ist gerade ein Genuss.`],
                (B) => [`${B} hatte einen schönen Lauf. nimm die nächste mit!`, `das war eine nette ${B}-Öffnung. es kommen weitere.`, `${B} ist abgeflaut, aber kein Stress — die kommen wieder.`, `du hast ein feines ${B}-Fenster verpasst. passiert jedem.`],
            ],
            buzzed: [
                (B) => [`${B} ist wach. nur so.`, `auf ${B} tut sich was. lass mich nicht betteln.`, `${B} leuchtet auf. tick tack.`, `ey. ${B}. es passiert.`, `${B} hat 'nen Puls. und du 'ne Ausrede?`],
                (B) => [`${B} läuft IMMER noch. machst du was oder guckst du nur zu?`, `weiter heiß auf ${B}. das Funkgerät steht direkt da, Großer.`, `${B} gibt nicht auf. warum du?`, `${B} hält durch. anders als meine Geduld.`],
                (B) => [`${B} aus DEINEM Locator machbar. aus DEINEM, Kevin.`, `${B} ist seit Ewigkeiten offen. ich frag nicht nochmal. (doch.)`, `du weißt, dass ${B} noch läuft, oder? ODER?`, `${B} direkt vor der Haustür und du liest das hier. mutig.`],
                (B) => [`Öffnungen kamen und gingen auf ${B}. ich hab zugesehen. hab gesagt, du hast zu tun.`, `super. ${B} war sperrangelweit offen. hoffentlich sitzt du bequem.`, `${B} hat dir zwanzig Minuten gegeben. du gabst nichts zurück.`, `das ist eine museumsreife ${B}-Öffnung, die du ignorierst. fast Respekt.`],
            ],
            drill: [
                (B) => [`${B}. AUF SENDUNG. SOFORT.`, `${B} IST LIVE. WORAUF WARTEST DU.`, `BEWEGUNG AUF ${B}. REAGIEREN.`, `AUGEN AUF ${B}, FUNKER.`],
                (B) => [`${B} IMMER NOCH OFFEN. WARUM LIEST DU DAS NOCH.`, `BEWEGUNG. ${B} ARBEITET NICHT VON ALLEIN.`, `${B} HÄLT. DU NICHT. ERKLÄRUNG.`, `${B} IST GENAU DA. ZIEL ERFASSEN.`],
                (B) => [`${B} AUS DEINEM EIGENEN LOCATOR, SOLDAT. INAKZEPTABEL.`, `${B}. TASTE RUNTER. DAS IST EIN BEFEHL.`, `${B} SEIT MINUTEN OFFEN. DAS IST PFLICHTVERGESSEN.`, `${B} IN DEINEM SEKTOR. ANGREIFEN.`],
                (B) => [`RÜCKZUG. ${B} IST WEG. DAS GEHT AUF DEINE KAPPE.`, `${B} GESCHLOSSEN. WIR REDEN NICHT DARÜBER.`, `CHANCE ${B}: VERLOREN. VERSAGEN PROTOKOLLIEREN.`, `FENSTER ${B}: ZU. SELBST-DEBRIEFING.`],
            ],
        },
        fade: {
            soft: (B) => [`${B} ist ruhiger geworden. vielleicht nächstes Mal.`, `${B} hat sich verabschiedet. halb so wild.`],
            buzzed: (B) => [`und… ${B} ist weg. typisch.`, `${B}? zu. wer zu spät kommt…`],
            drill: (B) => [`${B} VERLOREN. VERSAGEN DOKUMENTIEREN.`, `${B} WEG. NOTIERT. FÜR IMMER.`],
        },
        dud: (B) => [`ja, ${B} war schon tot, als du geguckt hast. mein Fehler.`, `${B} zu früh gerufen. ich war aufgeregt.`, `${B}? ist verpufft. nicht meckern.`, `okay, ${B} war ein Fehlalarm. verklag mich.`, `${B} hat uns beide versetzt. peinlich.`, `ich hab ${B} vielleicht leicht überverkauft.`],
        salt: (n, B, mean) => mean === 'drill'
            ? ` ${n} FRÜHERE VERSTÖSSE AUF ${B.toUpperCase()}.`
            : ` (${n}× hast du ${B} zuletzt versetzt, nur nebenbei.)`,
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
        meanLabels: { soft: 'Supportive', buzzed: 'Buzzed', drill: 'Drill Sergeant' },
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
        meanLabels: { soft: 'Aufmunternd', buzzed: 'Angeheitert', drill: 'Ausbilder' },
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

        const STEP_MS = 2200;
        const ADV_MS = 7 * 60_000; // 7 simulated minutes per step → crosses every rung
        let dnow = Date.now();
        // Each step lists the bands "currently worth it". 20m runs the full
        // ladder; 15m flashes once then dies (→ a dud + a grudge).
        const timeline = [['20m'], ['20m', '15m'], ['20m'], ['20m'], []];
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
