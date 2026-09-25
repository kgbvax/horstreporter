// panel-toggle.js — shared on/off state for the map's panel toggle pills
// (Band stats, Propagation). Both stay visible while their panel is open and
// show the same "on" look (.panel-toggle.is-active, style.css), expose the
// state as aria-pressed, and title the action a click performs next.

export function setPanelToggleState(button, on, { show, hide }) {
    if (!button) return;
    button.classList.toggle('is-active', on);
    button.setAttribute('aria-pressed', on ? 'true' : 'false');
    button.title = on ? hide : show;
}
