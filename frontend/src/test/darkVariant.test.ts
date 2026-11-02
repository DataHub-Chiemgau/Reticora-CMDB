/**
 * UI-18/NFR-07 (WP-630, F-355/F-361): text reaches WCAG AA (4.5:1) in the
 * light and the dark theme. text-gray-500 and text-gray-400 (grey secondary
 * text) and text-red-600 (danger colour) fall below 4.5:1 on the dark
 * surface, text-gray-400 also on white. Secondary text uses the shared class
 * text-muted, text in the danger colour without a filled background the
 * shared class text-danger (src/index.css); both carry their dark variant.
 *
 * Source scan over src/pages/** and src/components/** (production .tsx,
 * tests excluded): every string of a class list (string literal or template
 * part) that holds text-gray-500, text-gray-400 or text-red-600 without a
 * variant prefix must also hold a dark:text-* class. The check is per string,
 * so a class chosen by a condition (`active ? 'text-primary' :
 * 'text-gray-500'`) is checked on its own.
 */
import ts from 'typescript';
import { describe, expect, it } from 'vitest';

const LOW_CONTRAST = new Set(['text-gray-500', 'text-gray-400', 'text-red-600']);

const sources = import.meta.glob<string>(['../pages/**/*.tsx', '../components/**/*.tsx'], {
  query: '?raw',
  import: 'default',
  eager: true,
});

// Read from disk next to this directory: Vitest turns CSS imports (also
// ?raw) into empty modules.
const css = ts.sys.readFile(
  decodeURIComponent(new URL(import.meta.url).pathname).replace(/test\/[^/]+$/, 'index.css'),
);

function isTestFile(file: string): boolean {
  return /\.(test|spec|auditdefect)\.tsx$/.test(file);
}

interface Hit {
  line: number;
  classes: string;
}

// missingDarkVariant returns every class string of the source that holds a
// LOW_CONTRAST class without a dark:text-* class next to it.
function missingDarkVariant(fileName: string, source: string): Hit[] {
  const sf = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const found: Hit[] = [];
  const visit = (node: ts.Node) => {
    if (
      ts.isStringLiteral(node) ||
      ts.isNoSubstitutionTemplateLiteral(node) ||
      ts.isTemplateHead(node) ||
      ts.isTemplateMiddle(node) ||
      ts.isTemplateTail(node)
    ) {
      const classes = node.text.split(/\s+/).filter(Boolean);
      if (
        classes.some((c) => LOW_CONTRAST.has(c)) &&
        !classes.some((c) => c.startsWith('dark:text-'))
      ) {
        found.push({
          line: sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1,
          classes: node.text.trim(),
        });
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
  return found;
}

// Justified exceptions, per file the class strings that keep a low-contrast
// class without a dark variant. Empty: no such text is left. An entry needs
// a reason (e.g. a fixed light background in both themes); an entry that no
// longer matches fails, so the list only shrinks.
const EXCEPTIONS: Record<string, readonly string[]> = {};

const NEGATIVE_FIXTURE = `
export function Fixture({ active }) {
  return (
    <div>
      <p className="text-sm text-gray-500">hint</p>
      <span className="text-gray-400">reason</span>
      <button className="text-xs text-red-600 hover:underline">delete</button>
      <a className={\`px-4 \${active ? 'text-primary' : 'text-gray-500'}\`}>tab</a>
      <p className="text-sm text-gray-500 dark:text-gray-400">ok</p>
      <p className="text-xs text-red-600 dark:text-red-400">ok</p>
      <p className="text-muted">ok</p>
      <p className="text-danger hover:text-red-600">ok</p>
      <p className="bg-red-600 text-white">ok</p>
    </div>
  );
}
`;

describe('dark variant of low-contrast text colours', () => {
  it('detects the class strings of the negative fixture', () => {
    expect(missingDarkVariant('Fixture.tsx', NEGATIVE_FIXTURE)).toEqual([
      { line: 5, classes: 'text-sm text-gray-500' },
      { line: 6, classes: 'text-gray-400' },
      { line: 7, classes: 'text-xs text-red-600 hover:underline' },
      { line: 8, classes: 'text-gray-500' },
    ]);
  });

  it('defines the shared classes with a dark variant', () => {
    expect(css).toMatch(/\.text-muted\s*\{\s*@apply text-gray-600 dark:text-gray-400;\s*\}/);
    expect(css).toMatch(/\.text-danger\s*\{\s*@apply text-red-600 dark:text-red-400;\s*\}/);
  });

  it('scans the pages and components', () => {
    const files = Object.keys(sources).filter((f) => !isTestFile(f));
    expect(files).toContain('../pages/InventoryPage.tsx');
    expect(files).toContain('../pages/LocationTreePage.tsx');
    expect(files.some((f) => f.startsWith('../components/'))).toBe(true);
    for (const file of Object.keys(EXCEPTIONS)) {
      expect(files, file).toContain(file);
    }
  });

  it('finds text-gray-500, text-gray-400 and text-red-600 only with a dark variant', () => {
    const offenders: string[] = [];
    const stale: string[] = [];
    for (const [file, source] of Object.entries(sources)) {
      if (isTestFile(file)) continue;
      const allowed = EXCEPTIONS[file] ?? [];
      const hits = missingDarkVariant(file, source);
      for (const hit of hits) {
        if (!allowed.includes(hit.classes)) offenders.push(`${file}:${hit.line} "${hit.classes}"`);
      }
      for (const classes of allowed) {
        if (!hits.some((hit) => hit.classes === classes)) stale.push(`${file} "${classes}"`);
      }
    }
    expect(offenders, 'use text-muted or text-danger (src/index.css)').toEqual([]);
    expect(stale, 'remove exceptions that no longer match').toEqual([]);
  });
});
