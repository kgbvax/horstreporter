import { describe, it, expect } from 'vitest';
import { setPanelToggleState } from '../static/panel-toggle.js';

// The map's panel toggle pills (the Conditions toggle) stay visible while their
// panel is open, show the on state (.is-active), expose aria-pressed and title
// the next action.

describe('setPanelToggleState', () => {
    it('sets the on look, aria-pressed and the next-action title', () => {
        const btn = document.createElement('button');
        const labels = { show: 'Show x', hide: 'Hide x' };
        setPanelToggleState(btn, true, labels);
        expect(btn.classList.contains('is-active')).toBe(true);
        expect(btn.getAttribute('aria-pressed')).toBe('true');
        expect(btn.title).toBe('Hide x');
        setPanelToggleState(btn, false, labels);
        expect(btn.classList.contains('is-active')).toBe(false);
        expect(btn.getAttribute('aria-pressed')).toBe('false');
        expect(btn.title).toBe('Show x');
    });

    it('ignores a missing button', () => {
        expect(() => setPanelToggleState(null, true, { show: 'a', hide: 'b' })).not.toThrow();
    });
});
