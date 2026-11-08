/**
 * LayoutEditor edits the layout of a form (MET-50, UI-11, WP-316) in the
 * form of WP-259: sections with a key, a title in German and English
 * (MET-01), the catalog rules that show them (visible_when) and their
 * fields with a width of 1 to 12 grid columns.
 *
 * Sections and the fields of a section are ordered with SortableList
 * (@dnd-kit, E-54, WP-797): drag and drop, the KeyboardSensor (Space picks
 * up, the arrow keys move, Space drops, Escape cancels) and the buttons
 * "up" and "down". A field moves to another section with "move to
 * section"; the field palette places the attributes of the target. The
 * palette only reads the data model: data type, required mark and origin
 * are shown, never changed (MET-50). Moves, placements and removals are
 * announced in the live region of the editor.
 */
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus, Trash2, X } from 'lucide-react';
import type { FormLayoutDocument, FormLayoutField, FormLayoutSection } from '../../api/cmdb';
import { Badge } from '../ui/Badge';
import { Button, IconButton } from '../ui/Button';
import { Input } from '../ui/Input';
import { Select } from '../ui/Select';
import { SortableList } from './SortableList';
import { cn } from '../ui/utils';

/** Where an attribute of the palette comes from (as in the attribute catalog, WP-313). */
export type FieldOrigin =
  | { kind: 'own' }
  | { kind: 'inherited'; from: string }
  | { kind: 'set'; name: string }
  | { kind: 'global' };

/** An attribute the layout may place: read-only data of the data model. */
export interface PaletteField {
  /** The field name (attribute key). */
  name: string;
  /** The display name in the UI language. */
  label: string;
  dataType: string;
  required: boolean;
  origin: FieldOrigin;
}

export interface LayoutEditorProps {
  value: FormLayoutDocument;
  onChange: (next: FormLayoutDocument) => void;
  palette: PaletteField[];
  /** The keys of the catalog rules of the type (GET /rules?ci_type_id=). */
  ruleKeys: string[];
  /** The path of a value the server refused (422 field), e.g. layout.sections[1].fields[0].name. */
  invalidPath?: string;
  invalidMessage?: string;
  disabled?: boolean;
}

/** The widths of a field: 1 to 12 grid columns. */
export const LAYOUT_WIDTHS = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12] as const;

/** Section keys the resolved form uses itself (WP-719). */
export const RESERVED_SECTION_KEYS = ['_instance', '_required'] as const;

/** layoutComplete reports whether every section has a title in German and English (MET-01). */
export function layoutComplete(doc: FormLayoutDocument): boolean {
  return doc.sections.every((s) => s.title.de.trim() !== '' && s.title.en.trim() !== '');
}

/** placedNames are the field names the layout places. */
export function placedNames(doc: FormLayoutDocument): Set<string> {
  return new Set(doc.sections.flatMap((s) => s.fields.map((f) => f.name)));
}

/** The position a refused path names: a section, optionally a field of it. */
interface RefusedAt {
  section: number;
  field?: number;
  /** The part of the section or field: title, key, visible_when, name, width. */
  part?: string;
}

/** refusedAt reads a path of a 422 field (WP-259) into a section and field position. */
export function refusedAt(path: string | undefined): RefusedAt | undefined {
  if (!path) return undefined;
  const match = /^layout\.sections\[(\d+)\](?:\.fields\[(\d+)\])?(?:\.([a-z_]+))?/.exec(path);
  if (!match) return undefined;
  return {
    section: Number(match[1]),
    field: match[2] !== undefined ? Number(match[2]) : undefined,
    part: match[3],
  };
}

function nextSectionKey(doc: FormLayoutDocument): string {
  const used = new Set(doc.sections.map((s) => s.key));
  let n = doc.sections.length + 1;
  while (used.has(`section_${n}`)) n += 1;
  return `section_${n}`;
}

export function LayoutEditor({
  value,
  onChange,
  palette,
  ruleKeys,
  invalidPath,
  invalidMessage,
  disabled = false,
}: LayoutEditorProps) {
  const { t, i18n } = useTranslation();
  const statusId = useId();
  const [status, setStatus] = useState('');
  const [targetSection, setTargetSection] = useState('');
  const sections = value.sections;
  const placed = placedNames(value);
  const byName = new Map(palette.map((p) => [p.name, p]));
  const free = palette.filter((p) => !placed.has(p.name));
  const missingRequired = free.filter((p) => p.required);
  const refused = refusedAt(invalidPath);
  const lang = i18n.language.toLowerCase().startsWith('de') ? 'de' : 'en';
  const sectionName = (s: FormLayoutSection) =>
    (lang === 'de' ? s.title.de || s.title.en : s.title.en || s.title.de) || s.key;
  const fieldName = (f: FormLayoutField) => byName.get(f.name)?.label || f.name;
  const target = sections.find((s) => s.key === targetSection) ?? sections[0];

  const setSections = (next: FormLayoutSection[]) => onChange({ ...value, sections: next });
  const updateSection = (key: string, change: Partial<FormLayoutSection>) =>
    setSections(sections.map((s) => (s.key === key ? { ...s, ...change } : s)));

  const addSection = () => {
    const key = nextSectionKey(value);
    setSections([...sections, { key, title: { de: '', en: '' }, visible_when: [], fields: [] }]);
    setTargetSection(key);
    setStatus(t('formBuilder.editor.sectionAdded', { key }));
  };

  const removeSection = (section: FormLayoutSection) => {
    setSections(sections.filter((s) => s.key !== section.key));
    setStatus(t('formBuilder.editor.sectionRemoved', { name: sectionName(section) }));
  };

  const place = (field: PaletteField) => {
    if (!target) return;
    updateSection(target.key, { fields: [...target.fields, { name: field.name, width: 12 }] });
    setStatus(
      t('formBuilder.editor.placed', {
        name: field.label || field.name,
        section: sectionName(target),
      }),
    );
  };

  const removeField = (section: FormLayoutSection, field: FormLayoutField) => {
    updateSection(section.key, { fields: section.fields.filter((f) => f.name !== field.name) });
    setStatus(t('formBuilder.editor.fieldRemoved', { name: fieldName(field) }));
  };

  const moveToSection = (from: FormLayoutSection, field: FormLayoutField, toKey: string) => {
    const to = sections.find((s) => s.key === toKey);
    if (!to || to.key === from.key) return;
    setSections(
      sections.map((s) => {
        if (s.key === from.key)
          return { ...s, fields: s.fields.filter((f) => f.name !== field.name) };
        if (s.key === to.key) return { ...s, fields: [...s.fields, field] };
        return s;
      }),
    );
    setStatus(
      t('formBuilder.editor.movedToSection', {
        name: fieldName(field),
        section: sectionName(to),
        position: to.fields.length + 1,
        total: to.fields.length + 1,
      }),
    );
  };

  const originBadge = (origin: FieldOrigin) => {
    switch (origin.kind) {
      case 'own':
        return <Badge variant="neutral">{t('formBuilder.origin.own')}</Badge>;
      case 'inherited':
        return (
          <Badge variant="neutral">
            {t('formBuilder.origin.inherited', { type: origin.from })}
          </Badge>
        );
      case 'set':
        return (
          <Badge variant="neutral">{t('formBuilder.origin.set', { name: origin.name })}</Badge>
        );
      default:
        return <Badge variant="info">{t('formBuilder.origin.global')}</Badge>;
    }
  };

  const fieldFacts = (p: PaletteField | undefined) =>
    p ? (
      <span className="flex flex-wrap items-center gap-1.5 text-xs text-fg-muted">
        <span>{t(`citypes.dataTypes.${p.dataType}`, p.dataType)}</span>
        {p.required ? <Badge variant="warning">{t('formBuilder.editor.required')}</Badge> : null}
        {originBadge(p.origin)}
      </span>
    ) : (
      <span className="text-xs text-fg-muted">{t('formBuilder.editor.unknownField')}</span>
    );

  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]">
      <section
        aria-labelledby={`${statusId}-palette`}
        className="space-y-3 rounded-xl border border-line bg-panel p-4"
      >
        <h2 id={`${statusId}-palette`} className="text-base font-semibold text-fg">
          {t('formBuilder.editor.palette')}
        </h2>
        <p className="text-sm text-fg-muted">{t('formBuilder.editor.paletteHint')}</p>
        {sections.length > 0 ? (
          <Select
            label={t('formBuilder.editor.targetSection')}
            value={target?.key ?? ''}
            disabled={disabled}
            onChange={(e) => setTargetSection(e.target.value)}
            options={sections.map((s) => ({ value: s.key, label: sectionName(s) }))}
          />
        ) : (
          <p className="text-sm text-fg-muted">{t('formBuilder.editor.noSectionForPalette')}</p>
        )}
        {free.length === 0 ? (
          <p className="text-sm text-fg-muted">{t('formBuilder.editor.paletteEmpty')}</p>
        ) : (
          <ul aria-label={t('formBuilder.editor.palette')} className="divide-y divide-line">
            {free.map((p) => (
              <li key={p.name} data-palette-field={p.name} className="flex items-start gap-2 py-2">
                <div className="min-w-0 flex-1 space-y-1">
                  <p className="text-sm font-medium text-fg [overflow-wrap:anywhere]">{p.label}</p>
                  <p className="font-mono text-xs text-fg-muted">{p.name}</p>
                  {fieldFacts(p)}
                </div>
                <IconButton
                  size="sm"
                  label={t('formBuilder.editor.place', { name: p.label || p.name })}
                  icon={<Plus />}
                  disabled={disabled || !target}
                  onClick={() => place(p)}
                />
              </li>
            ))}
          </ul>
        )}
        {missingRequired.length > 0 ? (
          <p
            role="note"
            className="rounded-lg border border-line bg-accent-soft p-3 text-sm text-fg"
          >
            {t('formBuilder.editor.requiredHint', {
              names: missingRequired.map((p) => p.label || p.name).join(', '),
            })}
          </p>
        ) : null}
      </section>

      <section
        aria-labelledby={`${statusId}-sections`}
        className="space-y-3 rounded-xl border border-line bg-panel p-4"
      >
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 id={`${statusId}-sections`} className="text-base font-semibold text-fg">
            {t('formBuilder.editor.sections')}
          </h2>
          <Button
            variant="secondary"
            size="sm"
            icon={<Plus />}
            disabled={disabled}
            onClick={addSection}
          >
            {t('formBuilder.editor.addSection')}
          </Button>
        </div>
        {sections.length === 0 ? (
          <p className="text-sm text-fg-muted">{t('formBuilder.editor.noSections')}</p>
        ) : (
          <SortableList
            items={sections}
            getId={(s) => s.key}
            getName={sectionName}
            label={t('formBuilder.editor.sections')}
            disabled={disabled}
            onReorder={(next) => setSections(next)}
            className="space-y-2"
            renderItem={(section) => {
              const index = sections.indexOf(section);
              const refusedHere = refused?.section === index ? refused : undefined;
              const titleRefused =
                refusedHere?.field === undefined && refusedHere?.part === 'title';
              const sectionRefused =
                refusedHere !== undefined && refusedHere.field === undefined && !titleRefused;
              const visible = section.visible_when ?? [];
              const ruleOptions = [...new Set([...ruleKeys, ...visible])];
              const name = sectionName(section);
              return (
                <div
                  data-section-key={section.key}
                  data-invalid={sectionRefused || titleRefused ? 'true' : undefined}
                  className={cn(
                    'space-y-3 rounded-lg border border-line p-3',
                    (sectionRefused || titleRefused) && 'border-red-500',
                  )}
                >
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <p className="text-sm font-semibold text-fg">
                      {name} <span className="font-mono text-xs text-fg-muted">{section.key}</span>
                    </p>
                    <IconButton
                      size="sm"
                      label={t('formBuilder.editor.removeSection', { name })}
                      icon={<Trash2 />}
                      className="hover:text-danger"
                      disabled={disabled}
                      onClick={() => removeSection(section)}
                    />
                  </div>
                  <div className="grid gap-2 sm:grid-cols-2">
                    <Input
                      label={t('formBuilder.editor.titleDe', { key: section.key })}
                      value={section.title.de}
                      disabled={disabled}
                      required
                      maxLength={200}
                      error={
                        section.title.de.trim() === ''
                          ? t('formBuilder.editor.titleMissing')
                          : titleRefused
                            ? invalidMessage
                            : undefined
                      }
                      onChange={(e) =>
                        updateSection(section.key, {
                          title: { ...section.title, de: e.target.value },
                        })
                      }
                    />
                    <Input
                      label={t('formBuilder.editor.titleEn', { key: section.key })}
                      value={section.title.en}
                      disabled={disabled}
                      required
                      maxLength={200}
                      error={
                        section.title.en.trim() === ''
                          ? t('formBuilder.editor.titleMissing')
                          : undefined
                      }
                      onChange={(e) =>
                        updateSection(section.key, {
                          title: { ...section.title, en: e.target.value },
                        })
                      }
                    />
                  </div>
                  {sectionRefused ? (
                    <p role="alert" className="text-sm text-danger">
                      {invalidMessage ?? t('formBuilder.editor.refused')}
                    </p>
                  ) : null}
                  <fieldset className="space-y-1">
                    <legend className="text-sm font-medium text-fg">
                      {t('formBuilder.editor.visibleWhen', { name })}
                    </legend>
                    {ruleOptions.length === 0 ? (
                      <p className="text-sm text-fg-muted">{t('formBuilder.editor.noRules')}</p>
                    ) : (
                      <div className="flex flex-wrap gap-x-4 gap-y-1">
                        {ruleOptions.map((ruleKey) => (
                          <label
                            key={ruleKey}
                            className="flex items-center gap-1.5 text-sm text-fg"
                          >
                            <input
                              type="checkbox"
                              checked={visible.includes(ruleKey)}
                              disabled={disabled}
                              onChange={(e) =>
                                updateSection(section.key, {
                                  visible_when: e.target.checked
                                    ? [...visible, ruleKey]
                                    : visible.filter((k) => k !== ruleKey),
                                })
                              }
                            />
                            <span className="font-mono">{ruleKey}</span>
                            {!ruleKeys.includes(ruleKey) ? (
                              <span className="text-xs text-danger">
                                {t('formBuilder.editor.ruleUnknown')}
                              </span>
                            ) : null}
                          </label>
                        ))}
                      </div>
                    )}
                  </fieldset>
                  {section.fields.length === 0 ? (
                    <p className="text-sm text-fg-muted">{t('formBuilder.editor.noFields')}</p>
                  ) : (
                    <SortableList
                      items={section.fields}
                      getId={(f) => f.name}
                      getName={fieldName}
                      label={t('formBuilder.editor.fieldsOf', { name })}
                      disabled={disabled}
                      onReorder={(next) => updateSection(section.key, { fields: next })}
                      className="divide-y divide-line"
                      renderItem={(field) => {
                        const fieldIndex = section.fields.indexOf(field);
                        const fieldRefused =
                          refusedHere?.field !== undefined && refusedHere.field === fieldIndex;
                        const label = fieldName(field);
                        return (
                          <div
                            data-field-name={field.name}
                            data-invalid={fieldRefused ? 'true' : undefined}
                            className={cn(
                              'flex flex-wrap items-end gap-3',
                              fieldRefused && 'rounded-lg ring-2 ring-red-500',
                            )}
                          >
                            <div className="min-w-0 flex-1 space-y-1">
                              <p className="text-sm font-medium text-fg [overflow-wrap:anywhere]">
                                {label}{' '}
                                <span className="font-mono text-xs text-fg-muted">
                                  {field.name}
                                </span>
                              </p>
                              {fieldFacts(byName.get(field.name))}
                              {fieldRefused ? (
                                <p role="alert" className="text-sm text-danger">
                                  {invalidMessage ?? t('formBuilder.editor.refused')}
                                </p>
                              ) : null}
                            </div>
                            <div className="w-24">
                              <Select
                                label={t('formBuilder.editor.width', { name: label })}
                                value={String(field.width)}
                                disabled={disabled}
                                onChange={(e) =>
                                  updateSection(section.key, {
                                    fields: section.fields.map((f) =>
                                      f.name === field.name
                                        ? { ...f, width: Number(e.target.value) }
                                        : f,
                                    ),
                                  })
                                }
                                options={LAYOUT_WIDTHS.map((w) => ({
                                  value: String(w),
                                  label: String(w),
                                }))}
                              />
                            </div>
                            {sections.length > 1 ? (
                              <div className="w-44">
                                <Select
                                  label={t('formBuilder.editor.moveToSection', { name: label })}
                                  value=""
                                  disabled={disabled}
                                  onChange={(e) => moveToSection(section, field, e.target.value)}
                                  options={[
                                    { value: '', label: t('formBuilder.editor.chooseSection') },
                                    ...sections
                                      .filter((s) => s.key !== section.key)
                                      .map((s) => ({ value: s.key, label: sectionName(s) })),
                                  ]}
                                />
                              </div>
                            ) : null}
                            <IconButton
                              size="sm"
                              label={t('formBuilder.editor.removeField', { name: label })}
                              icon={<X />}
                              disabled={disabled}
                              onClick={() => removeField(section, field)}
                            />
                          </div>
                        );
                      }}
                    />
                  )}
                </div>
              );
            }}
          />
        )}
      </section>
      <div role="status" aria-live="polite" className="sr-only">
        {status}
      </div>
    </div>
  );
}
