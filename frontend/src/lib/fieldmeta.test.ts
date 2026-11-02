import { describe, it, expect } from 'vitest';
import {
  evaluateField,
  validateValue,
  matchPredicate,
  ruleOf,
  type FieldDefinition,
} from './fieldmeta';
import deDE from '../i18n/de-DE.json';
import enUS from '../i18n/en-US.json';

describe('evaluateField', () => {
  const physicalServer: FieldDefinition = {
    name: 'rack',
    data_type: 'text',
    conditional: { visible_when: [{ field: 'server_kind', op: 'eq', value: 'physical' }] },
  };

  it('shows the field when the predicate matches', () => {
    expect(evaluateField(physicalServer, { server_kind: 'physical' }).visible).toBe(true);
  });

  it('hides and de-requires the field when the predicate does not match', () => {
    const state = evaluateField({ ...physicalServer, required: true }, { server_kind: 'virtual' });
    expect(state.visible).toBe(false);
    expect(state.required).toBe(false);
  });

  it('applies conditional required and read-only', () => {
    const def: FieldDefinition = {
      name: 'serial',
      data_type: 'text',
      conditional: {
        required_when: [{ field: 'kind', op: 'eq', value: 'physical' }],
        read_only_when: [{ field: 'locked', op: 'eq', value: true }],
      },
    };
    expect(evaluateField(def, { kind: 'physical' }).required).toBe(true);
    expect(evaluateField(def, { kind: 'virtual' }).required).toBe(false);
    expect(evaluateField(def, { locked: true }).readOnly).toBe(true);
  });

  it('restricts allowed values conditionally', () => {
    const def: FieldDefinition = {
      name: 'size',
      data_type: 'enum',
      enum_values: ['s', 'm', 'l'],
      conditional: {
        allowed_values_when: {
          when: [{ field: 'kind', op: 'eq', value: 'virtual' }],
          values: ['s', 'm'],
        },
      },
    };
    expect(evaluateField(def, { kind: 'virtual' }).allowedValues).toEqual(['s', 'm']);
    expect(evaluateField(def, { kind: 'physical' }).allowedValues).toEqual(['s', 'm', 'l']);
  });
});

describe('matchPredicate', () => {
  const values: Record<string, unknown> = {
    n: 5,
    s: 'hello world',
    list: ['a', 'b'],
    empty: '',
    missing: null,
  };
  it.each([
    [{ field: 'n', op: 'gt', value: 4 } as const, true],
    [{ field: 'n', op: 'gte', value: 5 } as const, true],
    [{ field: 'n', op: 'lt', value: 6 } as const, true],
    [{ field: 'n', op: 'lte', value: 5 } as const, true],
    [{ field: 'n', op: 'eq', value: 5 } as const, true],
    [{ field: 'n', op: 'ne', value: 5 } as const, false],
    [{ field: 'n', op: 'in', value: [4, 5] } as const, true],
    [{ field: 's', op: 'contains', value: 'world' } as const, true],
    [{ field: 'list', op: 'contains', value: 'a' } as const, true],
    [{ field: 'empty', op: 'empty' } as const, true],
    [{ field: 'missing', op: 'empty' } as const, true],
    [{ field: 's', op: 'not_empty' } as const, true],
  ])('evaluates %j as %s', (pred, want) => {
    expect(matchPredicate(pred, values[pred.field])).toBe(want);
  });
});

// validateValue returns a catalog key with its parameters, which the form
// translates (GLO-10, F-343); every key exists in both catalogs.
describe('validateValue', () => {
  const field = (key: string) => `form.validation.field.${key}`;

  it('requires values only when required', () => {
    const def: FieldDefinition = {
      name: 'x',
      label: 'Hostname',
      data_type: 'text',
      required: true,
    };
    expect(validateValue(def, '')).toEqual({
      key: field('required'),
      params: { label: 'Hostname' },
    });
    expect(validateValue({ ...def, required: false }, '')).toBeNull();
    // Without a label the field name is named.
    expect(validateValue({ ...def, label: undefined }, '')).toEqual({
      key: field('required'),
      params: { label: 'x' },
    });
  });

  it('validates ip/mac/email/url', () => {
    expect(validateValue({ name: 'ip', data_type: 'ip' }, '10.0.0.1')).toBeNull();
    expect(validateValue({ name: 'ip', data_type: 'ip' }, 'nope')?.key).toBe(field('ip'));
    expect(validateValue({ name: 'm', data_type: 'mac' }, 'aa:bb:cc:dd:ee:ff')).toBeNull();
    expect(validateValue({ name: 'm', data_type: 'mac' }, 'xyz')?.key).toBe(field('mac'));
    expect(validateValue({ name: 'e', data_type: 'email' }, 'a@b.io')).toBeNull();
    expect(validateValue({ name: 'e', data_type: 'email' }, 'a@b')?.key).toBe(field('email'));
    expect(validateValue({ name: 'u', data_type: 'url' }, 'https://x.io/y')).toBeNull();
    expect(validateValue({ name: 'u', data_type: 'url' }, 'x.io')?.key).toBe(field('url'));
  });

  it('validates enum options', () => {
    const def: FieldDefinition = {
      name: 'os',
      data_type: 'enum',
      enum_values: ['linux', 'windows'],
    };
    expect(validateValue(def, 'linux')).toBeNull();
    expect(validateValue(def, 'plan9')).toEqual({
      key: field('notAllowed'),
      params: { label: 'os', value: 'plan9' },
    });
  });

  it('validates numeric bounds', () => {
    const def: FieldDefinition = {
      name: 'cores',
      data_type: 'integer',
      validation: { min: 1, max: 64 },
    };
    expect(validateValue(def, 8)).toBeNull();
    expect(validateValue(def, 128)).toEqual({
      key: field('max'),
      params: { label: 'cores', limit: 64 },
    });
    expect(validateValue(def, 0)).toEqual({
      key: field('min'),
      params: { label: 'cores', limit: 1 },
    });
  });

  it('names length bounds and patterns', () => {
    const def: FieldDefinition = {
      name: 'tag',
      data_type: 'text',
      validation: { min_length: 2, max_length: 4, pattern: '^[a-z]+$' },
    };
    expect(validateValue(def, 'abc')).toBeNull();
    expect(validateValue(def, 'ABC')?.key).toBe(field('pattern'));
    expect(validateValue(def, 'a')).toEqual({
      key: field('minLength'),
      params: { label: 'tag', limit: 2 },
    });
    expect(validateValue(def, 'abcde')).toEqual({
      key: field('maxLength'),
      params: { label: 'tag', limit: 4 },
    });
  });

  it('uses only keys of both catalogs', () => {
    const cases: Array<[FieldDefinition, unknown]> = [
      [{ name: 'a', data_type: 'text', required: true }, ''],
      [{ name: 'a', data_type: 'integer' }, 1.5],
      [{ name: 'a', data_type: 'number' }, 'x'],
      [{ name: 'a', data_type: 'boolean' }, 'x'],
      [{ name: 'a', data_type: 'date' }, '2026-02-30'],
      [{ name: 'a', data_type: 'datetime' }, 'never'],
      [{ name: 'a', data_type: 'ip' }, 'x'],
      [{ name: 'a', data_type: 'mac' }, 'x'],
      [{ name: 'a', data_type: 'url' }, 'x'],
      [{ name: 'a', data_type: 'email' }, 'x'],
      [{ name: 'a', data_type: 'text' }, 'a\nb'],
      [{ name: 'a', data_type: 'uuid' }, 'x'],
      [{ name: 'a', data_type: 'enum', enum_values: ['b'] }, 'x'],
      [{ name: 'a', data_type: 'text', validation: { pattern: '^b$' } }, 'x'],
      [{ name: 'a', data_type: 'integer', validation: { min: 2 } }, 1],
      [{ name: 'a', data_type: 'integer', validation: { max: 0 } }, 1],
      [{ name: 'a', data_type: 'text', validation: { min_length: 2 } }, 'x'],
      [{ name: 'a', data_type: 'text', validation: { max_length: 0 } }, 'x'],
      [{ name: 'a', data_type: 'json', validation: { min_length: 2 } }, 5],
    ];
    const seen = new Set<string>();
    for (const [def, value] of cases) {
      const result = validateValue(def, value);
      expect(result, `${def.data_type} ${String(value)}`).not.toBeNull();
      const key = result!.key;
      seen.add(key);
      for (const catalog of [deDE, enUS]) {
        const text = key
          .split('.')
          .reduce<unknown>((node, part) => (node as Record<string, unknown>)?.[part], catalog);
        expect(typeof text, key).toBe('string');
        expect(text as string, key).toContain('{{label}}');
      }
    }
    expect(seen.size).toBe(cases.length);
  });
});

// ruleOf builds the same AST as FieldDefinition.Rule in
// backend/internal/fieldmeta (TestRuleOfDefinition there expects this JSON).
describe('ruleOf', () => {
  it('builds the shared rule AST of a definition', () => {
    const def: FieldDefinition = {
      name: 'port',
      data_type: 'integer',
      required: true,
      enum_values: ['a'],
      validation: { min: 1, max: 9, min_length: 2, pattern: '^x', patterns: ['y$'] },
    };
    expect(ruleOf(def, true)).toEqual({
      op: 'all',
      rules: [
        { op: 'required' },
        { op: 'enum', values: ['a'] },
        { op: 'pattern', pattern: '^x' },
        { op: 'pattern', pattern: 'y$' },
        { op: 'range', min: 1, max: 9 },
        { op: 'range', of: 'length', min: 2, max: undefined },
      ],
    });
  });

  it('rejects calendar days that do not exist and out-of-range IPv4', () => {
    expect(validateValue({ name: 'd', data_type: 'date' }, '2026-02-30')).not.toBeNull();
    expect(validateValue({ name: 'i', data_type: 'ip' }, '10.0.0.256')).not.toBeNull();
    expect(validateValue({ name: 'i', data_type: 'ip' }, '2001:db8::1')).toBeNull();
    expect(validateValue({ name: 'n', data_type: 'integer' }, '5')).not.toBeNull();
  });

  it('counts code points like the server', () => {
    const def: FieldDefinition = { name: 'x', data_type: 'text', validation: { max_length: 1 } };
    expect(validateValue(def, '😀')).toBeNull();
  });
});
