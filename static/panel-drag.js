// panel-drag.js — make a floating overlay panel draggable by a handle element
// (typically its header bar). Desktop-only: on viewports <= 575px the CSS
// mobile-stack rule uses !important to win over the inline left/top this helper
// sets, and onDown bails so a touch-drag doesn't fight the stack.
//
// Position is persisted per-panel to localStorage (storageKey) and restored on
// init, so a card stays where the operator last dragged it across reloads.
//
// Conventions matched: pointer events (unify mouse + touch), a 3px click-vs-drag
// threshold so a header click never starts a drag, clamping keeps >= 40px of
// the card inside #map-stack (the offsetParent) so it can't get lost off-screen,
// and the close button (any <button> in the handle) is excluded so it stays a
// click target.

const MOVE_THRESHOLD = 3;   // px before a pointerdown counts as a drag
const EDGE_KEEP = 40;       // px of the card always kept inside the offset parent

export function makeDraggable(panel, handle, storageKey) {
    if (!panel || !handle) return;

    let dragging = false;
    let moved = false;
    let startX = 0;
    let startY = 0;
    let originLeft = 0;
    let originTop = 0;

    restorePosition();

    handle.addEventListener('pointerdown', onDown);

    function offsetParent() {
        return panel.offsetParent || document.body;
    }

    // Current left/top of the panel relative to its offsetParent, in px.
    function currentLeftTop() {
        const op = offsetParent();
        const opRect = op.getBoundingClientRect();
        const r = panel.getBoundingClientRect();
        return { left: r.left - opRect.left, top: r.top - opRect.top };
    }

    function onDown(e) {
        if (e.button != null && e.button !== 0) return;        // primary button / touch only
        if (e.target.closest('button')) return;               // close button stays clickable
        if (window.innerWidth <= 575) return;                   // mobile: CSS stack wins

        dragging = true;
        moved = false;
        startX = e.clientX;
        startY = e.clientY;
        const lt = currentLeftTop();
        originLeft = lt.left;
        originTop = lt.top;
        // Pin to explicit left/top so the drag moves in px coordinates (the
        // WSPR card is right-anchored by default; switch it to left here).
        panel.style.left = originLeft + 'px';
        panel.style.top = originTop + 'px';
        panel.style.right = 'auto';
        panel.classList.add('is-dragging');
        e.preventDefault();

        document.addEventListener('pointermove', onMove);
        document.addEventListener('pointerup', onUp);
        document.addEventListener('pointercancel', onUp);
    }

    function onMove(e) {
        if (!dragging) return;
        const dx = e.clientX - startX;
        const dy = e.clientY - startY;
        if (!moved && Math.hypot(dx, dy) < MOVE_THRESHOLD) return;
        moved = true;

        const op = offsetParent();
        const mapW = op.clientWidth;
        const mapH = op.clientHeight;
        const pw = panel.offsetWidth;
        const ph = panel.offsetHeight;
        // Keep at least EDGE_KEEP px of the card visible on each axis.
        const left = clamp(originLeft + dx, -(pw - EDGE_KEEP), mapW - EDGE_KEEP);
        const top = clamp(originTop + dy, 0, mapH - EDGE_KEEP);
        panel.style.left = left + 'px';
        panel.style.top = top + 'px';
    }

    function onUp() {
        if (!dragging) return;
        dragging = false;
        panel.classList.remove('is-dragging');
        document.removeEventListener('pointermove', onMove);
        document.removeEventListener('pointerup', onUp);
        document.removeEventListener('pointercancel', onUp);
        if (moved) savePosition();
    }

    function clamp(v, lo, hi) {
        return Math.max(lo, Math.min(v, hi));
    }

    function savePosition() {
        try {
            const left = parseFloat(panel.style.left);
            const top = parseFloat(panel.style.top);
            if (Number.isFinite(left) && Number.isFinite(top)) {
                localStorage.setItem(storageKey, JSON.stringify({ left, top }));
            }
        } catch {
            /* localStorage unavailable — non-fatal */
        }
    }

    function restorePosition() {
        if (window.innerWidth <= 575) return; // mobile uses the CSS stack
        try {
            const raw = localStorage.getItem(storageKey);
            if (!raw) return;
            const pos = JSON.parse(raw);
            if (typeof pos.left !== 'number' || typeof pos.top !== 'number') return;
            panel.style.left = pos.left + 'px';
            panel.style.top = pos.top + 'px';
            panel.style.right = 'auto';
        } catch {
            /* ignore corrupt storage */
        }
    }
}