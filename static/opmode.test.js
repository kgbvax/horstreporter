// @vitest-environment jsdom
import { vi, describe, it, expect, beforeEach } from 'vitest';

// opmode.js pulls in the heavy azimuth runtime and locator utils only for side
// effects we don't exercise here; stub them so the module imports cleanly.
vi.mock('./azimuth-runtime.js', () => ({ setAzimuthAntennaOverlay: vi.fn() }));
vi.mock('./utils.js', () => ({ locatorToBounds: vi.fn(() => null) }));

import {
    normalizeMode,
    beamModeLabel,
    overlayModeFor,
    reverseAlarmIntensity,
    REVERSE_ALARM_RAMP_MS,
    syncBeamButtonsFromAntenna,
    updateReverseAlarm
} from './opmode.js';

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
            <div id="opmode-reverse-badge" style="display: none;"></div>
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
});
