// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { resetHotBandsClient } from './hot-bands-client.js';
import {
    TUNING,
    reduce,
    createState,
    markAnswered,
    moodOf,
    resync,
    isUsualNow,
    usualShareNow,
    almanacSlot,
    dareBandsAt,
    isDareBand,
    isHoldingOpen,
    bandProfile,
    initHorstKevin,
} from './horst-kevin.js';

const P = TUNING.POLL_MS;
const MIN = 60_000;
// 1 Oct 2026, 12:00 UTC: outside the 6m season.
const T0 = Date.UTC(2026, 9, 1, 12, 0, 0);

const rec = (band, kind = 'surprise', spm = 3.2) => ({ band, kind, reason: 'test', spots_per_minute: spm });

// Drive the reducer through a list of polls. Each poll: { bands, kind, dt, ctx }.
// Returns the final state, every speech item (with its time) and every trace.
function run(polls, { state = createState(), start = T0, ctx = {} } = {}) {
    let s = state;
    let t = start;
    const speech = [];
    const traces = [];
    for (const p of polls) {
        t += p.dt ?? P;
        const recs = (p.bands || []).map((b) => rec(b, p.kind, p.spm));
        const out = reduce(s, recs, t, { ctxKey: 'JO32|0', rand: () => 0, ...ctx, ...(p.ctx || {}) });
        s = out.state;
        for (const x of out.speech) speech.push({ ...x, t });
        traces.push(out.trace);
    }
    return { state: s, speech, traces, t };
}

const rep = (n, p) => Array.from({ length: n }, () => p);
const warm = { bands: [] }; // the first poll after load is always silent

describe('reduce: episode lifecycle', () => {
    it('stays silent on a one-poll blip: no speech, call or grudge', () => {
        const { state, speech } = run([warm, { bands: ['20m'] }, ...rep(20, { bands: [] })]);
        expect(speech).toEqual([]);
        expect(state.calls).toEqual([]);
        expect(state.grudges).toEqual([]);
        expect(state.episodes).toEqual({});
    });

    it('needs 3 polls on 20m and 2 on 10m before the first dare', () => {
        const slow = run([warm, ...rep(2, { bands: ['20m'] })]);
        expect(slow.speech).toEqual([]);
        expect(moodOf(slow.state)).toBe('stirring');
        const slow3 = run([{ bands: ['20m'] }], { state: slow.state, start: slow.t });
        expect(slow3.speech.map((x) => [x.type, x.rung])).toEqual([['dare', 0]]);
        expect(moodOf(slow3.state)).toBe('fired');

        const fast = run([warm, ...rep(2, { bands: ['10m'] })]);
        expect(fast.speech.map((x) => [x.type, x.band, x.rung])).toEqual([['dare', '10m', 0]]);
    });

    it('does not let extra refresh() polls seconds apart rush the debounce', () => {
        const a = run([warm, { bands: ['20m'] }, { bands: ['20m'], dt: 2000 }, { bands: ['20m'], dt: 2000 }]);
        expect(a.speech).toEqual([]);
        expect(a.state.episodes['20m'].seenPolls).toBe(1);
        expect(a.state.episodes['20m'].confirmed).toBe(false);
        expect(a.state.polls).toHaveLength(2); // the extra polls replace the last record
        // Only after real poll spacing does it confirm (3 counted sightings on 20m).
        const b = run(rep(2, { bands: ['20m'] }), { state: a.state, start: a.t });
        expect(b.speech.map((x) => [x.type, x.band, x.rung])).toEqual([['dare', '20m', 0]]);

        // Right after load too: a refresh() poll a second after the silent first
        // poll does not confirm a fast band.
        const c = run([{ bands: ['10m'] }, { bands: ['10m'], dt: 1000 }, { bands: ['10m'], dt: 1000 }]);
        expect(c.speech).toEqual([]);
        expect(c.state.episodes['10m'].confirmed).toBe(false);
    });

    it('keeps the episode through a one-poll flap', () => {
        const a = run([warm, ...rep(3, { bands: ['20m'] })]);
        const ep = a.state.episodes['20m'];
        const b = run([{ bands: [] }, ...rep(2, { bands: ['20m'] })], { state: a.state, start: a.t });
        expect(b.speech).toEqual([]);
        expect(b.state.episodes['20m'].firstSeenAt).toBe(ep.firstSeenAt);
        expect(b.state.episodes['20m'].callId).toBe(ep.callId);
        expect(b.state.grudges).toEqual([]);
    });

    it('focusing the band while the response omits it: no grudge, verdict answered', () => {
        const a = run([warm, ...rep(3, { bands: ['20m'] })]);
        const b = run(rep(20, { bands: [], ctx: { answeredBands: ['20m'] } }), { state: a.state, start: a.t });
        expect(b.speech).toEqual([]);
        expect(b.state.episodes['20m'].answered).toBe(true);
        expect(b.state.calls[0].verdict).toBe('answered');
        // You move on; the opening ends later — still no grudge and no fade line.
        const c = run(rep(15, { bands: [] }), { state: b.state, start: b.t });
        expect(c.speech).toEqual([]);
        expect(c.state.grudges).toEqual([]);
        expect(c.state.episodes['20m']).toBeUndefined();
    });

    it('counts the rig band as answered', () => {
        const a = run([warm, ...rep(3, { bands: ['20m'] })]);
        const b = run([{ bands: ['20m'], ctx: { answeredBands: ['all', '20m'] } }], { state: a.state, start: a.t });
        expect(b.state.episodes['20m'].answered).toBe(true);
        expect(b.state.calls[0].verdict).toBe('answered');
        expect(moodOf(b.state)).toBe('stirring');
    });

    it('markAnswered answers the episode and its pending call', () => {
        const a = run([warm, ...rep(3, { bands: ['20m'] })]);
        const s = markAnswered(a.state, '20M');
        expect(s.episodes['20m'].answered).toBe(true);
        expect(s.calls[0].verdict).toBe('answered');
        expect(a.state.episodes['20m'].answered).toBe(false); // pure
    });

    it('never speaks the "gone" rung while the band is present, and caps the ladder at rung 2', () => {
        const { speech, state } = run([warm, ...rep(200, { bands: ['20m'] })]);
        expect(speech.map((x) => [x.type, x.rung])).toEqual([['dare', 0], ['dare', 1], ['dare', 2]]);
        expect(state.episodes['20m'].rung).toBe(2);
    });

    it('climbs on the per-band timings (20m: 0 / 8 / 20 min observed)', () => {
        const { speech } = run([warm, ...rep(60, { bands: ['20m'] })]);
        const first = speech[0].t;
        expect(speech[1].t - first).toBeGreaterThanOrEqual(7 * MIN);
        expect(speech[1].t - first).toBeLessThanOrEqual(8 * MIN);
        expect(speech[2].t - first).toBeGreaterThanOrEqual(19 * MIN);
    });

    it('speaks the rung-3 "gone" line as the fade of a rung-2 episode, with a grudge', () => {
        const a = run([warm, ...rep(45, { bands: ['20m'] })]);
        expect(a.state.episodes['20m'].rung).toBe(2);
        const b = run(rep(11, { bands: [] }), { state: a.state, start: a.t });
        expect(b.speech).toHaveLength(1);
        expect(b.speech[0]).toMatchObject({ type: 'fade', band: '20m', rung: 2 });
        expect(b.speech[0].text).toMatch(/openings came and went on 20m/);
        expect(b.state.grudges).toHaveLength(1);
        // Fade only after the grace period.
        expect(b.speech[0].t - a.t).toBeGreaterThanOrEqual(TUNING.FADE_GRACE_MS);
    });

    it('uses the plain fade line (no grudge) for a rung-0 episode', () => {
        const a = run([warm, ...rep(3, { bands: ['20m'] })]);
        const b = run(rep(11, { bands: [] }), { state: a.state, start: a.t });
        expect(b.speech.map((x) => [x.type, x.rung])).toEqual([['fade', 0]]);
        expect(b.state.grudges).toEqual([]);
    });

    it('continues an episode that returns within the merge window (no new rung-0 dare)', () => {
        const a = run([warm, ...rep(3, { bands: ['20m'] })]);
        const b = run(rep(11, { bands: [] }), { state: a.state, start: a.t });
        expect(b.state.recent['20m']).toBeDefined();
        const c = run(rep(4, { bands: ['20m'] }), { state: b.state, start: b.t });
        expect(c.speech.filter((x) => x.type === 'dare' && x.rung === 0)).toEqual([]);
        expect(c.state.episodes['20m'].callId).toBe(a.state.episodes['20m'].callId);
    });
});

describe('reduce: honest time', () => {
    it('resyncs quietly after a 30 min gap', () => {
        const a = run([warm, ...rep(20, { bands: ['20m'] })]);
        const b = run([{ bands: [], dt: 30 * MIN }], { state: a.state, start: a.t });
        expect(b.speech).toEqual([]);
        expect(b.traces[0].silent).toMatch(/gap/);
        expect(b.state.grudges).toEqual([]);
        expect(b.state.episodes).toEqual({});
        expect(b.state.calls[0].verdict).toBe('void');
        // Cooldown from when it was last seen, so it has run out after the gap on 10m…
        expect(b.state.cooldownUntil['20m']).toBe(a.t + bandProfile('20m').cooldownMs);
    });

    it('resets quietly on a QTH change', () => {
        const a = run([warm, ...rep(20, { bands: ['20m'] })]);
        const b = run([{ bands: ['15m'], ctx: { ctxKey: 'JN58|0' } }], { state: a.state, start: a.t });
        expect(b.speech).toEqual([]);
        expect(b.traces[0].silent).toBe('area changed');
        expect(b.state.grudges).toEqual([]);
        expect(Object.keys(b.state.episodes)).toEqual(['15m']);
        expect(b.state.calls[0].verdict).toBe('void');
    });

    it('is silent on the first poll after load', () => {
        const { speech, traces } = run([{ bands: ['20m', '10m'] }]);
        expect(speech).toEqual([]);
        expect(traces[0].silent).toBe('first poll');
    });

    it('voids a stale call restored from storage', () => {
        const state = createState({ calls: [{ id: 'old', band: '20m', rung: 0, rate: 1, ts: T0 - 60 * MIN, verdict: 'pending' }] });
        const { state: s } = run([warm], { state });
        expect(s.calls[0].verdict).toBe('void');
    });

    it('voids a restored call whose window has too little poll coverage', () => {
        const state = createState({ calls: [{ id: 'recent', band: '20m', rung: 0, rate: 1, ts: T0 - 8 * MIN, verdict: 'pending' }] });
        const { state: s } = run(rep(6, { bands: ['20m'] }), { state });
        expect(s.calls[0].verdict).toBe('void');
    });

    it('grades a sustained opening a banger after its window', () => {
        const { state } = run([warm, ...rep(25, { bands: ['20m'] })]);
        expect(state.calls[0].verdict).toBe('banger');
    });

    it('grades a dare that vanished at once a dud (its own grace does not save it)', () => {
        const a = run([warm, ...rep(3, { bands: ['20m'] })]);
        const b = run(rep(21, { bands: [] }), { state: a.state, start: a.t, ctx: { panelOpen: true } });
        expect(b.state.calls[0].verdict).toBe('dud');
        expect(b.speech.map((x) => x.type)).toEqual(['fade', 'dud']);
    });

    it('does not speak dud lines while the panel is closed', () => {
        const a = run([warm, ...rep(3, { bands: ['20m'] })]);
        const b = run(rep(21, { bands: [] }), { state: a.state, start: a.t });
        expect(b.state.calls[0].verdict).toBe('dud');
        expect(b.speech.map((x) => x.type)).toEqual(['fade']);
    });
});

describe('reduce: scope and speech budget', () => {
    it('records only the spoken call on a multi-band tick', () => {
        const { state, speech } = run([warm, ...rep(3, { bands: ['20m', '15m'] })]);
        expect(speech).toHaveLength(1);
        expect(state.calls).toHaveLength(1);
        expect(state.calls[0].band).toBe(speech[0].band);
        const other = speech[0].band === '20m' ? '15m' : '20m';
        expect(state.episodes[other].rung).toBe(-1);
        expect(state.episodes[other].spoken).toBe(false);
    });

    it('ignores a disabled band', () => {
        const { state, speech } = run([warm, ...rep(10, { bands: ['20m'] })], { ctx: { enabledBands: new Set(['15m']) } });
        expect(speech).toEqual([]);
        expect(state.episodes).toEqual({});
    });

    it('drops an episode quietly when its band gets disabled', () => {
        const a = run([warm, ...rep(3, { bands: ['20m'] })]);
        const b = run(rep(12, { bands: ['20m'], ctx: { enabledBands: new Set(['15m']) } }), { state: a.state, start: a.t });
        expect(b.speech).toEqual([]);
        expect(b.state.episodes).toEqual({});
        expect(b.state.grudges).toEqual([]);
        expect(b.state.calls[0].verdict).toBe('void');
    });

    it('never dares 60m or 160m, and 6m only in season', () => {
        expect(run([warm, ...rep(10, { bands: ['60m', '160m', '6m'] })]).speech).toEqual([]);
        const june = Date.UTC(2026, 5, 20, 12);
        const { speech } = run([warm, ...rep(3, { bands: ['6m'] })], { start: june });
        expect(speech.map((x) => [x.band, x.rung])).toEqual([['6m', 0]]);
        expect(dareBandsAt(june).has('6m')).toBe(true);
        expect(dareBandsAt(T0).has('6m')).toBe(false);
        expect(isDareBand('60m', june)).toBe(false);
        expect(bandProfile('6m')).toBe(TUNING.PROFILES.fast);
    });

    it('keeps at least the global gap between lines about different bands', () => {
        const { speech } = run([warm, ...rep(200, { bands: ['20m', '15m', '10m'] })]);
        expect(new Set(speech.map((x) => x.band)).size).toBe(3);
        for (let i = 1; i < speech.length; i++) {
            const gap = speech[i].t - speech[i - 1].t;
            if (speech[i].band !== speech[i - 1].band) expect(gap).toBeGreaterThanOrEqual(TUNING.GLOBAL_MIN_GAP_MS);
            else expect(speech[i].rung).toBeGreaterThan(0); // only a band's own escalation may come sooner
        }
    });

    it('lets the fast ladder climb on its own timings (10m: 0 / 2 / 5 min observed)', () => {
        const { speech } = run([warm, ...rep(20, { bands: ['10m'] })]);
        expect(speech.map((x) => x.rung)).toEqual([0, 1, 2]);
        const seen = speech[0].t - P; // first sighting, one poll before confirmation
        expect(speech[1].t - seen).toBe(2 * MIN);
        expect(speech[2].t - seen).toBe(5 * MIN);
    });

    it('still holds another band behind the global gap after a fast escalation', () => {
        const { speech } = run([warm, ...rep(3, { bands: ['10m'] }), ...rep(20, { bands: ['10m', '20m'] })]);
        const first20 = speech.find((x) => x.band === '20m');
        const prev = speech[speech.indexOf(first20) - 1];
        expect(first20.t - prev.t).toBeGreaterThanOrEqual(TUNING.GLOBAL_MIN_GAP_MS);
    });

    it('holds a band cooldown after an episode that spoke', () => {
        const a = run([warm, ...rep(2, { bands: ['10m'] })]);
        expect(a.speech).toHaveLength(1);
        // Fade, then stay away past the merge window (but inside the 30 min cooldown).
        const b = run(rep(35, { bands: [] }), { state: a.state, start: a.t });
        expect(b.state.recent['10m']).toBeUndefined();
        const c = run(rep(4, { bands: ['10m'] }), { state: b.state, start: b.t });
        expect(c.speech).toEqual([]);
        expect(c.traces.some((t) => t.decisions.some((d) => /10m: cooldown/.test(d)))).toBe(true);
        // Past the cooldown the (still present) opening gets its dare.
        const d = run(rep(40, { bands: ['10m'] }), { state: c.state, start: c.t });
        expect(d.speech[0]).toMatchObject({ type: 'dare', band: '10m', rung: 0 });
    });

    it('caps new dares per UTC day at 6 (a surprise may go to 10)', () => {
        const bands = ['80m', '40m', '30m', '20m', '17m', '15m', '12m', '10m'];
        const rising = run([warm, ...rep(400, { bands, kind: 'rising' })]);
        expect(rising.state.dareCount).toBe(TUNING.DAILY_DARE_CAP);
        expect(rising.speech.filter((x) => x.rung === 0)).toHaveLength(6);
        expect(rising.traces.some((t) => t.decisions.some((d) => /daily cap/.test(d)))).toBe(true);

        const surprise = run([warm, ...rep(400, { bands, kind: 'surprise' })]);
        expect(surprise.state.dareCount).toBe(8);
    });

    it('stops a surprise at the surprise cap of 10', () => {
        // Four dares earlier today (on 6m, out of season now, so no cooldown in the way).
        const calls = [1, 2, 3, 4].map((h) => ({ id: `c${h}`, band: '6m', rung: 0, rate: 1, ts: T0 - h * 60 * MIN, verdict: 'banger' }));
        const bands = ['80m', '40m', '30m', '20m', '17m', '15m', '12m', '10m'];
        const surprise = run([warm, ...rep(400, { bands, kind: 'surprise' })], { state: createState({ calls }) });
        expect(surprise.state.dareCount).toBe(TUNING.DAILY_SURPRISE_CAP);
        expect(surprise.speech.filter((x) => x.rung === 0)).toHaveLength(TUNING.DAILY_SURPRISE_CAP - 4);
        expect(surprise.traces.some((t) => t.decisions.some((d) => /daily cap \(10\/10\)/.test(d)))).toBe(true);
    });

    it('keeps the daily cap, band cooldowns and the global gap across a reload', () => {
        const today = [1, 2, 3, 4, 5, 6].map((h) => ({ id: `c${h}`, band: '6m', rung: 0, rate: 1, ts: T0 - h * 60 * MIN, verdict: 'void' }));
        const yesterday = { id: 'y', band: '15m', rung: 0, rate: 1, ts: T0 - 13 * 60 * MIN, verdict: 'dud' };
        const capped = run([warm, ...rep(10, { bands: ['20m'], kind: 'rising' })], { state: createState({ calls: [yesterday, ...today] }) });
        expect(capped.state.dareCount).toBe(6);
        expect(capped.speech).toEqual([]);
        expect(capped.traces.some((t) => t.decisions.some((d) => /20m: daily cap \(6\/6\)/.test(d)))).toBe(true);

        // A 20m dare 20 min before the reload: its band cooldown still holds.
        const recent = { id: 'r', band: '20m', rung: 0, rate: 1, ts: T0 - 20 * MIN, verdict: 'banger' };
        const s = createState({ calls: [recent] });
        expect(s.cooldownUntil['20m']).toBe(recent.ts + bandProfile('20m').cooldownMs);
        expect(s.lastSpokeAt).toBe(recent.ts);
        const cooled = run([warm, ...rep(10, { bands: ['20m'] })], { state: s });
        expect(cooled.speech).toEqual([]);
        expect(cooled.traces.some((t) => t.decisions.some((d) => /20m: cooldown/.test(d)))).toBe(true);

        // A dare on another band 2 min before the reload: the global gap still holds.
        const justNow = { id: 'j', band: '15m', rung: 0, rate: 1, ts: T0 - 2 * MIN, verdict: 'pending' };
        const gapped = run([warm, ...rep(10, { bands: ['20m'] })], { state: createState({ calls: [justNow] }) });
        expect(gapped.speech[0].t - justNow.ts).toBeGreaterThanOrEqual(TUNING.GLOBAL_MIN_GAP_MS);
    });

    it('resets the daily count on a new UTC day', () => {
        const late = Date.UTC(2026, 9, 1, 23, 50);
        const a = run([warm, ...rep(3, { bands: ['20m'] })], { start: late });
        expect(a.state.dareCount).toBe(1);
        const b = run(rep(30, { bands: ['20m'] }), { state: a.state, start: a.t });
        expect(b.state.day).toBe('2026-10-02');
        expect(b.state.dareCount).toBe(0);
    });

    it('marks desktop nags only for rung >= 1 or a surprise', () => {
        const r = run([warm, ...rep(60, { bands: ['20m'], kind: 'rising' })]);
        expect(r.speech.map((x) => x.nag)).toEqual([false, true, true]);
        const s = run([warm, ...rep(3, { bands: ['20m'], kind: 'surprise' })]);
        expect(s.speech[0].nag).toBe(true);
    });

    it('skips "YOUR grid" lines when the live area was widened', () => {
        const plain = run([warm, ...rep(45, { bands: ['20m'] })]);
        expect(plain.speech[2].text).toMatch(/YOUR grid/);
        const widened = run([warm, ...rep(45, { bands: ['20m'] })], { ctx: { widened: true } });
        expect(widened.speech[2].rung).toBe(2);
        expect(widened.speech[2].text).not.toMatch(/your (own )?grid|your spot|backyard/i);
    });

    it('salts a rung-2 dare for a repeat offender', () => {
        const now = T0;
        const grudges = [{ band: '20m', fadedAt: now - 60 * MIN }, { band: '20m', fadedAt: now - 30 * MIN }];
        const { speech } = run([warm, ...rep(45, { bands: ['20m'] })], { state: createState({ grudges }) });
        expect(speech[2].text).toMatch(/ghosted 20m/);
    });
});

describe('reduce: holding[]', () => {
    it('keeps a band that dropped out of the recommendations but still holds green', () => {
        const a = run([warm, ...rep(3, { bands: ['20m'] })]);
        const holding = [{ band: '20m', spots_per_minute: 2, status: 'green', activity_level: 'lively' }];
        const b = run(rep(40, { bands: [], ctx: { holding } }), { state: a.state, start: a.t });
        expect(b.state.episodes['20m']).toBeDefined();
        expect(b.state.grudges).toEqual([]);
        expect(b.speech.every((x) => x.type === 'dare')).toBe(true);
        expect(b.state.episodes['20m'].rung).toBeGreaterThanOrEqual(1);
        // Red status: the grace period starts, then it fades.
        const red = [{ band: '20m', spots_per_minute: 2, status: 'red' }];
        const c = run(rep(11, { bands: [], ctx: { holding: red } }), { state: b.state, start: b.t });
        expect(c.state.episodes['20m']).toBeUndefined();
        expect(c.speech.map((x) => x.type)).toEqual(['fade']);
    });

    it('treats red/grey or a rate far under the peak as faded', () => {
        expect(isHoldingOpen({ status: 'green', spots_per_minute: 2 }, 3)).toBe(true);
        expect(isHoldingOpen({ status: 'yellow', spots_per_minute: 0.5 }, 1)).toBe(true);
        expect(isHoldingOpen({ status: 'grey', spots_per_minute: 5 }, 1)).toBe(false);
        expect(isHoldingOpen({ status: 'red', spots_per_minute: 5 }, 1)).toBe(false);
        expect(isHoldingOpen({ status: 'green', spots_per_minute: 1 }, 3)).toBe(false); // < 0.4 × peak
        expect(isHoldingOpen({ status: 'green', spots_per_minute: 0.2 }, 0.3)).toBe(false); // < 0.3 floor
        expect(isHoldingOpen(null, 1)).toBe(false);
    });
});

describe('almanac gate', () => {
    const slot = almanacSlot(T0); // 12:00 UTC → slot 24
    const lane = (band, n, m) => ({ band, region: 'NA', n: Array(48).fill(n), m: Array(48).fill(m) });
    const almanac = { slot_minutes: 30, m_min: 10, usually_share: 0.5, lanes: [lane('20m', 20, 30), lane('20m', 5, 30), lane('15m', 3, 30), lane('10m', 9, 9)] };

    it('computes the slot from UTC and the usual share from known cells', () => {
        expect(slot).toBe(24);
        expect(almanacSlot(Date.UTC(2026, 0, 1, 0, 29))).toBe(0);
        expect(usualShareNow(almanac, '20m', slot)).toBeCloseTo(20 / 30);
        expect(usualShareNow(almanac, '10m', slot)).toBeNull(); // m < m_min: unknown
        expect(usualShareNow(almanac, '40m', slot)).toBeNull();
        expect(isUsualNow(almanac, '20m', slot)).toBe(true);
        expect(isUsualNow(almanac, '15m', slot)).toBe(false);
        expect(isUsualNow(almanac, '10m', slot)).toBe(false);
        expect(isUsualNow(null, '20m', slot)).toBe(false);
        expect(isUsualNow({ ...almanac, usually_share: 0.7 }, '20m', slot)).toBe(false);
    });

    it('silences a rising / dx_surge on a band usually open now: no dare, no episode', () => {
        for (const kind of ['rising', 'dx_surge']) {
            const { state, speech } = run([warm, ...rep(10, { bands: ['20m'], kind })], { ctx: { almanac } });
            expect(speech).toEqual([]);
            expect(state.episodes).toEqual({});
        }
    });

    it('re-applies the gate once the almanac arrives after the episode started', () => {
        const first = run([{ bands: ['20m'], kind: 'rising' }], { ctx: { almanac: null } });
        expect(first.state.episodes['20m']).toBeDefined();
        const later = run(rep(10, { bands: ['20m'], kind: 'rising', ctx: { almanac } }), { state: first.state, start: first.t });
        expect(later.speech).toEqual([]);
        expect(later.state.episodes).toEqual({});
        expect(later.traces[0].decisions.some((d) => /20m: rising but usually open now .*dropped/.test(d))).toBe(true);
    });

    it('never drops an episode that has already spoken', () => {
        const a = run([warm, ...rep(3, { bands: ['20m'], kind: 'rising' })], { ctx: { almanac: null } });
        expect(a.speech).toHaveLength(1);
        const b = run(rep(3, { bands: ['20m'], kind: 'rising', ctx: { almanac } }), { state: a.state, start: a.t });
        expect(b.state.episodes['20m']).toBeDefined();
    });

    it('lets a surprise through, and fails open without an almanac', () => {
        expect(run([warm, ...rep(3, { bands: ['20m'], kind: 'surprise' })], { ctx: { almanac } }).speech).toHaveLength(1);
        expect(run([warm, ...rep(3, { bands: ['20m'], kind: 'rising' })], { ctx: { almanac: null } }).speech).toHaveLength(1);
        expect(run([warm, ...rep(3, { bands: ['15m'], kind: 'rising' })], { ctx: { almanac } }).speech).toHaveLength(1);
    });
});

describe('resync / moodOf', () => {
    it('resync voids pending calls and makes the next poll a first poll', () => {
        const a = run([warm, ...rep(3, { bands: ['20m'] })]);
        const s = resync(a.state);
        expect(s.episodes).toEqual({});
        expect(s.lastPollAt).toBe(0);
        expect(s.calls[0].verdict).toBe('void');
        expect(moodOf(s)).toBe('dormant');
        expect(run([{ bands: ['20m'] }], { state: s, start: a.t }).traces[0].silent).toBe('first poll');
    });
});

// ── DOM wiring ───────────────────────────────────────────────────────────

function mountDom() {
    document.body.innerHTML = `
        <div id="controls"><div class="sidebar-hk"><button type="button" class="hk-avatar" tabindex="-1" aria-hidden="true"><img src="hk.jpg" alt=""></button></div></div>
        <div id="map-stack"><button id="show-sidebar" style="display: none;"></button></div>
        <div id="hk-layer" class="hk-layer" hidden>
            <div class="hk-bubble" hidden><span class="hk-bubble-text"></span><button type="button" class="hk-bubble-action" hidden></button></div>
            <div class="hk-panel" hidden>
                <select class="hk-meanness"><option value="soft">a</option><option value="buzzed">b</option><option value="drill">c</option></select>
                <select class="hk-lang"><option value="en">EN</option><option value="de">DE</option></select>
                <button class="hk-panel-close"></button>
                <div class="hk-panel-body"></div>
                <input type="checkbox" class="hk-desktop"><span class="hk-panel-foot-text"></span>
            </div>
        </div>`;
}

// Node's own (flag-less) localStorage shadows jsdom's; install a Map-backed one.
function installLocalStorageMock() {
    const store = new Map();
    const mock = {
        getItem: (key) => (store.has(key) ? store.get(key) : null),
        setItem: (key, value) => { store.set(String(key), String(value)); },
        removeItem: (key) => { store.delete(key); },
        clear: () => { store.clear(); },
    };
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: mock });
    Object.defineProperty(window, 'localStorage', { configurable: true, value: mock });
    return mock;
}

describe('initHorstKevin', () => {
    let fetchMock;
    let storage;
    beforeEach(() => {
        resetHotBandsClient(); // shared 10 s cache must not leak between tests
        storage = installLocalStorageMock();
        mountDom();
        fetchMock = vi.fn(async (url) => {
            if (String(url).startsWith('/api/almanac')) return { ok: false, status: 503, json: async () => ({}) };
            return { ok: true, status: 200, json: async () => ({ recommendations: [], holding: [], area: {} }) };
        });
        vi.stubGlobal('fetch', fetchMock);
    });
    afterEach(() => {
        vi.unstubAllGlobals();
        vi.useRealTimers();
        window.history.replaceState({}, '', '/');
    });

    it('returns null without the sidebar avatar or layer', () => {
        document.body.innerHTML = '';
        expect(initHorstKevin({})).toBeNull();
    });

    it('makes the picture live and asks for his own dare set without current_band', async () => {
        const kevin = initHorstKevin({
            getQth: () => 'JO32',
            getSurroundings: () => false,
            getCurrentBand: () => '20m',
            getRigBand: () => '',
            getEnabledBands: () => new Set(['20m']),
            onBandSwitch: () => true,
        });
        await Promise.resolve();
        await Promise.resolve();
        const avatar = document.querySelector('.hk-avatar');
        expect(avatar.classList.contains('is-live')).toBe(true);
        expect(avatar.tabIndex).toBe(0);
        expect(avatar.hasAttribute('aria-hidden')).toBe(false);
        expect(document.getElementById('hk-layer').hidden).toBe(false);

        const hot = fetchMock.mock.calls.map((c) => String(c[0])).find((u) => u.startsWith('/api/hot_bands'));
        const q = new URL(hot, 'http://x').searchParams;
        expect(q.get('qth')).toBe('JO32');
        expect(q.get('rings')).toBe('auto');
        expect(q.get('max')).toBe(String(TUNING.MAX_RECS));
        expect(q.get('bands').split(',')).toEqual([...dareBandsAt(Date.now())]);
        expect(q.has('current_band')).toBe(false);
        expect(q.has('include')).toBe(false);
        const alm = fetchMock.mock.calls.map((c) => String(c[0])).find((u) => u.startsWith('/api/almanac'));
        expect(alm).toBe('/api/almanac?qth=JO32');

        avatar.click();
        expect(document.querySelector('.hk-panel').hidden).toBe(false);
        kevin.stop();
    });

    it('tracks a band via include=, and counts the rig band as answered', async () => {
        vi.useFakeTimers();
        vi.setSystemTime(T0);
        let rig = '';
        fetchMock.mockImplementation(async (url) => {
            if (String(url).startsWith('/api/almanac')) return { ok: false, status: 503, json: async () => ({}) };
            return { ok: true, status: 200, json: async () => ({ recommendations: [rec('20m')], holding: [], area: {} }) };
        });
        const kevin = initHorstKevin({
            getQth: () => 'JO32',
            getSurroundings: () => false,
            getCurrentBand: () => '',
            getRigBand: () => rig,
            getEnabledBands: () => new Set(['20m', '15m']),
            onBandSwitch: () => true,
        });
        const hotQueries = () => fetchMock.mock.calls
            .map((c) => String(c[0]))
            .filter((u) => u.startsWith('/api/hot_bands'))
            .map((u) => new URL(u, 'http://x').searchParams);
        await vi.advanceTimersByTimeAsync(0);
        expect(hotQueries()[0].has('include')).toBe(false);
        await vi.advanceTimersByTimeAsync(P);
        expect(hotQueries()[1].get('include')).toBe('20m');

        // Third poll confirms 20m and speaks the rung-0 dare; then the rig tunes to 20m.
        await vi.advanceTimersByTimeAsync(P);
        expect(document.querySelector('.hk-bubble').hidden).toBe(false);
        rig = '20m';
        await vi.advanceTimersByTimeAsync(P);
        const calls = JSON.parse(storage.getItem('hk:calls'));
        expect(calls).toHaveLength(1);
        expect(calls[0]).toMatchObject({ band: '20m', verdict: 'answered' });

        // 20m drops out of the response while you work it: no grudge, no fade.
        fetchMock.mockImplementation(async (url) => {
            if (String(url).startsWith('/api/almanac')) return { ok: false, status: 503, json: async () => ({}) };
            return { ok: true, status: 200, json: async () => ({ recommendations: [], holding: [], area: {} }) };
        });
        await vi.advanceTimersByTimeAsync(20 * P);
        expect(storage.getItem('hk:grudges')).toBeNull();
        expect(document.querySelector('.hk-avatar').classList.contains('is-fired')).toBe(false);
        kevin.stop();
    });

    it('filters the recommendations through the enabled bands', async () => {
        vi.useFakeTimers();
        vi.setSystemTime(T0);
        fetchMock.mockImplementation(async (url) => {
            if (String(url).startsWith('/api/almanac')) return { ok: false, status: 503, json: async () => ({}) };
            return { ok: true, status: 200, json: async () => ({ recommendations: [rec('20m')], holding: [], area: {} }) };
        });
        const kevin = initHorstKevin({
            getQth: () => 'JO32',
            getCurrentBand: () => '',
            getRigBand: () => '',
            getEnabledBands: () => new Set(['15m']),
            onBandSwitch: () => true,
        });
        await vi.advanceTimersByTimeAsync(5 * P);
        const hot = fetchMock.mock.calls.map((c) => String(c[0])).filter((u) => u.startsWith('/api/hot_bands'));
        expect(hot.length).toBeGreaterThanOrEqual(5);
        expect(hot.every((u) => !new URL(u, 'http://x').searchParams.has('include'))).toBe(true);
        expect(document.querySelector('.hk-bubble').hidden).toBe(true);
        expect(storage.getItem('hk:calls')).toBeNull();
        kevin.stop();
    });

    it('runs the ?hk-demo scenario without touching localStorage', async () => {
        vi.useFakeTimers();
        window.history.replaceState({}, '', '/?hk-demo&hk-trace');
        const log = vi.spyOn(console, 'log').mockImplementation(() => {});
        const setItem = vi.spyOn(storage, 'setItem');
        const rnd = vi.spyOn(Math, 'random').mockReturnValue(0);
        initHorstKevin({ getQth: () => 'JO32', getCurrentBand: () => '', onBandSwitch: () => true });
        await vi.advanceTimersByTimeAsync(10 * MIN);
        expect(setItem).not.toHaveBeenCalled();
        expect(fetchMock).not.toHaveBeenCalled();
        const spoken = log.mock.calls.filter((c) => c[0] === '[hk-trace]').flatMap((c) => c[1].speech);
        expect(spoken.some((x) => /^dare 20m/.test(x))).toBe(true);
        expect(spoken.some((x) => /^fade 20m: openings came and went/.test(x))).toBe(true);
        expect(spoken.some((x) => /^dare 10m/.test(x))).toBe(true);
        expect(spoken.some((x) => /^dare 15m/.test(x))).toBe(true);
        expect(spoken.some((x) => /10m/.test(x) && /^fade/.test(x))).toBe(false);
        expect(document.querySelector('.hk-bubble-text').textContent).toMatch(/demo done|Demo fertig/);
        setItem.mockRestore();
        rnd.mockRestore();
        log.mockRestore();
    });
});
