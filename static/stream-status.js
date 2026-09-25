// stream-status.js — the sidebar status line (#stream-status) for the live
// spot stream. One place for its copy and markup so every state reads the
// same way: an optional plain first line (what the map is showing) and an
// optional second line in a status tone (ok / warn / danger), plus the
// loading spinner. Text is always assigned via textContent: some messages
// embed raw user input or server-sent error text.

import { formatNumber } from './utils.js';

export const STREAM_STATUS_TEXT = Object.freeze({
    idle: 'Not connected',
    loading: 'Loading recent spots',
    updating: 'Updating band selection',
    reconnecting: 'Connection lost, reconnecting',
    unreachable: 'Cannot reach the server, retrying',
    disconnected: 'Disconnected. Press Go to reconnect.',
    ended: 'Live data stopped. The timeline shows this session only.',
    timeTravel: 'Time travel: showing past spots',
    snapshotLoading: 'Loading snapshot',
});

// "1 spot" / "1,274 spots".
export function spotCountText(count) {
    const n = Math.max(0, Number(count) || 0);
    return `${formatNumber(n)} ${n === 1 ? 'spot' : 'spots'}`;
}

// "Live for JO32" — the first line while a stream is open.
export function liveForText(qth) {
    return `Live for ${qth}`;
}

export function connectingText(qth) {
    return `Connecting to live data for ${qth}`;
}

// Server-sent stream errors (e.g. the capacity reject) are kept verbatim but
// prefixed so the line says what failed.
export function serverErrorText(message) {
    return `Could not connect: ${String(message ?? '').trim() || 'unknown error'}`;
}

export function timeTravelErrorText(message) {
    return `Could not load time travel data: ${String(message ?? '').trim() || 'unknown error'}`;
}

// renderStreamStatus writes one status state into el.
//   title    plain first line (optional)
//   message  second line (or the only line), in the given tone (optional)
//   tone     'ok' | 'warn' | 'danger' | '' → .status-ok / .status-warn / .status-danger
//   spinner  append the loading spinner
export function renderStreamStatus(el, { title = '', message = '', tone = '', spinner = false } = {}) {
    if (!el) return;
    const nodes = [];
    if (title) nodes.push(document.createTextNode(title));
    if (message) {
        if (title) nodes.push(document.createElement('br'));
        const span = document.createElement('span');
        if (tone) span.className = `status-${tone}`;
        span.textContent = message;
        nodes.push(span);
    }
    if (spinner) {
        nodes.push(document.createTextNode(' '));
        const spin = document.createElement('div');
        spin.className = 'spinner';
        nodes.push(spin);
    }
    el.replaceChildren(...nodes);
}

// setStreamStatus is renderStreamStatus on the #stream-status element.
export function setStreamStatus(state) {
    renderStreamStatus(document.getElementById('stream-status'), state);
}
