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
 */
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

// Pages whose raw values WP-527 (t7-i18n-status-b) moves to the catalog:
// file -> properties still shown raw there. CITypeAdminPage `category` is a
// free metamodel text (no enum) and stays allowed for good. An entry that no
// longer matches fails, so the list only shrinks.
const ALLOWLIST: Record<string, readonly string[]> = {
  '../pages/AgentListPage.tsx': ['status'],
  '../pages/MaintenancePage.tsx': ['status'],
  '../pages/FindingsPage.tsx': ['severity'],
  '../pages/DeskListPage.tsx': ['status'],
  '../pages/CompliancePage.tsx': ['category', 'status'],
  '../pages/SecurityPage.tsx': ['category', 'severity'],
  '../pages/IGAPage.tsx': ['severity', 'status'],
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
