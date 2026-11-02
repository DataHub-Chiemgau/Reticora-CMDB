import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useRowNavigation } from './useRowNavigation';

// UI-04 / UI-10 (WP-519): the table keyboard pattern on its own.

function TableHarness({
  ids,
  onOpen,
  onToggle,
}: {
  ids: string[];
  onOpen: (id: string) => void;
  onToggle?: (id: string) => void;
}) {
  const { getRowProps } = useRowNavigation({ rowIds: ids, onOpen, onToggle });
  return (
    <>
      <button type="button">before</button>
      <table>
        <tbody>
          {ids.map((id) => (
            <tr key={id} data-testid={`row-${id}`} {...getRowProps(id)}>
              <td>
                <input type="checkbox" aria-label={`select ${id}`} tabIndex={-1} />
              </td>
              <td>{id}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <button type="button">after</button>
    </>
  );
}

function rows() {
  return screen.getAllByRole('row');
}

function tabStops() {
  return rows().filter((row) => row.tabIndex === 0);
}

describe('useRowNavigation', () => {
  it('keeps exactly one row in the Tab order (roving tabindex)', async () => {
    const user = userEvent.setup();
    render(<TableHarness ids={['a', 'b', 'c']} onOpen={vi.fn()} />);

    expect(tabStops()).toEqual([screen.getByTestId('row-a')]);
    rows()
      .slice(1)
      .forEach((row) => expect(row.tabIndex).toBe(-1));

    // Tab enters the table once and leaves it with the next Tab.
    await user.tab();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'before' }));
    await user.tab();
    expect(document.activeElement).toBe(screen.getByTestId('row-a'));
    await user.tab();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'after' }));

    // After moving to another row that row becomes the single Tab stop, and
    // Shift+Tab from "after" comes back to it.
    act(() => screen.getByTestId('row-a').focus());
    await user.keyboard('{ArrowDown}');
    expect(tabStops()).toEqual([screen.getByTestId('row-b')]);
    act(() => screen.getByRole('button', { name: 'after' }).focus());
    await user.tab({ shift: true });
    expect(document.activeElement).toBe(screen.getByTestId('row-b'));
  });

  it('moves with ArrowUp/ArrowDown and jumps with Home/End', async () => {
    const user = userEvent.setup();
    render(<TableHarness ids={['a', 'b', 'c', 'd']} onOpen={vi.fn()} />);
    await user.tab();
    await user.tab();
    expect(document.activeElement).toBe(screen.getByTestId('row-a'));

    await user.keyboard('{ArrowDown}');
    expect(document.activeElement).toBe(screen.getByTestId('row-b'));
    await user.keyboard('{ArrowDown}{ArrowDown}');
    expect(document.activeElement).toBe(screen.getByTestId('row-d'));
    // The last row stays active on ArrowDown, the first on ArrowUp.
    await user.keyboard('{ArrowDown}');
    expect(document.activeElement).toBe(screen.getByTestId('row-d'));
    await user.keyboard('{ArrowUp}');
    expect(document.activeElement).toBe(screen.getByTestId('row-c'));
    await user.keyboard('{Home}');
    expect(document.activeElement).toBe(screen.getByTestId('row-a'));
    await user.keyboard('{ArrowUp}');
    expect(document.activeElement).toBe(screen.getByTestId('row-a'));
    await user.keyboard('{End}');
    expect(document.activeElement).toBe(screen.getByTestId('row-d'));
    expect(tabStops()).toEqual([screen.getByTestId('row-d')]);
  });

  it('opens the row with Enter and toggles its selection with Space', async () => {
    const user = userEvent.setup();
    const onOpen = vi.fn();
    const onToggle = vi.fn();
    render(<TableHarness ids={['a', 'b']} onOpen={onOpen} onToggle={onToggle} />);
    act(() => screen.getByTestId('row-a').focus());

    await user.keyboard('{ArrowDown}{Enter}');
    expect(onOpen).toHaveBeenCalledTimes(1);
    expect(onOpen).toHaveBeenCalledWith('b');

    await user.keyboard(' ');
    expect(onToggle).toHaveBeenCalledTimes(1);
    expect(onToggle).toHaveBeenCalledWith('b');
    await user.keyboard('{ArrowUp} ');
    expect(onToggle).toHaveBeenLastCalledWith('a');
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  it('leaves Enter and Space to a control inside the row', async () => {
    const user = userEvent.setup();
    const onOpen = vi.fn();
    const onToggle = vi.fn();
    render(<TableHarness ids={['a', 'b']} onOpen={onOpen} onToggle={onToggle} />);
    const box = screen.getByRole('checkbox', { name: 'select a' });
    act(() => box.focus());

    // Space checks the native checkbox, the row callback stays silent.
    await user.keyboard(' ');
    expect(box).toBeChecked();
    await user.keyboard('{Enter}');
    expect(onToggle).not.toHaveBeenCalled();
    expect(onOpen).not.toHaveBeenCalled();
    // The arrow keys still move to the next row.
    await user.keyboard('{ArrowDown}');
    expect(document.activeElement).toBe(screen.getByTestId('row-b'));
  });

  it('follows a row focused with the pointer and survives a changed row list', async () => {
    const user = userEvent.setup();
    function Paged() {
      const [ids, setIds] = useState(['a', 'b', 'c']);
      return (
        <>
          <button type="button" onClick={() => setIds(['x', 'y'])}>
            next page
          </button>
          <TableHarness ids={ids} onOpen={vi.fn()} />
        </>
      );
    }
    render(<Paged />);
    await user.click(screen.getByText('c'));
    expect(document.activeElement).toBe(screen.getByTestId('row-c'));
    expect(tabStops()).toEqual([screen.getByTestId('row-c')]);

    // The remembered row is gone after paging: the first row is the Tab stop.
    await user.click(screen.getByRole('button', { name: 'next page' }));
    expect(tabStops()).toEqual([screen.getByTestId('row-x')]);
  });
});
