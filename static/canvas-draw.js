// @ts-check
// Canvas draw primitives: small reusable helpers that collapse the repeated
// beginPath/moveTo/lineTo/stroke boilerplate scattered across the renderers.
// Pure with respect to module state — they only touch the passed-in ctx.

/**
 * Stroke a radial line from radius r0 to r1 at a compass bearing (0=N, CW),
 * centered at (cx,cy). Optional lineWidth; caller sets strokeStyle/alpha.
 * @param {CanvasRenderingContext2D} ctx
 */
export function radialLine(ctx, cx, cy, r0, r1, bearingDeg, lineWidth) {
    const angle = ((bearingDeg - 90) * Math.PI) / 180;
    const cosA = Math.cos(angle);
    const sinA = Math.sin(angle);
    ctx.beginPath();
    ctx.moveTo(cx + (r0 * cosA), cy + (r0 * sinA));
    ctx.lineTo(cx + (r1 * cosA), cy + (r1 * sinA));
    if (lineWidth != null) ctx.lineWidth = lineWidth;
    ctx.stroke();
}

/**
 * Stroke a dashed straight line from (x0,y0) to (x1,y1); caller sets
 * strokeStyle. Restores a solid dash pattern afterward.
 * @param {CanvasRenderingContext2D} ctx
 */
export function dashedLine(ctx, x0, y0, x1, y1, dash, lineWidth) {
    if (lineWidth != null) ctx.lineWidth = lineWidth;
    ctx.setLineDash(dash);
    ctx.beginPath();
    ctx.moveTo(x0, y0);
    ctx.lineTo(x1, y1);
    ctx.stroke();
    ctx.setLineDash([]);
}

/**
 * Fill a circle at (cx,cy) radius r; caller sets fillStyle.
 * @param {CanvasRenderingContext2D} ctx
 */
export function fillCircle(ctx, cx, cy, r) {
    ctx.beginPath();
    ctx.arc(cx, cy, r, 0, Math.PI * 2);
    ctx.fill();
}

/**
 * Stroke a circle at (cx,cy) radius r; caller sets strokeStyle.
 * @param {CanvasRenderingContext2D} ctx
 */
export function strokeCircle(ctx, cx, cy, r, lineWidth) {
    ctx.beginPath();
    ctx.arc(cx, cy, r, 0, Math.PI * 2);
    if (lineWidth != null) ctx.lineWidth = lineWidth;
    ctx.stroke();
}
