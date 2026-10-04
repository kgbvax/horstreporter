// The Help dialog: a modal around info.html (an iframe).
//
// While it is open everything else on the page is inert, so keyboard focus and
// screen readers stay inside it. Escape closes it, also when focus is inside
// the iframe (which cannot bubble key events out; info.html reports it with
// postMessage). Closing puts focus back where it was.

const FIRST_VISIT_KEY = 'infoShown';
const CLOSE_MESSAGE = 'horst-info-close';

export function initInfoDialog({ trigger, storage = window.localStorage } = {}) {
    const overlay = document.createElement('div');
    overlay.id = 'info-overlay';
    overlay.innerHTML = `
        <div class="info-content" role="dialog" aria-modal="true" aria-label="HorstReporter help">
            <button type="button" id="close-info" title="Close help" aria-label="Close help"><span aria-hidden="true">&times;</span></button>
            <iframe src="info.html" title="HorstReporter help"></iframe>
        </div>
    `;
    document.body.appendChild(overlay);
    const closeBtn = overlay.querySelector('#close-info');
    const frame = overlay.querySelector('iframe');

    let returnFocus = null;
    let inerted = [];

    const isOpen = () => overlay.style.display === 'flex';

    function open() {
        if (isOpen()) return;
        returnFocus = document.activeElement;
        overlay.style.display = 'flex';
        inerted = Array.from(document.body.children).filter((el) => el !== overlay && !el.hasAttribute('inert'));
        for (const el of inerted) el.setAttribute('inert', '');
        trigger?.setAttribute('aria-expanded', 'true');
        closeBtn.focus();
    }

    function close() {
        if (!isOpen()) return;
        overlay.style.display = 'none';
        for (const el of inerted) el.removeAttribute('inert');
        inerted = [];
        trigger?.setAttribute('aria-expanded', 'false');
        const back = returnFocus && returnFocus !== document.body && document.contains(returnFocus) ? returnFocus : trigger;
        returnFocus = null;
        back?.focus();
    }

    if (trigger) {
        trigger.setAttribute('aria-haspopup', 'dialog');
        trigger.setAttribute('aria-expanded', 'false');
        trigger.addEventListener('click', open);
    }
    closeBtn.addEventListener('click', close);
    overlay.addEventListener('click', (e) => { if (e.target === overlay) close(); });

    // Document-level listeners are replaced, not stacked, if this runs again.
    if (window.__horstInfoDialogKeydown) document.removeEventListener('keydown', window.__horstInfoDialogKeydown, true);
    window.__horstInfoDialogKeydown = (e) => {
        if (e.key !== 'Escape' || !isOpen()) return;
        e.preventDefault();
        e.stopPropagation();
        close();
    };
    document.addEventListener('keydown', window.__horstInfoDialogKeydown, true);

    if (window.__horstInfoDialogMessage) window.removeEventListener('message', window.__horstInfoDialogMessage);
    window.__horstInfoDialogMessage = (e) => {
        if (e.origin !== window.location.origin || e.source !== frame.contentWindow) return;
        if (e.data?.type === CLOSE_MESSAGE) close();
    };
    window.addEventListener('message', window.__horstInfoDialogMessage);

    // First visit: show the help once.
    let seen = true;
    try { seen = Boolean(storage.getItem(FIRST_VISIT_KEY)); } catch { /* storage blocked: stay quiet */ }
    if (!seen) {
        open();
        try { storage.setItem(FIRST_VISIT_KEY, 'true'); } catch { /* ignore */ }
    }

    return { open, close, isOpen, overlay };
}
