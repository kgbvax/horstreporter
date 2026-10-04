import { describe, expect, it } from 'vitest';
import fs from 'node:fs';
import path from 'node:path';

const STATIC = path.resolve(__dirname, '../static');

// Static (non-dynamic) relative imports/re-exports of a module.
function staticImports(file) {
    const src = fs.readFileSync(path.join(STATIC, file), 'utf8');
    const out = new Set();
    const re = /(?:^|\n)\s*(?:import|export)\s[^'"\n]*?from\s*['"](\.[^'"]+)['"]|(?:^|\n)\s*import\s*['"](\.[^'"]+)['"]/g;
    let m;
    while ((m = re.exec(src))) {
        out.add(path.normalize(path.join(path.dirname(file), m[1] || m[2])));
    }
    return [...out];
}

function importClosure(entry) {
    const seen = new Set([entry]);
    const queue = [entry];
    while (queue.length) {
        const file = queue.shift();
        for (const dep of staticImports(file)) {
            if (!seen.has(dep) && fs.existsSync(path.join(STATIC, dep))) {
                seen.add(dep);
                queue.push(dep);
            }
        }
    }
    return seen;
}

describe('index.html modulepreload hints', () => {
    const html = fs.readFileSync(path.join(STATIC, 'index.html'), 'utf8');
    const hinted = [...html.matchAll(/<link rel="modulepreload" href="([^"]+)"\s*\/?>/g)].map((m) => m[1]);

    it('hints every module in app.js\'s static import closure', () => {
        const missing = [...importClosure('app.js')].filter((f) => !hinted.includes(f));
        expect(missing).toEqual([]);
    });

    it('only hints files that exist', () => {
        const absent = hinted.filter((f) => !fs.existsSync(path.join(STATIC, f)));
        expect(absent).toEqual([]);
    });
});
