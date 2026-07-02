// @vitest-environment jsdom
import { vi, describe, it, expect, beforeEach } from 'vitest';

// opmode.js pulls in the heavy azimuth runtime and locator utils only for side
// effects we don't exercise here; stub them so the module imports cleanly.
vi.mock('./azimuth-runtime.js', () => ({ setAzimuthAntennaOverlay: vi.fn() }));
vi.mock('./map.js', () => ({ setMercatorAntennaOverlay: vi.fn() }));
vi.mock('./utils.js', () => ({
    locatorToBounds: vi.fn(() => null),
    freqHzToBand: (hz) => {
        const ranges = [
            ['160m', 1800000, 2000000], ['80m', 3500000, 4000000], ['40m', 7000000, 7300000],
            ['30m', 10100000, 10150000], ['20m', 14000000, 14350000], ['17m', 18068000, 18168000],
            ['15m', 21000000, 21450000], ['12m', 24890000, 24990000], ['10m', 28000000, 29700000],
            ['6m', 50000000, 54000000]
        ];
        const f = Number(hz);
        if (!Number.isFinite(f) || f <= 0) return '';
        for (const [label, lo, hi] of ranges) if (f >= lo && f <= hi) return label;
        return '';
    }
}));

import {
    normalizeMode,
    beamModeLabel,
    overlayModeFor,
    reverseAlarmIntensity,
    REVERSE_ALARM_RAMP_MS,
    syncBeamButtonsFromAntenna,
    updateReverseAlarm,
    syncControlWidgets,
    syncAntennaOverlay,
    setAntennaMode,
    updateOpModeStatusLine,
    __setOpModeStateForTest
} from './opmode.js';
import { setAzimuthAntennaOverlay } from './azimuth-runtime.js';

describe('normalizeMode (UltraBeam vocabulary)', () => {
    it('maps reverse aliases to canonical reverse', () => {
        for (const v of ['reverse', '180', '180°', 'backward', 'back', 'REVERSE']) {
            expect(normalizeMode(v)).toBe('reverse');
        }
    });
    it('maps bidirectional aliases', () => {
        for (const v of ['bidirectional', 'bi-dir', 'bidir', 'bi']) {
            expect(normalizeMode(v)).toBe('bidirectional');
        }
    });
    it('defaults unknown/forward to forward', () => {
        for (const v of ['forward', 'normal', '', 'garbage', null, undefined]) {
            expect(normalizeMode(v)).toBe('forward');
        }
    });
});

describe('beamModeLabel', () => {
    it('renders operator-facing labels', () => {
        expect(beamModeLabel('reverse')).toBe('180°');
        expect(beamModeLabel('bidirectional')).toBe('bi-dir');
        expect(beamModeLabel('forward')).toBe('forward');
    });
});

describe('overlayModeFor (runtime vocabulary bridge)', () => {
    it('translates reverse to the runtime backward lobe', () => {
        expect(overlayModeFor('reverse')).toBe('backward');
        expect(overlayModeFor('forward')).toBe('forward');
        expect(overlayModeFor('bidirectional')).toBe('bidirectional');
    });
});

describe('reverseAlarmIntensity (90s ramp)', () => {
    it('ramps 0 -> 1 over the window and clamps', () => {
        expect(reverseAlarmIntensity(0)).toBe(0);
        expect(reverseAlarmIntensity(REVERSE_ALARM_RAMP_MS / 2)).toBeCloseTo(0.5, 5);
        expect(reverseAlarmIntensity(REVERSE_ALARM_RAMP_MS)).toBe(1);
        expect(reverseAlarmIntensity(REVERSE_ALARM_RAMP_MS * 2)).toBe(1);
    });
    it('never goes negative', () => {
        expect(reverseAlarmIntensity(-5000)).toBe(0);
        expect(reverseAlarmIntensity(NaN)).toBe(0);
    });
});

describe('beam button + alarm DOM behavior', () => {
    beforeEach(() => {
        document.body.innerHTML = `
            <button class="opmode-beam-btn" id="opmode-beam-forward" data-mode="forward"></button>
            <button class="opmode-beam-btn" id="opmode-beam-180" data-mode="reverse"></button>
            <button class="opmode-beam-btn" id="opmode-beam-bidir" data-mode="bidirectional"></button>
            <div id="opmode-reverse-badge" style="display: none;" role="status" aria-live="assertive"></div>
        `;
        // Reset the module-level reverseSince between tests.
        updateReverseAlarm({ mode: 'forward', beamOnline: true });
    });

    it('marks the live direction button active', () => {
        syncBeamButtonsFromAntenna({ mode: 'reverse' });
        expect(document.getElementById('opmode-beam-180').classList.contains('active')).toBe(true);
        expect(document.getElementById('opmode-beam-180').getAttribute('aria-pressed')).toBe('true');
        expect(document.getElementById('opmode-beam-forward').classList.contains('active')).toBe(false);
        expect(document.getElementById('opmode-beam-bidir').classList.contains('active')).toBe(false);
    });

    it('raises the reverse alarm + text cue when reverse is live', () => {
        updateReverseAlarm({ mode: 'reverse', beamOnline: true });
        const btn = document.getElementById('opmode-beam-180');
        const badge = document.getElementById('opmode-reverse-badge');
        expect(btn.classList.contains('opmode-reverse-alarm')).toBe(true);
        // Non-color channel (text cue) is shown regardless of color/motion.
        expect(badge.style.display).not.toBe('none');
        expect(btn.style.getPropertyValue('--reverse-alarm-intensity')).not.toBe('');
    });

    it('clears the alarm and text cue when leaving reverse', () => {
        updateReverseAlarm({ mode: 'reverse', beamOnline: true });
        updateReverseAlarm({ mode: 'forward', beamOnline: true });
        const btn = document.getElementById('opmode-beam-180');
        const badge = document.getElementById('opmode-reverse-badge');
        expect(btn.classList.contains('opmode-reverse-alarm')).toBe(false);
        expect(badge.style.display).toBe('none');
    });

    it('does not alarm when the UltraBeam is offline', () => {
        updateReverseAlarm({ mode: 'reverse', beamOnline: false });
        const btn = document.getElementById('opmode-beam-180');
        expect(btn.classList.contains('opmode-reverse-alarm')).toBe(false);
    });

    it('restarts the ramp after leaving and re-entering reverse', () => {
        const realNow = Date.now;
        let now = 1_000_000;
        Date.now = () => now;
        try {
            updateReverseAlarm({ mode: 'reverse', beamOnline: true });
            now += REVERSE_ALARM_RAMP_MS; // ramp to max
            updateReverseAlarm({ mode: 'reverse', beamOnline: true });
            const high = document.getElementById('opmode-beam-180').style.getPropertyValue('--reverse-alarm-intensity');
            expect(parseFloat(high)).toBe(1);

            updateReverseAlarm({ mode: 'forward', beamOnline: true }); // leaves reverse
            updateReverseAlarm({ mode: 'reverse', beamOnline: true });  // re-enters, fresh ramp
            const low = document.getElementById('opmode-beam-180').style.getPropertyValue('--reverse-alarm-intensity');
            expect(parseFloat(low)).toBe(0);
        } finally {
            Date.now = realNow;
        }
    });

    it('announces via an assertive live region with text content on entry', () => {
        const badge = document.getElementById('opmode-reverse-badge');
        expect(badge.getAttribute('aria-live')).toBe('assertive');
        updateReverseAlarm({ mode: 'reverse', beamOnline: true });
        expect(badge.textContent).toMatch(/REVERSE/);
        updateReverseAlarm({ mode: 'forward', beamOnline: true });
        expect(badge.textContent).toBe(''); // cleared so it re-announces next time
    });
});

const PERMITTED_ONLINE = {
    enabled: true,
    controlPermittedByServer: true,
    controlPermittedByAgent: true,
    controlPermittedByUser: true,
    commandInFlight: false,
    transport: 'direct',
    directBaseUrl: 'http://localhost:9955',
    ultrabeamCapabilities: { control: true, online: true }
};

describe('beam control gating (syncControlWidgets)', () => {
    beforeEach(() => {
        document.body.innerHTML = `
            <button class="opmode-beam-btn" id="opmode-beam-forward" data-mode="forward"></button>
            <button class="opmode-beam-btn" id="opmode-beam-180" data-mode="reverse"></button>
            <button class="opmode-beam-btn" id="opmode-beam-bidir" data-mode="bidirectional"></button>
            <div id="opmode-beam-unavailable" style="display: none;"></div>
        `;
    });

    const buttonsDisabled = () =>
        [...document.querySelectorAll('.opmode-beam-btn')].every((b) => b.disabled);

    it('enables buttons and shows no message when permitted + online', () => {
        __setOpModeStateForTest(PERMITTED_ONLINE);
        syncControlWidgets();
        expect(buttonsDisabled()).toBe(false);
        expect(document.getElementById('opmode-beam-unavailable').textContent).toBe('');
    });

    it('disables buttons with "UltraBeam offline" when online=false', () => {
        __setOpModeStateForTest({ ...PERMITTED_ONLINE, ultrabeamCapabilities: { control: true, online: false } });
        syncControlWidgets();
        expect(buttonsDisabled()).toBe(true);
        expect(document.getElementById('opmode-beam-unavailable').textContent).toBe('UltraBeam offline');
    });

    it('shows "UltraBeam not configured" when capability absent', () => {
        __setOpModeStateForTest({ ...PERMITTED_ONLINE, ultrabeamCapabilities: null });
        syncControlWidgets();
        expect(buttonsDisabled()).toBe(true);
        expect(document.getElementById('opmode-beam-unavailable').textContent).toBe('UltraBeam not configured');
    });

    it('shows "Antenna control not permitted" even when also not configured', () => {
        __setOpModeStateForTest({ ...PERMITTED_ONLINE, controlPermittedByUser: false, ultrabeamCapabilities: null });
        syncControlWidgets();
        expect(buttonsDisabled()).toBe(true);
        expect(document.getElementById('opmode-beam-unavailable').textContent).toBe('Antenna control not permitted');
    });
});

describe('setAntennaMode posts to the beam endpoint', () => {
    beforeEach(() => {
        document.body.innerHTML = '';
        __setOpModeStateForTest({ ...PERMITTED_ONLINE, station: { lat: 50, lng: 8 } });
    });

    it('POSTs canonical mode + permit_control to /v1/antenna/beam', async () => {
        const fetchMock = vi.fn(async () => ({
            ok: true,
            json: async () => ({ antenna: { mode: 'bidirectional', azimuth_deg: 90, beam_online: true, azimuth_online: true } })
        }));
        globalThis.fetch = fetchMock;

        await setAntennaMode('bidirectional');

        const [url, opts] = fetchMock.mock.calls[0];
        expect(String(url).endsWith('/v1/antenna/beam')).toBe(true);
        expect(opts.method).toBe('POST');
        const body = JSON.parse(opts.body);
        expect(body).toMatchObject({ mode: 'bidirectional', permit_control: true });
    });

    it('rejects when the UltraBeam is offline', async () => {
        __setOpModeStateForTest({ ultrabeamCapabilities: { control: true, online: false } });
        await expect(setAntennaMode('reverse')).rejects.toThrow(/offline/);
    });
});

describe('overlay suppression (syncAntennaOverlay)', () => {
    beforeEach(() => {
        document.body.innerHTML = '';
        setAzimuthAntennaOverlay.mockClear();
        __setOpModeStateForTest({
            enabled: true,
            station: { lat: 50, lng: 8, locator: 'JO40', name: 'TEST' },
            requestRender: () => {}
        });
    });

    it('suppresses the overlay when the beam is offline', () => {
        __setOpModeStateForTest({ antenna: { azimuthDeg: 90, mode: 'reverse', beamOnline: false, beamwidth3dBDeg: 60 } });
        syncAntennaOverlay();
        const arg = setAzimuthAntennaOverlay.mock.calls.at(-1)[0];
        expect(arg.enabled).toBe(false);
    });

    it('suppresses the overlay when azimuth is missing', () => {
        __setOpModeStateForTest({ antenna: { azimuthDeg: null, mode: 'reverse', beamOnline: true, beamwidth3dBDeg: 60 } });
        syncAntennaOverlay();
        const arg = setAzimuthAntennaOverlay.mock.calls.at(-1)[0];
        expect(arg.enabled).toBe(false);
    });

    it('draws the reverse lobe via the backward runtime vocabulary', () => {
        __setOpModeStateForTest({ antenna: { azimuthDeg: 90, mode: 'reverse', beamOnline: true, beamwidth3dBDeg: 80 } });
        syncAntennaOverlay();
        const arg = setAzimuthAntennaOverlay.mock.calls.at(-1)[0];
        expect(arg.enabled).toBe(true);
        expect(arg.mode).toBe('backward');
    });
});

describe('opmode status line (Band | Mode | QRG | Antenna)', () => {
    beforeEach(() => {
        document.body.innerHTML = `
            <div id="opmode-status-line" style="display: none;">
                <span id="opmode-sl-band">—</span>
                <span id="opmode-sl-mode">—</span>
                <span id="opmode-sl-qrg">—</span>
                <span id="opmode-sl-antenna">—</span>
            </div>`;
        __setOpModeStateForTest({
            enabled: true,
            antenna: null,
            liveRig: null,
            lastTuned: null
        });
    });

    const txt = (id) => document.getElementById(id).textContent;

    it('is hidden when opmode is disabled', () => {
        __setOpModeStateForTest({ enabled: false });
        updateOpModeStatusLine();
        expect(document.getElementById('opmode-status-line').style.display).toBe('none');
    });

    it('is hidden when there are no values (no live rig, no last tune)', () => {
        __setOpModeStateForTest({
            enabled: true,
            liveRig: null,
            lastTuned: null,
            antenna: { azimuthDeg: 245, mode: 'forward', azimuthOnline: true }
        });
        updateOpModeStatusLine();
        expect(document.getElementById('opmode-status-line').style.display).toBe('none');
    });

    it('shows live rig band/mode/QRG when a rig readback is present', () => {
        __setOpModeStateForTest({
            liveRig: { freqHz: 14074000, mode: 'USB', split: false, online: true, freqRxHz: null, modeRx: '' },
            antenna: { azimuthDeg: 245, mode: 'forward', azimuthOnline: true }
        });
        updateOpModeStatusLine();
        expect(document.getElementById('opmode-status-line').style.display).toBe('flex');
        expect(txt('opmode-sl-band')).toBe('20m');
        expect(txt('opmode-sl-mode')).toBe('USB');
        expect(txt('opmode-sl-qrg')).toBe('14.074 MHz');
        expect(txt('opmode-sl-antenna')).toBe('245° forward');
    });

    it('surfaces split operation with TX/RX frequencies', () => {
        __setOpModeStateForTest({
            liveRig: { freqHz: 14074000, mode: 'CW', split: true, online: true, freqRxHz: 14080000, modeRx: 'CW' },
            antenna: { azimuthDeg: 90, mode: 'forward', azimuthOnline: true }
        });
        updateOpModeStatusLine();
        expect(txt('opmode-sl-qrg')).toBe('TX 14.074 / RX 14.080 MHz');
        expect(txt('opmode-sl-mode')).toContain('split');
    });

    it('falls back to the last commanded tune when no live rig', () => {
        __setOpModeStateForTest({
            liveRig: null,
            lastTuned: { freqHz: 7040000, mode: 'CW' },
            antenna: null
        });
        updateOpModeStatusLine();
        expect(txt('opmode-sl-band')).toBe('40m');
        expect(txt('opmode-sl-mode')).toBe('CW');
        expect(txt('opmode-sl-qrg')).toBe('7.040 MHz');
        expect(txt('opmode-sl-antenna')).toBe('—');
    });

    it('prefers live rig over the last commanded tune', () => {
        __setOpModeStateForTest({
            liveRig: { freqHz: 21074000, mode: 'USB', split: false, online: true, freqRxHz: null, modeRx: '' },
            lastTuned: { freqHz: 7040000, mode: 'CW' },
            antenna: null
        });
        updateOpModeStatusLine();
        expect(txt('opmode-sl-band')).toBe('15m');
        expect(txt('opmode-sl-qrg')).toBe('21.074 MHz');
    });
});
