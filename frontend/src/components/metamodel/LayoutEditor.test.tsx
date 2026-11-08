/**
 * WP-316 (MET-50, UI-11 [P2]): the layout editor of the form builder.
 *
 *   Sections   add, title (without a German or English title the layout is
 *              not savable), order and delete
 *   Fields     place from the palette, move inside a section with the
 *              keyboard (Space, arrow, Space; Escape cancels) and with the
 *              buttons, move between sections, remove; width 1 to 12
 *   Rules      visible_when only from the catalog rules of the type
 *   A11y       announcements in DE and EN in the aria-live region; every
 *              control found by role and accessible name; data type and
 *              required mark are read-only text
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/metamodel/LayoutEditor.test.tsx
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, within } from '@testing-library/react';
import { useState } from 'react';
import i18n from '../../i18n';
import type { FormLayoutDocument } from '../../api/cmdb';
import { renderWithProviders } from '../../test/utils';
import {
  LayoutEditor,
  layoutComplete,
  refusedAt,
  type LayoutEditorProps,
  type PaletteField,
} from './LayoutEditor';

const PALETTE: PaletteField[] = [
  {
    name: 'vendor',
    label: 'Hersteller',
    dataType: 'text',
    required: true,
    origin: { kind: 'own' },
  },
  {
    name: 'ports',
    label: 'Ports',
    dataType: 'integer',
    required: false,
    origin: { kind: 'inherited', from: 'Netzwerk' },
  },
  {
    name: 'cost',
    label: 'Kostenstelle',
    dataType: 'text',
    required: false,
    origin: { kind: 'global' },
  },
  {
    name: 'vlan',
    label: 'VLAN',
    dataType: 'integer',
    required: false,
    origin: { kind: 'set', name: 'Netzset' },
  },
];

const TWO_SECTIONS: FormLayoutDocument = {
  sections: [
    {
      key: 'main',
      title: { de: 'Allgemein', en: 'General' },
      visible_when: [],
      fields: [
        { name: 'ports', width: 6 },
        { name: 'cost', width: 12 },
      ],
    },
    { key: 'net', title: { de: 'Netz', en: 'Network' }, visible_when: [], fields: [] },
  ],
};

/** The editor with its state, the last value readable through latest(). */
function renderEditor(initial: FormLayoutDocument, props: Partial<LayoutEditorProps> = {}) {
  let latest = initial;
  function Host() {
    const [doc, setDoc] = useState(initial);
    latest = doc;
    return (
      <LayoutEditor
        value={doc}
        onChange={setDoc}
        palette={PALETTE}
        ruleKeys={['is_core', 'has_uplink']}
        {...props}
      />
    );
  }
  renderWithProviders(<Host />);
  return { latest: () => latest };
}

/** Lays the entries of a sortable list out one below the other (jsdom has no layout). */
function layoutRows() {
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
    const index = this.getAttribute('data-sortable-index');
    const top = index === null ? 0 : Number(index) * 40;
    const height = index === null ? 0 : 40;
    const width = index === null ? 0 : 400;
    return {
      x: 0,
      y: top,
      top,
      left: 0,
      width,
      height,
      right: width,
      bottom: top + height,
      toJSON: () => ({}),
    } as DOMRect;
  });
}

/** The KeyboardSensor listens for the next key after a tick. */
async function key(target: Element, code: string) {
  await act(async () => {
    fireEvent.keyDown(target, { key: code === 'Space' ? ' ' : code, code });
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

const fieldNames = (doc: FormLayoutDocument, section: number) =>
  doc.sections[section]!.fields.map((f) => f.name);

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
});

afterEach(async () => {
  vi.restoreAllMocks();
  await act(async () => {
    await i18n.changeLanguage('de-DE');
  });
});

describe('LayoutEditor sections', () => {
  it('adds a section that is not savable until it has a German and an English title', () => {
    const { latest } = renderEditor({ sections: [] });
    expect(screen.getByText('Noch keine Abschnitte.')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Abschnitt hinzufügen' }));
    expect(latest().sections.map((s) => s.key)).toEqual(['section_1']);
    expect(layoutComplete(latest())).toBe(false);
    const de = screen.getByRole('textbox', { name: 'Titel (DE) von section_1' });
    const en = screen.getByRole('textbox', { name: 'Titel (EN) von section_1' });
    expect(de).toHaveAttribute('aria-invalid', 'true');

    fireEvent.change(de, { target: { value: 'Technik' } });
    expect(layoutComplete(latest())).toBe(false);
    expect(en).toHaveAccessibleDescription('Titel fehlt');
    fireEvent.change(en, { target: { value: 'Technical' } });
    expect(layoutComplete(latest())).toBe(true);
    expect(latest().sections[0]!.title).toEqual({ de: 'Technik', en: 'Technical' });
  });

  it('orders sections with the buttons and deletes one', () => {
    const { latest } = renderEditor(TWO_SECTIONS);
    const sections = screen.getByRole('list', { name: 'Abschnitte' });
    expect(within(sections).getByRole('button', { name: 'Nach oben: Allgemein' })).toBeDisabled();

    fireEvent.click(within(sections).getByRole('button', { name: 'Nach unten: Allgemein' }));
    expect(latest().sections.map((s) => s.key)).toEqual(['net', 'main']);

    fireEvent.click(screen.getByRole('button', { name: 'Abschnitt löschen: Netz' }));
    expect(latest().sections.map((s) => s.key)).toEqual(['main']);
    expect(screen.getByText('Abschnitt Netz gelöscht.')).toBeInTheDocument();
  });
});

describe('LayoutEditor fields', () => {
  it('places a field from the palette into the chosen section with width 12', () => {
    const { latest } = renderEditor(TWO_SECTIONS);
    const palette = screen.getByRole('list', { name: 'Feldpalette' });
    // Placed fields leave the palette.
    expect(within(palette).queryByText('Ports')).not.toBeInTheDocument();
    expect(within(palette).getByText('Hersteller')).toBeInTheDocument();

    fireEvent.change(screen.getByRole('combobox', { name: 'Zielabschnitt' }), {
      target: { value: 'net' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Platzieren: VLAN' }));

    expect(latest().sections[1]!.fields).toEqual([{ name: 'vlan', width: 12 }]);
    expect(within(palette).queryByText('VLAN')).not.toBeInTheDocument();
    expect(screen.getByText('VLAN in Abschnitt Netz platziert.')).toBeInTheDocument();
  });

  it('shows required fields without a place as a note', () => {
    renderEditor(TWO_SECTIONS);
    expect(screen.getByRole('note')).toHaveTextContent(
      'Pflichtfelder ohne Platz im Layout: Hersteller.',
    );
    fireEvent.click(screen.getByRole('button', { name: 'Platzieren: Hersteller' }));
    expect(screen.queryByRole('note')).not.toBeInTheDocument();
  });

  it('moves a field with the keyboard: Space, arrow, Space', async () => {
    layoutRows();
    const { latest } = renderEditor(TWO_SECTIONS);
    const handle = screen.getByRole('button', { name: 'Verschieben: Ports' });
    handle.focus();
    await key(handle, 'Space');
    expect(await screen.findByText('Ports aufgenommen, Position 1 von 2.')).toBeInTheDocument();
    await key(handle, 'ArrowDown');
    await key(handle, 'Space');

    expect(fieldNames(latest(), 0)).toEqual(['cost', 'ports']);
  });

  it('cancels a keyboard move with Escape', async () => {
    layoutRows();
    const { latest } = renderEditor(TWO_SECTIONS);
    const handle = screen.getByRole('button', { name: 'Verschieben: Ports' });
    handle.focus();
    await key(handle, 'Space');
    await key(handle, 'ArrowDown');
    await key(handle, 'Escape');

    expect(
      await screen.findByText('Verschieben abgebrochen. Ports bleibt auf Position 1 von 2.'),
    ).toBeInTheDocument();
    expect(fieldNames(latest(), 0)).toEqual(['ports', 'cost']);
  });

  it('moves a field with the buttons and into another section, and removes it', () => {
    const { latest } = renderEditor(TWO_SECTIONS);
    const fields = screen.getByRole('list', { name: 'Felder von Allgemein' });
    fireEvent.click(within(fields).getByRole('button', { name: 'Nach oben: Kostenstelle' }));
    expect(fieldNames(latest(), 0)).toEqual(['cost', 'ports']);

    fireEvent.change(screen.getByRole('combobox', { name: 'In Abschnitt verschieben: Ports' }), {
      target: { value: 'net' },
    });
    expect(fieldNames(latest(), 0)).toEqual(['cost']);
    expect(latest().sections[1]!.fields).toEqual([{ name: 'ports', width: 6 }]);
    expect(
      screen.getByText('Ports in Abschnitt Netz verschoben, Position 1 von 1.'),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Entfernen: Ports' }));
    expect(latest().sections[1]!.fields).toEqual([]);
    expect(
      within(screen.getByRole('list', { name: 'Feldpalette' })).getByText('Ports'),
    ).toBeInTheDocument();
  });

  it('offers the widths 1 to 12', () => {
    const { latest } = renderEditor(TWO_SECTIONS);
    const width = screen.getByRole('combobox', { name: 'Breite: Ports' });
    expect(
      within(width)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual(['1', '2', '3', '4', '5', '6', '7', '8', '9', '10', '11', '12']);
    expect(width).toHaveValue('6');
    fireEvent.change(width, { target: { value: '4' } });
    expect(latest().sections[0]!.fields[0]).toEqual({ name: 'ports', width: 4 });
  });

  it('shows data type, required mark and origin as read-only text', () => {
    renderEditor(TWO_SECTIONS);
    const palette = screen.getByRole('list', { name: 'Feldpalette' });
    const vendor = palette.querySelector<HTMLElement>('[data-palette-field="vendor"]')!;
    expect(within(vendor).getByText('Pflicht')).toBeInTheDocument();
    expect(within(vendor).getByText('eigen')).toBeInTheDocument();
    expect(within(vendor).queryByRole('textbox')).not.toBeInTheDocument();
    expect(within(vendor).queryByRole('checkbox')).not.toBeInTheDocument();
    expect(within(vendor).queryByRole('combobox')).not.toBeInTheDocument();
    const placed = screen.getByRole('list', { name: 'Felder von Allgemein' });
    expect(within(placed).getByText('geerbt von Netzwerk')).toBeInTheDocument();
    expect(within(placed).getByText('global')).toBeInTheDocument();
    const vlan = palette.querySelector<HTMLElement>('[data-palette-field="vlan"]')!;
    expect(within(vlan).getByText('Set Netzset')).toBeInTheDocument();
  });
});

describe('LayoutEditor rules and refusals', () => {
  it('offers visible_when only from the catalog rules', () => {
    const { latest } = renderEditor(TWO_SECTIONS);
    const group = screen.getByRole('group', {
      name: 'Sichtbar, wenn die Regeln gelten: Allgemein',
    });
    expect(
      within(group)
        .getAllByRole('checkbox')
        .map((c) => c.closest('label')!.textContent),
    ).toEqual(['is_core', 'has_uplink']);
    fireEvent.click(within(group).getByRole('checkbox', { name: 'has_uplink' }));
    expect(latest().sections[0]!.visible_when).toEqual(['has_uplink']);
    fireEvent.click(within(group).getByRole('checkbox', { name: 'has_uplink' }));
    expect(latest().sections[0]!.visible_when).toEqual([]);
  });

  it('marks the field a 422 names by its path', () => {
    expect(refusedAt('layout.sections[1].fields[0].name')).toEqual({
      section: 1,
      field: 0,
      part: 'name',
    });
    const doc: FormLayoutDocument = {
      sections: [
        TWO_SECTIONS.sections[0]!,
        { ...TWO_SECTIONS.sections[1]!, fields: [{ name: 'gone', width: 12 }] },
      ],
    };
    renderEditor(doc, {
      invalidPath: 'layout.sections[1].fields[0].name',
      invalidMessage: 'the target has no field with this name',
    });
    const refused = document.querySelector<HTMLElement>('[data-field-name="gone"]')!;
    expect(refused).toHaveAttribute('data-invalid', 'true');
    expect(within(refused).getByRole('alert')).toHaveTextContent(
      'the target has no field with this name',
    );
    expect(within(refused).getByText('Kein Attribut des Ziels')).toBeInTheDocument();
    expect(document.querySelector('[data-field-name="ports"]')).not.toHaveAttribute('data-invalid');
  });
});

describe('LayoutEditor announcements', () => {
  it('announces moves in German and English in the live region', async () => {
    layoutRows();
    renderEditor(TWO_SECTIONS);
    fireEvent.change(screen.getByRole('combobox', { name: 'In Abschnitt verschieben: Ports' }), {
      target: { value: 'net' },
    });
    const live = screen
      .getAllByRole('status')
      .find((el) => el.textContent?.includes('in Abschnitt Netz verschoben'));
    expect(live).toHaveAttribute('aria-live', 'polite');

    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    fireEvent.change(screen.getByRole('combobox', { name: 'Move to section: Kostenstelle' }), {
      target: { value: 'net' },
    });
    expect(
      screen.getByText('Kostenstelle moved to section Network, position 2 of 2.'),
    ).toBeInTheDocument();

    const handle = screen.getByRole('button', { name: 'Move: Ports' });
    handle.focus();
    await key(handle, 'Space');
    expect(await screen.findByText('Picked up Ports, position 1 of 2.')).toBeInTheDocument();
    await key(handle, 'Escape');
  });

  it('names every control by role and accessible name', () => {
    renderEditor(TWO_SECTIONS);
    for (const name of [
      'Abschnitt hinzufügen',
      'Abschnitt löschen: Allgemein',
      'Verschieben: Allgemein',
      'Nach unten: Allgemein',
      'Verschieben: Ports',
      'Nach unten: Ports',
      'Entfernen: Ports',
      'Platzieren: Hersteller',
    ]) {
      expect(screen.getByRole('button', { name })).toBeInTheDocument();
    }
    for (const name of ['Zielabschnitt', 'Breite: Ports', 'In Abschnitt verschieben: Ports']) {
      expect(screen.getByRole('combobox', { name })).toBeInTheDocument();
    }
    expect(screen.getByRole('textbox', { name: 'Titel (DE) von main' })).toHaveValue('Allgemein');
    expect(screen.getByRole('textbox', { name: 'Titel (EN) von main' })).toHaveValue('General');
  });
});
