import { describe, expect, it } from 'vitest';
import Ajv2020 from 'ajv/dist/2020.js';
import { filterOps, parseFilter, type FilterEntityKind } from './filter';

// Loaded at test time: the frontend image is built from frontend/ alone, so
// the type check must not depend on the files outside it.
const conformance = Object.values(
  import.meta.glob<{ filter_cases: unknown[]; invalid_filters: unknown[] }>(
    '../../../api/rules.conformance.json',
    { eager: true, import: 'default' },
  ),
)[0]!;
const schema = Object.values(
  import.meta.glob<Record<string, unknown>>('../../../api/rules.schema.json', {
    eager: true,
    import: 'default',
  }),
)[0]!;

interface FilterCase {
  name: string;
  entity_kind: FilterEntityKind;
  filter: unknown;
}

interface InvalidFilter extends FilterCase {
  path: string;
}

// filter_cases and invalid_filters (WP-789), the same cases as
// TestFilterConformance in backend/internal/savedview.
describe('filter AST conformance', () => {
  const cases = conformance.filter_cases as FilterCase[];
  const invalid = conformance.invalid_filters as InvalidFilter[];
  const ajv = new Ajv2020({ strict: false });
  ajv.addSchema(schema);
  const validFilter = ajv.getSchema(`${schema.$id as string}#/$defs/filter`)!;

  it('has cases', () => {
    expect(cases.length).toBeGreaterThan(0);
    expect(invalid.length).toBeGreaterThan(0);
  });

  it.each(cases.map((c) => [c.name, c] as const))('%s', (_name, c) => {
    expect(parseFilter(c.filter, c.entity_kind)).toEqual(c.filter);
    // Every valid case is valid against $defs/filter of the schema.
    expect(validFilter(c.filter), JSON.stringify(validFilter.errors)).toBe(true);
  });

  it.each(invalid.map((c) => [c.name, c] as const))('%s', (_name, c) => {
    expect(() => parseFilter(c.filter, c.entity_kind)).toThrow(
      new RegExp(`^${c.path.replace(/[.[\]]/g, '\\$&')}: `),
    );
  });

  it('has the operators of $defs/filter_compare', () => {
    const defs = schema.$defs as Record<string, { properties: { op: { enum: string[] } } }>;
    expect(defs.filter_compare!.properties.op.enum).toEqual([...filterOps]);
  });
});

describe('parseFilter', () => {
  it('defaults to entity kind ci', () => {
    const related = {
      related: { direction: 'in', where: { field: 'name', op: 'eq', value: 'x' } },
    };
    expect(parseFilter(related)).toEqual(related);
    expect(() => parseFilter(related, 'asset')).toThrow(/^where\.related: /);
  });

  it('refuses unknown members and wrong shapes', () => {
    expect(() => parseFilter({ field: 'name', op: 'eq', value: 'x', extra: 1 })).toThrow(
      /^where\.extra: /,
    );
    expect(() =>
      parseFilter({
        related: { direction: 'out', where: { field: 'name', op: 'exists' }, depth: 2 },
      }),
    ).toThrow(/^where\.related\.depth: /);
    expect(() => parseFilter({ and: { field: 'name', op: 'exists' } })).toThrow(/^where\.and: /);
    expect(() => parseFilter([])).toThrow(/^where: /);
    expect(() =>
      parseFilter({
        related: { direction: 'out', max_depth: 1.5, where: { field: 'name', op: 'exists' } },
      }),
    ).toThrow(/^where\.related\.max_depth: /);
    expect(() =>
      parseFilter({
        related: { direction: 'out', rel_types: 'runs_on', where: { field: 'name', op: 'exists' } },
      }),
    ).toThrow(/^where\.related\.rel_types: /);
  });
});
