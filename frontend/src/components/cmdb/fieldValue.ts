/**
 * The value of a CI field shown where the field name is data (WP-523,
 * GLO-10/UI-18): the provenance rows (REC-10), the field changes of the
 * history (CI-06, AUD-03) and the value an override covers. A field whose
 * values are keys of the i18n catalog (the CI status) shows the catalog
 * label, never the raw key; every other field keeps the given formatting.
 *
 * The source scan in src/i18n/rawEnum.test.ts requires such a value to go
 * through fieldValueText before it reaches the JSX.
 */
import type { TFunction } from 'i18next';

/** The catalog group of each CI field whose values are catalog keys. */
const CATALOG_FIELDS: Readonly<Record<string, string>> = {
  status: 'ci.statuses',
};

export function fieldValueText(
  t: TFunction,
  field: string,
  value: unknown,
  format: (value: unknown) => string,
): string {
  const group = CATALOG_FIELDS[field];
  if (group && typeof value === 'string' && value !== '') {
    return t(`${group}.${value}`, value);
  }
  return format(value);
}
