import { useCallback, useRef, useState } from 'react';
import type { FocusEvent, KeyboardEvent } from 'react';

export interface RowNavigationOptions {
  /** Ids of the rendered rows, in display order. */
  rowIds: readonly string[];
  /** Enter on a row: open its detail. */
  onOpen: (id: string) => void;
  /** Space on a row: toggle its selection. Without it Space keeps its default. */
  onToggle?: (id: string) => void;
}

export interface RowNavigationProps {
  ref: (element: HTMLElement | null) => void;
  tabIndex: 0 | -1;
  onKeyDown: (event: KeyboardEvent<HTMLElement>) => void;
  onFocus: (event: FocusEvent<HTMLElement>) => void;
}

/**
 * useRowNavigation is the keyboard pattern for data tables (UI-04, UI-10,
 * WP-519): a roving tabindex keeps exactly one row in the Tab order, the
 * arrow keys move between rows, Home/End jump to the first/last row, Enter
 * opens the row and Space toggles its selection. Controls inside a row are
 * meant to be taken out of the Tab order (tabIndex -1); they stay usable with
 * the pointer, and the arrow keys keep working while one of them has focus.
 */
export function useRowNavigation({ rowIds, onOpen, onToggle }: RowNavigationOptions) {
  const [activeId, setActiveId] = useState<string | undefined>(undefined);
  const rows = useRef(new Map<string, HTMLElement>());
  // The remembered row drops out of the list after paging or filtering; the
  // first row then takes its place in the Tab order.
  const currentId = activeId !== undefined && rowIds.includes(activeId) ? activeId : rowIds[0];

  const focusRow = useCallback((id: string | undefined) => {
    if (id === undefined) {
      return;
    }
    setActiveId(id);
    rows.current.get(id)?.focus();
  }, []);

  function getRowProps(id: string): RowNavigationProps {
    return {
      ref: (element) => {
        if (element) {
          rows.current.set(id, element);
        } else {
          rows.current.delete(id);
        }
      },
      tabIndex: id === currentId ? 0 : -1,
      onFocus: () => setActiveId(id),
      onKeyDown: (event) => {
        if (event.altKey || event.ctrlKey || event.metaKey) {
          return;
        }
        const index = rowIds.indexOf(id);
        // Enter and Space belong to the row itself; on a control inside the
        // row (link, checkbox) they keep their native meaning.
        const onRow = event.target === event.currentTarget;
        switch (event.key) {
          case 'ArrowDown':
            event.preventDefault();
            focusRow(rowIds[Math.min(index + 1, rowIds.length - 1)]);
            return;
          case 'ArrowUp':
            event.preventDefault();
            focusRow(rowIds[Math.max(index - 1, 0)]);
            return;
          case 'Home':
            event.preventDefault();
            focusRow(rowIds[0]);
            return;
          case 'End':
            event.preventDefault();
            focusRow(rowIds[rowIds.length - 1]);
            return;
          case 'Enter':
            if (onRow) {
              event.preventDefault();
              onOpen(id);
            }
            return;
          case ' ':
            if (onRow && onToggle) {
              event.preventDefault();
              onToggle(id);
            }
            return;
          default:
        }
      },
    };
  }

  return { activeId: currentId, getRowProps };
}
