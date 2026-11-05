import { useSyncExternalStore } from 'react';

/**
 * useMediaQuery follows a CSS media query. Use it only where the markup
 * itself must differ (one control instead of two copies); layout belongs
 * to CSS.
 */
export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (notify) => {
      if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
        return () => undefined;
      }
      const list = window.matchMedia(query);
      list.addEventListener('change', notify);
      return () => list.removeEventListener('change', notify);
    },
    () =>
      typeof window !== 'undefined' && typeof window.matchMedia === 'function'
        ? window.matchMedia(query).matches
        : false,
    () => false,
  );
}
