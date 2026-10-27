/**
 * GLO-10/UI-18 (WP-525, F-174): status, severity, category and state values
 * are English keys; the UI shows them through the i18n catalog
 * (t('<domain>.statuses.<value>', value)), never raw as `{x.status}`.
 *
 * Source scan over src/pages/** and src/components/** (production .tsx,
 * tests excluded): a JSX child expression that is just a member access
 * ending in status, severity, category or state is a raw enum value.
 * Attributes such as value={form.category} and values inside t() or a
 * template literal are not JSX children and stay allowed.
 *
 * WP-527 (t7-i18n-status-b): the same check runs as a lint rule
 * (no-restricted-syntax in eslint.config.js) for src/pages/** and
 * src/components/**; the negative fixture below goes through ESLint with the
 * project configuration.
 */
import { ESLint } from 'eslint';
import ts from 'typescript';
import { describe, expect, it } from 'vitest';

const ENUM_PROPERTIES = new Set(['status', 'severity', 'category', 'state']);

const sources = import.meta.glob<string>(['../pages/**/*.tsx', '../components/**/*.tsx'], {
  query: '?raw',
  import: 'default',
  eager: true,
});

function isTestFile(file: string): boolean {
  return /\.(test|spec|auditdefect)\.tsx$/.test(file);
}

interface RawEnum {
  line: number;
  property: string;
  text: string;
}

// rawEnumExpressions returns every JSX child `{<expr>.<prop>}` with prop in
// ENUM_PROPERTIES (also with optional chaining or a trailing `!`).
function rawEnumExpressions(fileName: string, source: string): RawEnum[] {
  const sf = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const found: RawEnum[] = [];
  const visit = (node: ts.Node) => {
    if (
      ts.isJsxExpression(node) &&
      node.expression &&
      (ts.isJsxElement(node.parent) || ts.isJsxFragment(node.parent))
    ) {
      let expr: ts.Expression = node.expression;
      while (ts.isParenthesizedExpression(expr) || ts.isNonNullExpression(expr)) {
        expr = expr.expression;
      }
      if (ts.isPropertyAccessExpression(expr) && ENUM_PROPERTIES.has(expr.name.text)) {
        found.push({
          line: sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1,
          property: expr.name.text,
          text: node.getText(sf),
        });
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
  return found;
}

// The only raw value left: the CI type template category is free metamodel
// text (ci_type.category, no CHECK constraint, no enum in the OpenAPI
// document), not a catalog key; the line carries a justified disable
// comment for the lint rule below. An entry that no longer matches fails, so
// the list only shrinks.
const ALLOWLIST: Record<string, readonly string[]> = {
  '../pages/CITypeAdminPage.tsx': ['category'],
};

const NEGATIVE_FIXTURE = `
export function Fixture({ item, x, form, t }) {
  return (
    <div>
      <Badge variant={variant(item.status)}>{item.status}</Badge>
      <td>{x?.severity}</td>
      <>{x.meta.state}</>
      <span>
        {form.category!}
      </span>
      <Select value={form.category} label={x.status} />
      <Badge>{t(\`asset.statuses.\${item.status}\`, item.status)}</Badge>
      <p>{\`\${x.category} \`}</p>
      <p>{x.statusText}</p>
    </div>
  );
}
`;

describe('raw enum values in JSX', () => {
  it('detects raw enum expressions in the negative fixture', () => {
    expect(rawEnumExpressions('Fixture.tsx', NEGATIVE_FIXTURE)).toEqual([
      { line: 5, property: 'status', text: '{item.status}' },
      { line: 6, property: 'severity', text: '{x?.severity}' },
      { line: 7, property: 'state', text: '{x.meta.state}' },
      { line: 9, property: 'category', text: '{form.category!}' },
    ]);
  });

  it('scans the pages and components', () => {
    const files = Object.keys(sources).filter((f) => !isTestFile(f));
    expect(files).toContain('../pages/AssetListPage.tsx');
    expect(files.some((f) => f.startsWith('../components/'))).toBe(true);
    for (const file of Object.keys(ALLOWLIST)) {
      expect(files, file).toContain(file);
    }
  });

  it('shows no raw status, severity, category or state outside the allowlist', () => {
    const offenders: string[] = [];
    const stale: string[] = [];
    for (const [file, source] of Object.entries(sources)) {
      if (isTestFile(file)) continue;
      const allowed = ALLOWLIST[file] ?? [];
      const hits = rawEnumExpressions(file, source);
      for (const hit of hits) {
        if (!allowed.includes(hit.property)) {
          offenders.push(`${file}:${hit.line} ${hit.text}`);
        }
      }
      for (const property of allowed) {
        if (!hits.some((hit) => hit.property === property)) {
          stale.push(`${file} ${property}`);
        }
      }
    }
    expect(offenders, 'use t(<domain>.statuses.<value>, value)').toEqual([]);
    expect(stale, 'remove allowlist entries that no longer match').toEqual([]);
  });
});

describe('lint rule against raw enum values', () => {
  const eslint = new ESLint();
  const lint = async (filePath: string, source: string) => {
    const [result] = await eslint.lintText(source, { filePath });
    return (result?.messages ?? [])
      .filter((m) => m.ruleId === 'no-restricted-syntax')
      .map((m) => ({ line: m.line, message: m.message }));
  };

  it.each(['src/pages/RawEnumFixture.tsx', 'src/components/RawEnumFixture.tsx'])(
    'reports the raw enum children of the negative fixture in %s',
    async (filePath) => {
      const messages = await lint(filePath, NEGATIVE_FIXTURE);
      expect(messages.map((m) => m.line)).toEqual([5, 6, 7, 9]);
      for (const m of messages) {
        expect(m.message).toMatch(/i18n catalog \(GLO-10\)/);
      }
    },
  );

  it('keeps the rule on the pages that also forbid literal texts', async () => {
    const messages = await lint('src/pages/DashboardPage.tsx', NEGATIVE_FIXTURE);
    expect(messages.filter((m) => /use t\(`/.test(m.message)).map((m) => m.line)).toEqual([
      5, 6, 7, 9,
    ]);
  });

  it('allows the CI type template category only through a justified disable comment', async () => {
    const source = sources['../pages/CITypeAdminPage.tsx'] ?? '';
    const lines = source.split('\n');
    const at = lines.findIndex((l) => l.trim() === '{tpl.category}');
    expect(at, 'CITypeAdminPage {tpl.category}').toBeGreaterThan(0);
    expect(lines[at - 1]).toMatch(
      /eslint-disable-next-line no-restricted-syntax -- GLO-10: .*free metamodel text/,
    );
    const [strict] = await new ESLint({ allowInlineConfig: false }).lintText(source, {
      filePath: 'src/pages/CITypeAdminPage.tsx',
    });
    expect(
      (strict?.messages ?? [])
        .filter((m) => m.ruleId === 'no-restricted-syntax')
        .map((m) => m.line),
    ).toEqual([at + 1]);
    expect(await lint('src/pages/CITypeAdminPage.tsx', source)).toEqual([]);
  });

  it('does not apply outside src/pages and src/components', async () => {
    expect(await lint('src/i18n/RawEnumFixture.tsx', NEGATIVE_FIXTURE)).toEqual([]);
  });
});
