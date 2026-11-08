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
import { DynamicForm, validateAll, type DynamicFormSection } from './DynamicForm';

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

/**
 * WP-803 (MET-52, UI-11): without layout the fields stay grouped by
 * ui_group and sort_order with the live conditional evaluation; with layout
 * sections and fields follow the layout, titles in the UI language, widths
 * in a 12-column grid, and the server state is combined with the live one.
 */
const grouped: FieldDefinition[] = [
  { name: 'b', label: 'B', data_type: 'text', ui_group: 'Netz', sort_order: 2 },
  { name: 'a', label: 'A', data_type: 'text', ui_group: 'Netz', sort_order: 1 },
  { name: 'c', label: 'C', data_type: 'text', ui_group: 'Basis', sort_order: 0 },
  {
    name: 'detail',
    label: 'Detail',
    data_type: 'text',
    ui_group: 'Netz',
    sort_order: 3,
    conditional: { visible_when: [{ field: 'a', op: 'eq', value: 'yes' }] },
  },
];

const labelsIn = (legend: string) =>
  Array.from(
    screen.getByText(legend, { selector: 'legend' }).closest('fieldset')!.querySelectorAll('label'),
  ).map((l) => l.textContent);

describe('DynamicForm without layout (regression)', () => {
  it('groups by ui_group in sort_order', () => {
    renderWithProviders(<DynamicForm fields={grouped} values={{}} onChange={() => {}} />);
    const legends = screen.getAllByText(/^(Netz|Basis)$/, { selector: 'legend' });
    expect(legends.map((l) => l.textContent)).toEqual(['Basis', 'Netz']);
    expect(labelsIn('Netz')).toEqual(['A', 'B']);
  });

  it('evaluates conditional visibility live', () => {
    const { rerender } = renderWithProviders(
      <DynamicForm fields={grouped} values={{ a: 'no' }} onChange={() => {}} />,
    );
    expect(screen.queryByLabelText('Detail')).not.toBeInTheDocument();
    rerender(<DynamicForm fields={grouped} values={{ a: 'yes' }} onChange={() => {}} />);
    expect(screen.getByLabelText('Detail')).toBeInTheDocument();
  });
});

const layoutFields: FieldDefinition[] = [
  { name: 'host', label: 'Host', data_type: 'text', ui_group: 'X', sort_order: 9 },
  { name: 'vlan', label: 'VLAN', data_type: 'integer', ui_group: 'Y', sort_order: 1 },
  { name: 'note', label: 'Notiz', data_type: 'text' },
  { name: 'hidden_srv', label: 'Versteckt', data_type: 'text' },
  {
    name: 'cond',
    label: 'Bedingt',
    data_type: 'text',
    conditional: { visible_when: [{ field: 'host', op: 'eq', value: 'show' }] },
  },
];

const layout: DynamicFormSection[] = [
  {
    key: 'net',
    title: { de: 'Netzwerk', en: 'Network' },
    visible: true,
    fields: [
      { name: 'vlan', width: 4 },
      { name: 'missing', width: 6 },
      { name: 'host', width: 8, state: { visible: true, required: true, readOnly: true } },
      { name: 'hidden_srv', width: 6, state: { visible: false, required: false, readOnly: false } },
      { name: 'cond', width: 12, state: { visible: false, required: false, readOnly: false } },
    ],
  },
  {
    key: 'off',
    title: { de: 'Aus', en: 'Off' },
    visible: false,
    fields: [{ name: 'note', width: 12 }],
  },
];

function renderLayout(values: Record<string, unknown> = {}) {
  return renderWithProviders(
    <DynamicForm fields={layoutFields} values={values} layout={layout} onChange={() => {}} />,
  );
}

describe('DynamicForm with layout (WP-803)', () => {
  it('follows the layout order and skips names without definition', () => {
    const { container } = renderLayout();
    const names = Array.from(container.querySelectorAll('[data-field-name]')).map((f) =>
      f.getAttribute('data-field-name'),
    );
    expect(names).toEqual(['vlan', 'host']);
    expect(container.querySelectorAll('[data-section-key]')).toHaveLength(1);
  });

  it('titles sections in the UI language', async () => {
    renderLayout();
    expect(screen.getByText('Netzwerk', { selector: 'legend' })).toBeInTheDocument();
    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    expect(screen.getByText('Network', { selector: 'legend' })).toBeInTheDocument();
  });

  it('sets the width in the 12-column grid', () => {
    const { container } = renderLayout();
    const vlan = container.querySelector('[data-field-name="vlan"]')!;
    expect(vlan.className).toContain('sm:col-span-4');
    expect(vlan.parentElement!.className).toContain('sm:grid-cols-12');
    expect(container.querySelector('[data-field-name="host"]')!.className).toContain(
      'sm:col-span-8',
    );
  });

  it('hides a section with visible false', () => {
    renderLayout();
    expect(screen.queryByText('Aus')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Notiz')).not.toBeInTheDocument();
  });

  it('keeps read only and required of the server across inputs', () => {
    const { rerender } = renderLayout({ host: '' });
    expect(screen.getByLabelText('Host *')).toBeDisabled();
    expect(screen.getByLabelText('Host *')).toBeRequired();
    rerender(
      <DynamicForm
        fields={layoutFields}
        values={{ host: 'x', vlan: 3 }}
        layout={layout}
        onChange={() => {}}
      />,
    );
    expect(screen.getByLabelText('Host *')).toBeDisabled();
    expect(screen.getByLabelText('Host *')).toBeRequired();
  });

  it('applies the server visibility to fields without conditional only', () => {
    const { rerender } = renderLayout({ host: '' });
    expect(screen.queryByLabelText('Versteckt')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Bedingt')).not.toBeInTheDocument();
    rerender(
      <DynamicForm
        fields={layoutFields}
        values={{ host: 'show' }}
        layout={layout}
        onChange={() => {}}
      />,
    );
    expect(screen.getByLabelText('Bedingt')).toBeInTheDocument();
    expect(screen.queryByLabelText('Versteckt')).not.toBeInTheDocument();
  });
});
