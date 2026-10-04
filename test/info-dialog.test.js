import { beforeEach, describe, expect, it } from 'vitest';
import { readFileSync, existsSync } from 'node:fs';

import { initInfoDialog } from '../static/info-dialog.js';

function memoryStorage(initial = {}) {
    const data = { ...initial };
    return {
        getItem: (k) => (k in data ? data[k] : null),
        setItem: (k, v) => { data[k] = String(v); },
    };
}

function setup({ seen = true } = {}) {
    document.body.innerHTML = '<header id="app-header"><button id="theme-toggle">T</button></header><main id="app"><button id="start">Go</button></main>';
    const trigger = document.createElement('button');
    trigger.id = 'info-toggle';
    document.getElementById('app-header').appendChild(trigger);
    const dialog = initInfoDialog({ trigger, storage: memoryStorage(seen ? { infoShown: 'true' } : {}) });
    return { trigger, dialog };
}

const escape = (target = document) => target.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));

describe('Help dialog', () => {
    beforeEach(() => { document.body.innerHTML = ''; });

    it('is a labelled modal dialog with a named close button and iframe', () => {
        const { dialog, trigger } = setup();
        const box = dialog.overlay.querySelector('.info-content');
        expect(box.getAttribute('role')).toBe('dialog');
        expect(box.getAttribute('aria-modal')).toBe('true');
        expect(box.getAttribute('aria-label')).toBeTruthy();
        expect(dialog.overlay.querySelector('iframe').getAttribute('title')).toBeTruthy();
        expect(dialog.overlay.querySelector('#close-info').getAttribute('aria-label')).toBe('Close help');
        expect(trigger.getAttribute('aria-haspopup')).toBe('dialog');
        expect(trigger.getAttribute('aria-expanded')).toBe('false');
    });

    it('open: shows the dialog, moves focus to Close, makes the rest of the page inert', () => {
        const { dialog, trigger } = setup();
        trigger.focus();
        trigger.click();
        expect(dialog.isOpen()).toBe(true);
        expect(document.activeElement.id).toBe('close-info');
        expect(trigger.getAttribute('aria-expanded')).toBe('true');
        expect(document.getElementById('app-header').hasAttribute('inert')).toBe(true);
        expect(document.getElementById('app').hasAttribute('inert')).toBe(true);
        expect(dialog.overlay.hasAttribute('inert')).toBe(false);
    });

    it('Escape closes it, lifts inert and returns focus to the button that opened it', () => {
        const { dialog } = setup();
        const start = document.getElementById('start');
        start.focus();
        dialog.open();
        escape();
        expect(dialog.isOpen()).toBe(false);
        expect(document.getElementById('app').hasAttribute('inert')).toBe(false);
        expect(document.getElementById('app-header').hasAttribute('inert')).toBe(false);
        expect(document.activeElement).toBe(start);
    });

    it('Escape does nothing while the dialog is closed', () => {
        setup();
        const ev = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
        document.dispatchEvent(ev);
        expect(ev.defaultPrevented).toBe(false);
    });

    it('Close button and backdrop click close it', () => {
        const { dialog } = setup();
        dialog.open();
        dialog.overlay.querySelector('#close-info').click();
        expect(dialog.isOpen()).toBe(false);
        dialog.open();
        dialog.overlay.querySelector('.info-content').click(); // inside the box: stays open
        expect(dialog.isOpen()).toBe(true);
        dialog.overlay.click(); // the backdrop
        expect(dialog.isOpen()).toBe(false);
    });

    it('closes when the iframe reports Escape, but ignores other senders', () => {
        const { dialog } = setup();
        dialog.open();
        const frame = dialog.overlay.querySelector('iframe');
        const send = (init) => window.dispatchEvent(new MessageEvent('message', { data: { type: 'horst-info-close' }, origin: window.location.origin, source: frame.contentWindow, ...init }));
        send({ origin: 'https://evil.example' });
        send({ source: window });
        expect(dialog.isOpen()).toBe(true);
        send({});
        expect(dialog.isOpen()).toBe(false);
    });

    it('first visit opens it once and remembers; later visits stay closed', () => {
        const first = setup({ seen: false });
        expect(first.dialog.isOpen()).toBe(true);
        first.dialog.close();
        expect(document.activeElement).toBe(first.trigger); // nothing to return to: the Help button
        const again = initInfoDialog({ trigger: first.trigger, storage: memoryStorage({ infoShown: 'true' }) });
        expect(again.isOpen()).toBe(false);
    });

    it('re-initialising replaces the document listeners instead of stacking them', () => {
        const a = setup();
        const b = setup();
        a.dialog.open();
        b.dialog.open();
        escape();
        expect(b.dialog.isOpen()).toBe(false); // the live one closes
        expect(a.dialog.isOpen()).toBe(true); // the stale one is no longer listening
    });
});

describe('Help text', () => {
    it('says double-click, has no known typos, and ships no German doc', () => {
        const en = readFileSync('static/info.md', 'utf8');
        expect(en).toMatch(/Double-click the map/);
        expect(en).not.toMatch(/Click anywhere on the map|somrthing|intrested/);
        expect(existsSync('static/info.de.md')).toBe(false);
        expect(readFileSync('static/info.html', 'utf8')).not.toMatch(/info\.de\.md/);
    });
});
