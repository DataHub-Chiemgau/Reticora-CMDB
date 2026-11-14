import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { X } from 'lucide-react';
import type { components } from '../../api/generated/schema';
import type { ImportMapping, ImportTransform } from '../../api/client';
import { AttributePicker, fieldSpecLabel } from '../rules/AttributePicker';
import { ValueEditor } from '../rules/ValueEditor';
import { IconButton } from '../ui/Button';
import { Input } from '../ui/Input';
import { Select } from '../ui/Select';
import {
  attributeEditor,
  attributeKind,
  type FieldSpec,
  type ValueEditorKind,
} from '../../rules/builderModes';

type AttributeDefinition = components['schemas']['AttributeDefinition'];

/** The entities an import profile writes (ImportProfile.entity, WP-774, WP-776). */
export const IMPORT_ENTITIES = [
  'ci',
  'asset',
  'contract',
  'consumable',
  'location',
  'relationship',
  // IPAM with the column names of phpIPAM (IPM-09, WP-920).
  'subnet',
  'ip_address',
] as const satisfies readonly components['schemas']['ImportProfile']['entity'][];
export type ImportEntity = (typeof IMPORT_ENTITIES)[number];

/** The modes of a profile and a run (create_missing, update_existing, both). */
export const IMPORT_MODES = [
  'create_missing',
  'update_existing',
  'both',
] as const satisfies readonly components['schemas']['ImportProfile']['mode'][];
export type ImportMode = (typeof IMPORT_MODES)[number];

/** The file formats of the assistant; json is the API import (WP-303) without a file. */
export const IMPORT_FILE_FORMATS = ['csv', 'xlsx'] as const;
export type ImportFileFormat = (typeof IMPORT_FILE_FORMATS)[number];

/** The transformations of a column, applied in order (ImportTransform.op, WP-301). */
export const IMPORT_TRANSFORMS = [
  'trim',
  'date_format',
  'enum_map',
  'split',
  'constant',
] as const satisfies readonly ImportTransform['op'][];
export type ImportTransformOp = (typeof IMPORT_TRANSFORMS)[number];

/**
 * IMPORT_TARGETS are the core fields each entity writes (the targets the
 * import writers of backend/internal/importer read); attributes.<key> (CI,
 * asset) and relations.<type> (CI) come on top. Their labels are the field
 * labels savedviews.fields of the catalog.
 */
export const IMPORT_TARGETS: Readonly<Record<ImportEntity, readonly string[]>> = {
  ci: [
    'name',
    'ci_type',
    'status',
    'manufacturer',
    'model',
    'serial_number',
    'hardware_uuid',
    'management_ip',
    'primary_mac',
    'hostname',
    'fqdn',
    'os_name',
    'os_version',
    'firmware_version',
    'sys_object_id',
    'client_id',
    'vrf_id',
    'location',
    'location_kind',
    'location_id',
    'observed_at',
  ],
  asset: [
    'inventory_no',
    'name',
    'category',
    'status',
    'manufacturer',
    'model',
    'serial_number',
    'purchase_date',
    'purchase_price',
    'currency',
    'warranty_until',
    'cost_center',
    'supplier_id',
    'contract_id',
    'owner_user_id',
    'owner_team_id',
    'invoice_number',
    'rfid_tag',
    'barcode',
    'location',
    'primary_ci_id',
    'client_id',
    'notes',
  ],
  contract: [
    'number',
    'client_id',
    'supplier_id',
    'kind',
    'status',
    'start_date',
    'end_date',
    'notice_period_days',
    'value',
    'currency',
    'notes',
  ],
  consumable: ['sku', 'name', 'barcode', 'supplier_id', 'category', 'unit', 'client_id', 'notes'],
  location: ['kind', 'name', 'parent', 'path', 'client_id'],
  // The phpIPAM columns of backend/internal/importer/ipam.go (WP-920).
  subnet: [
    'section',
    'subnet',
    'mask',
    'cidr',
    'description',
    'vlan',
    'vrf',
    'gateway',
    'customer',
    'nameserver',
    'is_pool',
  ],
  ip_address: [
    'ip_addr',
    'hostname',
    'description',
    'mac',
    'state',
    'is_gateway',
    'vrf',
    'subnet',
    'type',
  ],
  relationship: [
    'rel_type',
    'source_ci_id',
    'target_ci_id',
    'source_serial_number',
    'target_serial_number',
    'source_hardware_uuid',
    'target_hardware_uuid',
    'source_primary_mac',
    'target_primary_mac',
    'source_management_ip',
    'target_management_ip',
    'source_interface_id',
    'target_interface_id',
    'confidence',
    'notes',
  ],
};

/** The editor of the default of a core field. */
function coreEditor(target: string): ValueEditorKind {
  switch (target) {
    case 'ci_type':
      return 'ci_type';
    case 'client_id':
      return 'client';
    case 'owner_user_id':
      return 'user';
    case 'owner_team_id':
      return 'team';
    case 'location_id':
      return 'location';
    case 'purchase_date':
    case 'warranty_until':
    case 'start_date':
    case 'end_date':
      return 'date';
    case 'purchase_price':
    case 'notice_period_days':
    case 'value':
    case 'confidence':
      return 'number';
  }
  return 'text';
}

export interface ImportTargetOptions {
  /** The attribute definitions of the chosen CI type (CI only). */
  definitions?: readonly AttributeDefinition[];
  /** The relationship type keys a CI row may name (relations.<type>). */
  relationTypes?: readonly string[];
}

/**
 * importFieldSpecs lists the targets of an entity for the AttributePicker:
 * the core fields, for CIs the attributes of the chosen type and the
 * relations.
 */
export function importFieldSpecs(
  entity: ImportEntity,
  { definitions = [], relationTypes = [] }: ImportTargetOptions = {},
): FieldSpec[] {
  const core = IMPORT_TARGETS[entity].map((target): FieldSpec => ({
    field: target,
    source: 'column',
    kind: coreEditor(target) === 'number' ? 'number' : 'text',
    editor: coreEditor(target),
    column: target,
  }));
  if (entity !== 'ci') return core;
  const attributes = definitions
    .filter((d) => /^[A-Za-z][A-Za-z0-9_-]{0,99}$/.test(d.key))
    .map((d): FieldSpec => ({
      field: `attributes.${d.key}`,
      source: 'attribute',
      kind: attributeKind(d.data_type),
      editor: attributeEditor(d),
      definition: d,
    }));
  const relations = relationTypes
    .filter((key) => /^[A-Za-z][A-Za-z0-9_-]{0,99}$/.test(key))
    .map((key): FieldSpec => ({
      field: `relations.${key}`,
      source: 'column',
      kind: 'text',
      editor: 'text',
    }));
  return [...core, ...attributes, ...relations];
}

/** A new transformation with the parameter it needs. */
function newTransform(op: ImportTransformOp): ImportTransform {
  switch (op) {
    case 'date_format':
      return { op, layout: '02.01.2006' };
    case 'enum_map':
      return { op, values: {} };
    case 'split':
      return { op, separator: ';' };
    case 'constant':
      return { op, value: '' };
  }
  return { op };
}

/** formatValues writes an enum map as "source=target" pairs, one per comma. */
function formatValues(values: Record<string, string> | undefined): string {
  return Object.entries(values ?? {})
    .map(([from, to]) => `${from}=${to}`)
    .join(', ');
}

/** parseValues reads "source=target" pairs; a pair without "=" is left out. */
export function parseValues(text: string): Record<string, string> {
  const values: Record<string, string> = {};
  for (const pair of text.split(',')) {
    const at = pair.indexOf('=');
    if (at <= 0) continue;
    const from = pair.slice(0, at).trim();
    if (from) values[from] = pair.slice(at + 1).trim();
  }
  return values;
}

/** EnumMapInput keeps the text being typed and sends the parsed pairs. */
function EnumMapInput({
  label,
  values,
  onChange,
  disabled,
}: {
  label: string;
  values: Record<string, string> | undefined;
  onChange: (values: Record<string, string>) => void;
  disabled?: boolean;
}) {
  const { t } = useTranslation();
  const [text, setText] = useState(() => formatValues(values));
  return (
    <Input
      label={label}
      value={text}
      disabled={disabled}
      placeholder={t('import.mapping.enumMapPlaceholder')}
      onChange={(event) => {
        setText(event.target.value);
        onChange(parseValues(event.target.value));
      }}
    />
  );
}

interface TransformProps {
  column: string;
  index: number;
  transform: ImportTransform;
  onChange: (transform: ImportTransform) => void;
  onRemove: () => void;
  disabled?: boolean;
}

/** TransformRow edits one step of the transformation list of a column. */
function TransformRow({ column, index, transform, onChange, onRemove, disabled }: TransformProps) {
  const { t } = useTranslation();
  const name = t(`import.transforms.${transform.op}`, transform.op);
  const param = (() => {
    switch (transform.op) {
      case 'date_format':
        return (
          <Input
            label={t('import.mapping.layout', { name })}
            value={transform.layout ?? ''}
            disabled={disabled}
            onChange={(e) => onChange({ ...transform, layout: e.target.value })}
          />
        );
      case 'enum_map':
        return (
          <EnumMapInput
            label={t('import.mapping.values', { name })}
            values={transform.values}
            disabled={disabled}
            onChange={(values) => onChange({ ...transform, values })}
          />
        );
      case 'split':
        return (
          <Input
            label={t('import.mapping.separator', { name })}
            value={transform.separator ?? ''}
            disabled={disabled}
            onChange={(e) => onChange({ ...transform, separator: e.target.value })}
          />
        );
      case 'constant':
        return (
          <Input
            label={t('import.mapping.constant', { name })}
            value={transform.value ?? ''}
            disabled={disabled}
            onChange={(e) => onChange({ ...transform, value: e.target.value })}
          />
        );
    }
    return null;
  })();
  return (
    <li className="flex items-end gap-2 rounded-lg border border-line bg-canvas px-3 py-2">
      <div className="min-w-0 flex-1 space-y-1">
        <p className="text-xs font-medium text-fg">
          {t('import.mapping.step', { index: index + 1, name })}
        </p>
        {param}
      </div>
      <IconButton
        size="sm"
        icon={<X />}
        disabled={disabled}
        label={t('import.mapping.removeTransform', { name, column })}
        onClick={onRemove}
      />
    </li>
  );
}

export interface MappingEditorProps {
  /** columns is the header of the file (POST /imports/preview). */
  columns: readonly string[];
  /** sampleRows are the preview rows, shown as examples of a column. */
  sampleRows?: readonly (readonly string[])[];
  /** fields are the targets offered (importFieldSpecs). */
  fields: readonly FieldSpec[];
  value: ImportMapping;
  onChange: (mapping: ImportMapping) => void;
  disabled?: boolean;
}

/** emptyMapping is a mapping without columns (the profile needs at least one). */
export function emptyMapping(): ImportMapping {
  return { columns: {}, defaults: {} };
}

/**
 * MappingEditor maps each column of the file onto a target of the entity
 * (UI-11, WP-322): a core field, an attribute of the chosen CI type or a
 * relation, chosen with the AttributePicker of WP-315; per column an
 * ordered list of the transformations of WP-301 (trim, date_format,
 * enum_map, split, constant) and a default for the target, edited with the
 * ValueEditor of WP-315 and sent as text (mapping.defaults). A column
 * without a target is not imported.
 */
export function MappingEditor({
  columns,
  sampleRows = [],
  fields,
  value,
  onChange,
  disabled,
}: MappingEditorProps) {
  const { t, i18n } = useTranslation();
  const defaults = value.defaults ?? {};

  const specOf = (target: string): FieldSpec | undefined =>
    fields.find((f) => f.field === target) ??
    (target
      ? { field: target, source: 'column', kind: 'text', editor: 'text', column: target }
      : undefined);

  const setColumn = (column: string, target: string) => {
    const next = { ...value.columns };
    const previous = next[column]?.target;
    const nextDefaults = { ...defaults };
    if (target === '') {
      delete next[column];
    } else {
      next[column] = { target, transforms: next[column]?.transforms ?? [] };
    }
    if (previous && previous !== target && nextDefaults[previous] !== undefined) {
      if (target) nextDefaults[target] = nextDefaults[previous];
      delete nextDefaults[previous];
    }
    onChange({ columns: next, defaults: nextDefaults });
  };

  const setTransforms = (column: string, transforms: ImportTransform[]) => {
    const current = value.columns[column];
    if (!current) return;
    onChange({ ...value, columns: { ...value.columns, [column]: { ...current, transforms } } });
  };

  const setDefault = (target: string, v: unknown) => {
    const next = { ...defaults };
    if (v === undefined || v === '' || v === null) delete next[target];
    else next[target] = String(v);
    onChange({ ...value, defaults: next });
  };

  return (
    <ol className="space-y-3" aria-label={t('import.mapping.title')}>
      {columns.map((column, columnIndex) => {
        const mapped = value.columns[column];
        const target = mapped?.target ?? '';
        const spec = specOf(target);
        const transforms = mapped?.transforms ?? [];
        const examples = sampleRows
          .map((row) => row[columnIndex])
          .filter((cell): cell is string => typeof cell === 'string' && cell !== '')
          .slice(0, 3);
        const targetName = spec ? fieldSpecLabel(t, i18n.language, spec) : '';
        return (
          <li
            key={column}
            className="space-y-3 rounded-lg border border-line bg-panel p-4"
            data-testid={`mapping-${column}`}
          >
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <p className="font-mono text-sm font-medium text-fg [overflow-wrap:anywhere]">
                {column}
              </p>
              {examples.length > 0 ? (
                <p className="text-xs text-fg-muted [overflow-wrap:anywhere]">
                  {t('import.mapping.examples', { values: examples.join(', ') })}
                </p>
              ) : null}
            </div>
            <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
              <AttributePicker
                label={t('import.mapping.target', { column })}
                fields={fields}
                value={target}
                current={target && !fields.some((f) => f.field === target) ? spec : undefined}
                disabled={disabled}
                onChange={(field) => setColumn(column, field)}
              />
              {mapped && spec ? (
                <ValueEditor
                  label={t('import.mapping.default', { name: targetName })}
                  spec={spec}
                  op="eq"
                  value={defaults[target]}
                  onChange={(v) => setDefault(target, v)}
                />
              ) : null}
            </div>
            {mapped ? (
              <div className="space-y-2">
                {transforms.length > 0 ? (
                  <ol
                    className="space-y-2"
                    aria-label={t('import.mapping.transformsOf', { column })}
                  >
                    {transforms.map((transform, index) => (
                      <TransformRow
                        // The list is edited in place; the position is the identity.
                        key={`${transform.op}-${index}`}
                        column={column}
                        index={index}
                        transform={transform}
                        disabled={disabled}
                        onChange={(next) =>
                          setTransforms(
                            column,
                            transforms.map((x, i) => (i === index ? next : x)),
                          )
                        }
                        onRemove={() =>
                          setTransforms(
                            column,
                            transforms.filter((_, i) => i !== index),
                          )
                        }
                      />
                    ))}
                  </ol>
                ) : null}
                <div className="sm:max-w-xs">
                  <Select
                    label={t('import.mapping.addTransform', { column })}
                    value=""
                    disabled={disabled || transforms.length >= 20}
                    onChange={(event) => {
                      const op = event.target.value as ImportTransformOp;
                      if (op) setTransforms(column, [...transforms, newTransform(op)]);
                    }}
                    options={[
                      { value: '', label: t('import.mapping.chooseTransform') },
                      ...IMPORT_TRANSFORMS.map((op) => ({
                        value: op,
                        label: t(`import.transforms.${op}`),
                      })),
                    ]}
                  />
                </div>
              </div>
            ) : null}
          </li>
        );
      })}
    </ol>
  );
}
