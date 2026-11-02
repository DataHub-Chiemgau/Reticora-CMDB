/**
 * UI-18: the dialog is portaled under <body>, outside the App shell that gives
 * the pages their text colour. It sets the theme text colour itself, so text
 * without a colour class (the checkbox labels of the CI type field editor) is
 * not the browser default black on the dark dialog.
 */
import { screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { renderWithProviders } from '../../test/utils';
import { Modal } from './Modal';

describe('Modal theme', () => {
  it('sets the theme text colour on the portaled dialog', async () => {
    const { container } = renderWithProviders(
      <div className="text-gray-900 dark:text-gray-100">
        <Modal open onOpenChange={() => undefined} title="Feld hinzufügen">
          <label>
            <input type="checkbox" /> Eindeutig
          </label>
        </Modal>
      </div>,
    );

    const dialog = await screen.findByRole('dialog', { name: 'Feld hinzufügen' });
    // Rendered outside the tree of the caller: nothing is inherited from it.
    expect(container.contains(dialog)).toBe(false);
    expect(dialog.closest('body')).toBe(document.body);
    expect(dialog).toHaveClass(
      'bg-white',
      'text-gray-900',
      'dark:bg-gray-900',
      'dark:text-gray-100',
    );
    expect(screen.getByLabelText('Eindeutig').closest('[role="dialog"]')).toBe(dialog);
  });
});
