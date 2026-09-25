// stream-status.test.js — the sidebar stream status line (#stream-status):
// copy helpers and the two-line markup (plain first line, toned second line,
// optional spinner). Text always goes through textContent.

import { describe, it, expect, beforeEach } from 'vitest';

import {
    STREAM_STATUS_TEXT,
    renderStreamStatus,
    setStreamStatus,
    spotCountText,
    liveForText,
    connectingText,
    serverErrorText,
    timeTravelErrorText,
} from '../static/stream-status.js';

describe('stream status copy', () => {
    it('uses plain user language for every state', () => {
        expect(STREAM_STATUS_TEXT).toMatchObject({
            idle: 'Not connected',
            loading: 'Loading recent spots',
            reconnecting: 'Connection lost, reconnecting',
            disconnected: 'Disconnected. Press Go to reconnect.',
            ended: 'Live data stopped. The timeline shows this session only.',
        });
        for (const text of Object.values(STREAM_STATUS_TEXT)) {
            expect(text).not.toMatch(/Status:|QTH|Subscribed| — | · /);
            expect(text[0]).toBe(text[0].toUpperCase());
        }
    });

    it('pluralizes the spot count', () => {
        expect(spotCountText(1)).toBe('1 spot');
        expect(spotCountText(0)).toBe('0 spots');
        expect(spotCountText(12)).toBe('12 spots');
        expect(spotCountText(undefined)).toBe('0 spots');
    });

    it('names the locator or callsign the stream is for', () => {
        expect(liveForText('JO32')).toBe('Live for JO32');
        expect(connectingText('DL9ET')).toBe('Connecting to live data for DL9ET');
    });

    it('prefixes raw error text so the line says what failed', () => {
        expect(serverErrorText('Server is at capacity.')).toBe('Could not connect: Server is at capacity.');
        expect(serverErrorText('')).toBe('Could not connect: unknown error');
        expect(timeTravelErrorText('history fetch failed (503)')).toBe('Could not load time travel data: history fetch failed (503)');
        expect(timeTravelErrorText(null)).toBe('Could not load time travel data: unknown error');
    });
});

describe('renderStreamStatus', () => {
    let el;
    beforeEach(() => {
        document.body.innerHTML = '<div id="stream-status">old</div>';
        el = document.getElementById('stream-status');
    });

    it('renders a title line and a toned message line with a spinner', () => {
        renderStreamStatus(el, { title: 'Live for JO32', message: 'Loading recent spots', tone: 'warn', spinner: true });
        expect(el.childNodes[0].textContent).toBe('Live for JO32');
        expect(el.querySelector('br')).not.toBeNull();
        expect(el.querySelector('span.status-warn').textContent).toBe('Loading recent spots');
        expect(el.querySelector('.spinner')).not.toBeNull();
    });

    it('renders a single message without a line break or tone class when none is given', () => {
        renderStreamStatus(el, { message: 'Not connected' });
        expect(el.textContent).toBe('Not connected');
        expect(el.querySelector('br')).toBeNull();
        expect(el.querySelector('span').className).toBe('');
        expect(el.querySelector('.spinner')).toBeNull();
    });

    it('treats message text as text, never as markup', () => {
        renderStreamStatus(el, { message: '<img src=x onerror=alert(1)>', tone: 'danger' });
        expect(el.querySelector('img')).toBeNull();
        expect(el.querySelector('.status-danger').textContent).toBe('<img src=x onerror=alert(1)>');
    });

    it('replaces the previous state and tolerates a missing element', () => {
        renderStreamStatus(el, { title: 'Live for JO32', message: '5 spots', tone: 'ok' });
        renderStreamStatus(el, { message: 'Disconnected. Press Go to reconnect.', tone: 'danger' });
        expect(el.textContent).toBe('Disconnected. Press Go to reconnect.');
        expect(el.querySelector('.status-ok')).toBeNull();
        expect(() => renderStreamStatus(null, { message: 'x' })).not.toThrow();
    });

    it('setStreamStatus targets #stream-status', () => {
        setStreamStatus({ message: STREAM_STATUS_TEXT.idle });
        expect(el.textContent).toBe('Not connected');
    });
});
