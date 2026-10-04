import { describe, expect, it } from 'vitest';
import { fillMercatorGraylinePixels } from '../static/grayline.js';
import { blendOverlayColors, getGraylineOverlayOpacities, getSubsolarPoint } from '../static/utils.js';

// The pre-optimisation per-pixel implementation (map.js buildMercatorGraylineDataUrl).
function mercatorYToLat(yRatio) {
    return (Math.atan(Math.sinh(Math.PI * (1 - (2 * yRatio))))) * 180 / Math.PI;
}
function referencePixels(width, height, subsolar, twilightFill, nightFill) {
    const data = new Uint8ClampedArray(width * height * 4);
    for (let y = 0; y < height; y += 1) {
        const lat = mercatorYToLat(y / (height - 1));
        for (let x = 0; x < width; x += 1) {
            const lng = -180 + ((x / (width - 1)) * 360);
            const { graylineOpacity, nightOpacity } = getGraylineOverlayOpacities(lat, lng, subsolar);
            if (graylineOpacity <= 0 && nightOpacity <= 0) continue;
            let pixel = { r: 0, g: 0, b: 0, a: 0 };
            pixel = blendOverlayColors(pixel, twilightFill, graylineOpacity);
            pixel = blendOverlayColors(pixel, nightFill, nightOpacity);
            const o = (y * width * 4) + (x * 4);
            data[o] = Math.round(pixel.r);
            data[o + 1] = Math.round(pixel.g);
            data[o + 2] = Math.round(pixel.b);
            data[o + 3] = Math.round(pixel.a * 255);
        }
    }
    return data;
}

describe('fillMercatorGraylinePixels', () => {
    const twilight = [0xb0, 0x8b, 0x72];
    const night = [0x18, 0x25, 0x34];
    const subsolarPoints = [
        { lat: 0, lng: 0 },
        { lat: 23.4, lng: -120 },
        { lat: -20, lng: 150.5 },
        { lat: 5, lng: 179.9 },
        getSubsolarPoint(new Date('2026-10-04T19:30:00Z')),
    ];

    for (const sun of subsolarPoints) {
        it(`matches the per-pixel reference for subsolar ${sun.lat.toFixed(1)},${sun.lng.toFixed(1)}`, () => {
            const w = 256;
            const h = 128;
            const ref = referencePixels(w, h, sun, twilight, night);
            const got = new Uint8ClampedArray(w * h * 4);
            fillMercatorGraylinePixels(got, w, h, sun, twilight, night);
            let maxDiff = 0;
            let off = 0;
            for (let i = 0; i < ref.length; i += 1) {
                const d = Math.abs(ref[i] - got[i]);
                if (d > maxDiff) maxDiff = d;
                if (d > 0) off += 1;
            }
            // Same maths reordered: allow a 1-step rounding difference on a handful of pixels.
            expect(maxDiff).toBeLessThanOrEqual(1);
            expect(off / ref.length).toBeLessThan(0.001);
        });
    }
});
