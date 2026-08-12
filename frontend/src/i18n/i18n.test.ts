import { describe, expect, it } from 'vitest';
import deDE from './de-DE.json';
import enUS from './en-US.json';

function flatten(record: Record<string, unknown>, prefix = ''): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(record)) {
    if (value && typeof value === 'object') {
      Object.assign(out, flatten(value as Record<string, unknown>, `${prefix}${key}.`));
    } else {
      out[`${prefix}${key}`] = String(value);
    }
  }
  return out;
}

const flatDE = flatten(deDE);
const flatEN = flatten(enUS);

describe('i18n resources', () => {
  it('keeps de-DE and en-US key sets in sync', () => {
    const missingInDE = Object.keys(flatEN).filter((key) => !(key in flatDE));
    const missingInEN = Object.keys(flatDE).filter((key) => !(key in flatEN));
    expect(missingInDE, `keys missing in de-DE: ${missingInDE.join(', ')}`).toEqual([]);
    expect(missingInEN, `keys missing in en-US: ${missingInEN.join(', ')}`).toEqual([]);
  });

  it('has no empty translation values', () => {
    for (const [key, value] of Object.entries(flatDE)) {
      expect(value.trim(), `de-DE key ${key} is empty`).not.toBe('');
    }
    for (const [key, value] of Object.entries(flatEN)) {
      expect(value.trim(), `en-US key ${key} is empty`).not.toBe('');
    }
  });

  it('covers every t() key used in source files (fallbacks would hide gaps)', () => {
    // Many call sites pass t('key', 'Fallback'); if the key were missing from
    // the resources, the hardcoded fallback would silently win and the German
    // primary language could regress unnoticed. This test asserts every
    // statically referenced key exists in both locales, making the fallback
    // argument dead code instead of a hidden translation source.
    const sources = import.meta.glob('../**/*.{ts,tsx}', {
      query: '?raw',
      import: 'default',
      eager: true,
    }) as Record<string, string>;

    const keyPattern = /\bt\(\s*'([a-zA-Z0-9._-]+)'/g;
    const missing: string[] = [];
    for (const [path, content] of Object.entries(sources)) {
      if (path.endsWith('.test.tsx') || path.endsWith('.test.ts')) {
        continue;
      }
      for (const match of content.matchAll(keyPattern)) {
        const key = match[1] as string;
        if (!(key in flatDE)) {
          missing.push(`${path}: ${key} missing in de-DE`);
        }
        if (!(key in flatEN)) {
          missing.push(`${path}: ${key} missing in en-US`);
        }
      }
    }
    expect(missing, `uncovered t() keys:\n${missing.join('\n')}`).toEqual([]);
  });
});
