import { beforeEach, describe, expect, it, vi } from 'vitest';

const { mockMap } = vi.hoisted(() => ({
    mockMap: {
        removeLayer: vi.fn(),
        fitBounds: vi.fn(),
        hasLayer: vi.fn(() => false)
    }
}));

vi.mock('../static/map.js', () => ({
    map: mockMap
}));

// Fresh module state per test: turfFailures/lastTurfFailureAt live at module
// scope, so each case re-imports renderers.js to start from a clean loader.
async function freshRenderers() {
    vi.resetModules();
    return import('../static/renderers.js');
}

describe('ensureTurf lazy loader', () => {
    beforeEach(() => {
        delete globalThis.turf;
        const injected = [];
        const realCreate = document.createElement.bind(document);
        vi.spyOn(document, 'createElement').mockImplementation((tag) => {
            const el = realCreate(tag);
            if (tag === 'script') {
                el.remove = vi.fn();
                injected.push(el);
            }
            return el;
        });
        vi.spyOn(document.head, 'appendChild').mockImplementation(() => {});
        globalThis.__ensureTurfInjected = injected;
    });

    it('reuses an already-present turf global without injecting', async () => {
        globalThis.turf = { marker: 'stub' };
        const { ensureTurf } = await freshRenderers();
        await expect(ensureTurf()).resolves.toBe(globalThis.turf);
        expect(globalThis.__ensureTurfInjected).toHaveLength(0);
    });

    it('rejects when the script loads without a turf global, then backs off', async () => {
        const { ensureTurf } = await freshRenderers();
        const p = ensureTurf();
        expect(globalThis.__ensureTurfInjected).toHaveLength(1);
        globalThis.__ensureTurfInjected[0].onload();
        await expect(p).rejects.toThrow('turf global is missing');
        expect(globalThis.__ensureTurfInjected[0].remove).toHaveBeenCalled();

        // Within the backoff window a retry must not inject another script.
        await expect(ensureTurf()).rejects.toThrow('backing off');
        expect(globalThis.__ensureTurfInjected).toHaveLength(1);
    });

    it('rejects on script error and removes the dead tag', async () => {
        const { ensureTurf } = await freshRenderers();
        const p = ensureTurf();
        expect(globalThis.__ensureTurfInjected).toHaveLength(1);
        globalThis.__ensureTurfInjected[0].onerror();
        await expect(p).rejects.toThrow('failed to load');
        expect(globalThis.__ensureTurfInjected[0].remove).toHaveBeenCalled();
        await expect(ensureTurf()).rejects.toThrow('backing off');
        expect(globalThis.__ensureTurfInjected).toHaveLength(1);
    });

    it('resolves once the script defines the global and serves later calls from cache', async () => {
        const { ensureTurf } = await freshRenderers();
        const p = ensureTurf();
        const stub = { marker: 'real' };
        globalThis.turf = stub;
        globalThis.__ensureTurfInjected[0].onload();
        await expect(p).resolves.toBe(stub);
        // The successful loader promise stays cached: even with the global
        // gone again, a later call resolves without re-injecting a script.
        delete globalThis.turf;
        await expect(ensureTurf()).resolves.toBe(stub);
        expect(globalThis.__ensureTurfInjected).toHaveLength(1);
    });

    it('rejects with a Promise (not null) when no DOM is available', async () => {
        const { ensureTurf } = await freshRenderers();
        const doc = globalThis.document;
        Object.defineProperty(globalThis, 'document', { configurable: true, value: undefined });
        try {
            await expect(ensureTurf()).rejects.toThrow('no DOM available');
        } finally {
            Object.defineProperty(globalThis, 'document', { configurable: true, value: doc });
        }
    });
});