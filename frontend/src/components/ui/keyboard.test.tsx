import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
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

  it('wraps Shift+Tab inside the dialog with real key presses', async () => {
    const user = userEvent.setup();
    renderWithProviders(<ModalHarness />);
    await user.tab();
    const trigger = screen.getByRole('button', { name: 'open dialog' });
    expect(document.activeElement).toBe(trigger);
    await user.keyboard('{Enter}');

    const dialog = await screen.findByRole('dialog', { name: 'Edit item' });
    const field = screen.getByRole('textbox', { name: 'first field' });
    const save = screen.getByRole('button', { name: 'save' });
    const close = screen.getByRole('button', { name: 'Modal schließen' });
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));

    // Order inside the dialog: close button, field, save. Shift+Tab from the
    // first control wraps to the last one and never leaves the dialog.
    close.focus();
    await user.tab({ shift: true });
    expect(document.activeElement).toBe(save);
    await user.tab({ shift: true });
    expect(document.activeElement).toBe(field);
    await user.tab({ shift: true });
    expect(document.activeElement).toBe(close);
    await user.tab({ shift: true });
    expect(document.activeElement).toBe(save);
    expect(document.activeElement).not.toBe(trigger);
  });

  it('closes with Escape typed in an input field and returns focus to the trigger', async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    renderWithProviders(<ModalHarness onOpenChange={onOpenChange} />);
    const trigger = screen.getByRole('button', { name: 'open dialog' });
    await user.click(trigger);

    const field = await screen.findByRole('textbox', { name: 'first field' });
    await user.click(field);
    await user.keyboard('draft');
    expect(field).toHaveValue('draft');
    await user.keyboard('{Escape}');

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
  it('is a labelled native select that is focusable and changes by keyboard', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    renderWithProviders(
      <Select
        label="Status"
        options={[
          { value: 'active', label: 'Active' },
          { value: 'paused', label: 'Paused' },
          { value: 'retired', label: 'Retired' },
        ]}
        onChange={(event) => onChange(event.target.value)}
        error="Pick a status"
      />,
    );
    const select = screen.getByRole('combobox', { name: 'Status' }) as HTMLSelectElement;
    expect(select.tabIndex).toBe(0);
    // Reached with Tab, changed with the arrow keys and Home/End: every key
    // press reports the new value like a pointer choice.
    await user.tab();
    expect(document.activeElement).toBe(select);
    expect(select).toHaveAccessibleDescription('Pick a status');
    expect(select).toHaveAttribute('aria-invalid', 'true');

    await user.keyboard('{ArrowDown}');
    expect(select.value).toBe('paused');
    expect(onChange).toHaveBeenLastCalledWith('paused');
    await user.keyboard('{End}');
    expect(select.value).toBe('retired');
    // At the last option ArrowDown stays put and reports nothing.
    await user.keyboard('{ArrowDown}');
    expect(select.value).toBe('retired');
    await user.keyboard('{ArrowUp}');
    expect(select.value).toBe('paused');
    await user.keyboard('{Home}');
    expect(select.value).toBe('active');
    expect(onChange.mock.calls.map(([value]) => value)).toEqual([
      'paused',
      'retired',
      'paused',
      'active',
    ]);
    // Alt+ArrowDown opens the native option popup and changes nothing.
    await user.keyboard('{Alt>}{ArrowDown}{/Alt}');
    expect(select.value).toBe('active');
    expect(onChange).toHaveBeenCalledTimes(4);
  });

  it('skips disabled options and keeps a controlled value in sync', async () => {
    const user = userEvent.setup();
    function Controlled() {
      const [value, setValue] = useState('a');
      return (
        <>
          <Select
            label="Mode"
            value={value}
            onChange={(event) => setValue(event.target.value)}
            options={[
              { value: 'a', label: 'A' },
              { value: 'b', label: 'B' },
              { value: 'c', label: 'C' },
            ]}
          />
          <output>{value}</output>
        </>
      );
    }
    renderWithProviders(<Controlled />);
    const select = screen.getByRole('combobox', { name: 'Mode' }) as HTMLSelectElement;
    (select.options[1] as HTMLOptionElement).disabled = true;
    await user.tab();
    await user.keyboard('{ArrowDown}');
    expect(select.value).toBe('c');
    expect(screen.getByRole('status')).toHaveTextContent('c');
  });
});
