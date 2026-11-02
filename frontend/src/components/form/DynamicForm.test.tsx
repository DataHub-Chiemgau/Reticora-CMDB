/**
 * GLO-10, F-343: the validation messages of the DynamicForm are shown in the
 * UI language. validateValue returns a catalog key with parameters, the form
 * translates it; server texts are shown as sent.
 */
import { afterEach, describe, expect, it } from 'vitest';
import { act, screen } from '@testing-library/react';
import i18n from '../../i18n';
import type { FieldDefinition } from '../../lib/fieldmeta';
import { renderWithProviders } from '../../test/utils';
import { DynamicForm, validateAll } from './DynamicForm';

const fields: FieldDefinition[] = [
  { name: 'hostname', label: 'Hostname', data_type: 'text', required: true },
  { name: 'cores', label: 'Kerne', data_type: 'integer', validation: { max: 64 } },
];

function renderForm(values: Record<string, unknown>, extra: Record<string, string> = {}) {
  return renderWithProviders(
    <DynamicForm
      fields={fields}
      values={values}
      errors={{ ...validateAll(fields, values), ...extra }}
      onChange={() => {}}
    />,
  );
}

afterEach(async () => {
  await act(async () => {
    await i18n.changeLanguage('de-DE');
  });
});

describe('DynamicForm validation messages', () => {
  it('names an empty required field in German', async () => {
    await act(async () => {
      await i18n.changeLanguage('de-DE');
    });
    renderForm({ hostname: '' });

    expect(screen.getByText('Hostname ist erforderlich')).toBeInTheDocument();
    expect(screen.queryByText(/is required/)).not.toBeInTheDocument();
  });

  it('names an empty required field in English', async () => {
    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    renderForm({ hostname: '' });

    expect(screen.getByText('Hostname is required')).toBeInTheDocument();
    expect(screen.queryByText(/erforderlich/)).not.toBeInTheDocument();
  });

  it('follows a language switch and fills in the parameters', async () => {
    renderForm({ hostname: 'srv-01', cores: 128 });
    expect(screen.getByText('Kerne darf höchstens 64 sein')).toBeInTheDocument();

    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    expect(screen.getByText('Kerne must be at most 64')).toBeInTheDocument();
  });

  it('shows a server message as sent', () => {
    renderForm({ hostname: 'srv-01' }, { cores: 'must be at most 60' });

    expect(screen.getByText('must be at most 60')).toBeInTheDocument();
  });
});
