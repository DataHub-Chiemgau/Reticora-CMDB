import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { localizedText } from '../../api/cmdb';
import { Select } from '../ui/Select';
import type { FieldSpec } from '../../rules/builderModes';

/**
 * fieldSpecLabel names a field in the UI language (GLO-10): a column by the
 * catalog label of savedviews.fields, an attribute by the display_name of
 * its definition (its key without one), a field of the linked asset with
 * the prefix of the asset.
 */
export function fieldSpecLabel(t: TFunction, language: string, spec: FieldSpec): string {
  const attributeName = () =>
    (spec.definition && localizedText(spec.definition.display_name, language)) ||
    spec.field.replace(/^(asset\.)?attributes\./, '');
  switch (spec.source) {
    case 'tags':
      return t('rules.fieldLabels.tags');
    case 'attribute':
      return t('rules.fieldLabels.attribute', { name: attributeName() });
    case 'asset_attribute':
      return t('rules.fieldLabels.assetAttribute', { name: attributeName() });
    case 'asset':
      return t('rules.fieldLabels.asset', {
        name: t(`savedviews.fields.${spec.column ?? ''}`, spec.column ?? spec.field),
      });
    default:
      return t(`savedviews.fields.${spec.column ?? spec.field}`, spec.field);
  }
}

export interface AttributePickerProps {
  label: string;
  /** fields are the fields offered (fieldCatalog of builderModes, without hidden ones). */
  fields: readonly FieldSpec[];
  /** value is the chosen field path ('' for none). */
  value: string;
  onChange: (field: string, spec: FieldSpec | undefined) => void;
  /** current is the spec of a value that is no longer offered (kept visible, marked). */
  current?: FieldSpec;
  error?: string;
  id?: string;
  disabled?: boolean;
}

/**
 * AttributePicker chooses the field of a comparison from a given list: the
 * columns of the whitelist of the entity kind, tags, the fields of the
 * linked asset and the searchable attribute definitions (WP-315). It needs
 * no RuleBuilder; the mapping of WP-322 uses it with its own list.
 */
export function AttributePicker({
  label,
  fields,
  value,
  onChange,
  current,
  error,
  id,
  disabled,
}: AttributePickerProps) {
  const { t, i18n } = useTranslation();
  const offered = fields.some((f) => f.field === value) || !current ? fields : [current, ...fields];
  const options = [
    { value: '', label: t('rules.builder.chooseField') },
    ...offered.map((f) => ({
      value: f.field,
      label: fieldSpecLabel(t, i18n.language, f),
    })),
  ];
  return (
    <Select
      id={id}
      label={label}
      value={value}
      error={error}
      disabled={disabled}
      onChange={(event) =>
        onChange(
          event.target.value,
          offered.find((f) => f.field === event.target.value),
        )
      }
      options={options}
    />
  );
}
