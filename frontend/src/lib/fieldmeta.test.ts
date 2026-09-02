import { describe, it, expect } from 'vitest';
import { evaluateField, validateValue, matchPredicate, type FieldDefinition } from './fieldmeta';

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

describe('validateValue', () => {
  it('requires values only when required', () => {
    const def: FieldDefinition = { name: 'x', data_type: 'text', required: true };
    expect(validateValue(def, '')).toMatch(/required/);
    expect(validateValue({ ...def, required: false }, '')).toBeNull();
  });

  it('validates ip/mac/email/url', () => {
    expect(validateValue({ name: 'ip', data_type: 'ip' }, '10.0.0.1')).toBeNull();
    expect(validateValue({ name: 'ip', data_type: 'ip' }, 'nope')).toMatch(/IP/);
    expect(validateValue({ name: 'm', data_type: 'mac' }, 'aa:bb:cc:dd:ee:ff')).toBeNull();
    expect(validateValue({ name: 'm', data_type: 'mac' }, 'xyz')).toMatch(/MAC/);
    expect(validateValue({ name: 'e', data_type: 'email' }, 'a@b.io')).toBeNull();
    expect(validateValue({ name: 'u', data_type: 'url' }, 'https://x.io/y')).toBeNull();
  });

  it('validates enum options', () => {
    const def: FieldDefinition = {
      name: 'os',
      data_type: 'enum',
      enum_values: ['linux', 'windows'],
    };
    expect(validateValue(def, 'linux')).toBeNull();
    expect(validateValue(def, 'plan9')).toMatch(/not an allowed value/);
  });

  it('validates numeric bounds', () => {
    const def: FieldDefinition = {
      name: 'cores',
      data_type: 'integer',
      validation: { min: 1, max: 64 },
    };
    expect(validateValue(def, 8)).toBeNull();
    expect(validateValue(def, 128)).toMatch(/at most/);
  });
});
