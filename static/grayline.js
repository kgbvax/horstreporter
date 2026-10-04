// grayline.js — pixel kernel for the Mercator day/night overlay.
//
// The overlay is a 1024x512 RGBA image rebuilt whenever the grayline time
// bucket or theme changes (map.js). The straightforward version called
// getGraylineOverlayOpacities (utils.js) per pixel, which resolves the
// subsolar point and allocates several arrays/objects each call: ~75 ms on the
// main thread at page load. This kernel produces the same pixels from
// per-row / per-column trigonometry computed once, with no per-pixel
// allocation, and skips the day half of the globe on a single compare.

// Same defaults as getGraylineOverlayOpacities (utils.js).
const TWILIGHT_WIDTH_DEG = 15;
const MAX_GRAYLINE_OPACITY = 0.18;
const MAX_NIGHT_OPACITY = 0.34;

const DEG = Math.PI / 180;

function mercatorYToLat(yRatio) {
    return (Math.atan(Math.sinh(Math.PI * (1 - (2 * yRatio))))) * 180 / Math.PI;
}

/**
 * Fill `data` (RGBA, width*height*4, zero-initialised) with the overlay for
 * `subsolar` ({lat, lng} in degrees). Pixels in full daylight stay transparent.
 * twilightFill / nightFill are [r, g, b].
 */
export function fillMercatorGraylinePixels(data, width, height, subsolar, twilightFill, nightFill) {
    const twilightHalf = TWILIGHT_WIDTH_DEG / 2;
    const dayEdge = 90 - twilightHalf;
    const nightEdge = 90 + twilightHalf;
    const cosDayEdge = Math.cos(dayEdge * DEG);

    const sunLat = subsolar.lat * DEG;
    const sunLng = subsolar.lng * DEG;
    const sunX = Math.cos(sunLat) * Math.cos(sunLng);
    const sunY = Math.cos(sunLat) * Math.sin(sunLng);
    const sunZ = Math.sin(sunLat);

    // dot(point, sun) = cosLat * colTerm[x] + sinLat * sunZ, with
    // colTerm[x] = cos(lng)*sunX + sin(lng)*sunY.
    const colTerm = new Float64Array(width);
    for (let x = 0; x < width; x += 1) {
        const lng = (-180 + ((x / (width - 1)) * 360)) * DEG;
        colTerm[x] = (Math.cos(lng) * sunX) + (Math.sin(lng) * sunY);
    }

    const [tr, tg, tb] = twilightFill;
    const [nr, ng, nb] = nightFill;

    for (let y = 0; y < height; y += 1) {
        const latRad = mercatorYToLat(y / (height - 1)) * DEG;
        const cosLat = Math.cos(latRad);
        const rowZ = Math.sin(latRad) * sunZ;
        let offset = y * width * 4;
        for (let x = 0; x < width; x += 1, offset += 4) {
            const cosine = (cosLat * colTerm[x]) + rowZ;
            // zenith <= dayEdge  <=>  cosine >= cos(dayEdge): full daylight.
            if (cosine >= cosDayEdge) continue;
            const zenith = Math.acos(cosine < -1 ? -1 : cosine) / DEG;

            let gray = 0;
            let night;
            if (zenith < nightEdge) {
                const ratio = (zenith - dayEdge) / (nightEdge - dayEdge);
                gray = MAX_GRAYLINE_OPACITY * Math.sin(Math.PI * ratio);
                night = ratio > 0.5 ? MAX_NIGHT_OPACITY * 0.35 * ((ratio - 0.5) / 0.5) : 0;
            } else {
                const nightRatio = Math.min(1, Math.max(0, (zenith - nightEdge) / (180 - nightEdge)));
                night = (MAX_NIGHT_OPACITY * 0.35) + ((MAX_NIGHT_OPACITY * 0.65) * Math.pow(nightRatio, 0.72));
            }

            // blendOverlayColors(twilight, gray) then blendOverlayColors(night):
            // over a transparent base the first blend yields the fill colour at
            // alpha `gray`; the second composites night over it.
            let r = 0;
            let g = 0;
            let b = 0;
            let a = 0;
            if (gray > 0) {
                r = tr;
                g = tg;
                b = tb;
                a = gray;
            }
            if (night > 0) {
                const next = a + (night * (1 - a));
                const wBase = a;
                const wNew = night * (1 - a);
                r = ((r * wBase) + (nr * wNew)) / next;
                g = ((g * wBase) + (ng * wNew)) / next;
                b = ((b * wBase) + (nb * wNew)) / next;
                a = next;
            }
            if (a <= 0) continue;
            data[offset] = Math.round(r);
            data[offset + 1] = Math.round(g);
            data[offset + 2] = Math.round(b);
            data[offset + 3] = Math.round(a * 255);
        }
    }
}
