/** The palette listens for this event on window (CommandPalette.tsx). */
export const OPEN_PALETTE_EVENT = 'reticora:open-palette';

/** openCommandPalette opens the palette like Ctrl+K does. */
export function openCommandPalette() {
  window.dispatchEvent(new CustomEvent(OPEN_PALETTE_EVENT));
}
