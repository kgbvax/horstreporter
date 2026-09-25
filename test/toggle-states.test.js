import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

// One "on" look for toggles (style.css "Unified control system"): selected =
// --accent-tint-strong fill + --accent-strong text. Solid accent fills are
// kept only for the Go button (.btn-primary) and the operator beam buttons.
// The accent text must hold WCAG AA (>= 4.5:1) on both tints in both themes,
// and text on the solid fills must too.

const css = readFileSync('static/style.css', 'utf8'); // vitest runs from the repo root
const cqSource = readFileSync('static/dxcluster.js', 'utf8');

function block(source, selectorLine) {
    const start = source.indexOf(selectorLine);
    if (start < 0) throw new Error(`selector not found: ${selectorLine}`);
    return source.slice(start, source.indexOf('}', start) + 1);
}

// The first body { ... } block that declares the tokens, and the dark override.
const lightBlock = block(css, '/* Global variables and theme setup */\nbody {');
const darkBlock = block(css, 'body[data-theme="dark"] {');

function tokens(text) {
    const out = {};
    const code = text.replace(/\/\*[\s\S]*?\*\//g, ''); // comments quote tokens too
    for (const m of code.matchAll(/(--[\w-]+):\s*([^;]+);/g)) out[m[1]] = m[2].trim();
    return out;
}

const hex = (h) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));

// Resolve a token to [r,g,b]: hex, var(--x) or color-mix(in srgb, A p%, B).
function resolve(value, scope) {
    const v = value.trim();
    if (v.startsWith('#')) return hex(v);
    const ref = v.match(/^var\((--[\w-]+)\)$/);
    if (ref) return resolve(scope[ref[1]], scope);
    const mix = v.match(/^color-mix\(in srgb,\s*(.+?)\s+(\d+)%,\s*(.+)\)$/);
    if (mix) {
        const a = resolve(mix[1], scope);
        const b = resolve(mix[3], scope);
        const p = Number(mix[2]) / 100;
        return a.map((c, i) => c * p + b[i] * (1 - p));
    }
    throw new Error(`cannot resolve ${v}`);
}

function luminance([r, g, b]) {
    const lin = (c) => {
        const s = c / 255;
        return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
    };
    return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

function ratio(a, b) {
    const la = luminance(a);
    const lb = luminance(b);
    return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

const light = tokens(lightBlock);
const dark = { ...light, ...tokens(darkBlock) };

describe('accent contrast (style.css tokens)', () => {
    for (const [name, scope] of [['light', light], ['dark', dark]]) {
        it(`${name}: accent text holds AA on the page and on both tints`, () => {
            const text = resolve('var(--accent-strong)', scope);
            for (const surface of ['--bg-color', '--accent-tint', '--accent-tint-strong']) {
                const r = ratio(text, resolve(`var(${surface})`, scope));
                expect(r, `${name} --accent-strong on ${surface}: ${r.toFixed(2)}`).toBeGreaterThanOrEqual(4.5);
            }
        });

        it(`${name}: text on the solid accent fills holds AA`, () => {
            const ink = resolve('var(--accent-contrast)', scope);
            for (const fill of ['--accent-fill', '--accent-fill-strong']) {
                const r = ratio(ink, resolve(`var(${fill})`, scope));
                expect(r, `${name} --accent-contrast on ${fill}: ${r.toFixed(2)}`).toBeGreaterThanOrEqual(4.5);
            }
        });
    }
});

describe('one "on" look for toggles', () => {
    const tinted = (rule) => {
        expect(rule).toContain('var(--accent-tint-strong)');
        expect(rule).toContain('var(--accent-strong)');
        expect(rule).not.toMatch(/background[\w-]*:\s*var\(--accent\)/);
    };

    it('map panel toggles use the tint look', () => {
        tinted(block(css, '.panel-toggle.is-active,\n.panel-toggle.is-active:hover {'));
    });

    it('Propagation source and color scale chips use the tint look', () => {
        tinted(block(css, '.wspr-src-chip.is-on,\n.wspr-src-chip.is-on:hover {'));
        expect(css).not.toContain('#17a2b8;\n    color: #fff');
    });

    it('timeline speed buttons have no solid override (Bootstrap .active = tint)', () => {
        expect(css).not.toMatch(/\.timeline-speed \.btn\.active/);
    });

    it('Chase Queue mode pills and sorted column use the tint look', () => {
        tinted(block(cqSource, '.cq-mode.act {'));
        tinted(block(cqSource, '.cq-fh1 button.act, .cq-fh2 button.act {'));
    });

    it('solid fills remain only on Go and the beam buttons, via --accent-fill', () => {
        expect(block(css, '.btn-primary {')).toContain('--bs-btn-bg: var(--accent-fill)');
        expect(block(css, '.opmode-beam-btn {')).toContain('--bs-btn-active-bg: var(--accent-fill)');
        // No selected state paints white-on-accent (3.3:1) any more.
        expect(css).not.toMatch(/background:\s*var\(--accent\);\s*\n\s*border-color:[^;]+;\s*\n\s*color:\s*var\(--accent-contrast\)/);
    });
});
