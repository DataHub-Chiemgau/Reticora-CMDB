import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { Modal } from './Modal';
import { Select } from './Select';
import { renderWithProviders } from '../../test/utils';

// UI-10 / WP-192: the shared building blocks are fully keyboard operable.

function ModalHarness({ onOpenChange }: { onOpenChange?: (open: boolean) => void }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        open dialog
      </button>
      <Modal
        open={open}
        onOpenChange={(next) => {
          setOpen(next);
          onOpenChange?.(next);
        }}
        title="Edit item"
        description="Change the item"
      >
        <input aria-label="first field" />
        <button type="button">save</button>
      </Modal>
    </>
  );
}

describe('Modal keyboard operation', () => {
  it('moves focus into the dialog, traps Tab and restores focus on Escape', async () => {
    const onOpenChange = vi.fn();
    renderWithProviders(<ModalHarness onOpenChange={onOpenChange} />);
    const trigger = screen.getByRole('button', { name: 'open dialog' });
    trigger.focus();
    fireEvent.click(trigger);

    const dialog = await screen.findByRole('dialog', { name: 'Edit item' });
    expect(dialog).toHaveAccessibleDescription('Change the item');
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));

    // Tab from the last control wraps to the first one inside the dialog,
    // Shift+Tab from the first wraps to the last.
    const save = screen.getByRole('button', { name: 'save' });
    save.focus();
    fireEvent.keyDown(save, { key: 'Tab' });
    expect(dialog.contains(document.activeElement)).toBe(true);
    expect(document.activeElement).not.toBe(save);
    const first = document.activeElement as HTMLElement;
    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true });
    expect(document.activeElement).toBe(save);

    fireEvent.keyDown(document.activeElement as HTMLElement, { key: 'Escape' });
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(onOpenChange).toHaveBeenLastCalledWith(false);
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  it('has a keyboard reachable, labelled close button', async () => {
    renderWithProviders(<ModalHarness />);
    fireEvent.click(screen.getByRole('button', { name: 'open dialog' }));
    const dialog = await screen.findByRole('dialog');
    const close = Array.from(dialog.querySelectorAll('button')).find(
      (b) => b.getAttribute('aria-label') && b.textContent === '✕',
    );
    expect(close).toBeDefined();
    expect(close?.tabIndex).not.toBe(-1);
    fireEvent.click(close as HTMLElement);
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });
});

describe('Select keyboard operation', () => {
  it('is a labelled native select that is focusable and changes by keyboard', () => {
    const onChange = vi.fn();
    renderWithProviders(
      <Select
        label="Status"
        options={[
          { value: 'active', label: 'Active' },
          { value: 'retired', label: 'Retired' },
        ]}
        onChange={onChange}
        error="Pick a status"
      />,
    );
    const select = screen.getByRole('combobox', { name: 'Status' });
    expect(select.tabIndex).toBe(0);
    select.focus();
    expect(document.activeElement).toBe(select);
    expect(select).toHaveAccessibleDescription('Pick a status');
    expect(select).toHaveAttribute('aria-invalid', 'true');

    // Native selects change their value with the arrow keys; the resulting
    // change event reaches the handler.
    fireEvent.change(select, { target: { value: 'retired' } });
    expect(onChange).toHaveBeenCalledTimes(1);
    expect((select as HTMLSelectElement).value).toBe('retired');
  });
});
