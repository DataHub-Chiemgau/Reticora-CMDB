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
 *
 * WP-523: the scan above sees `{x.status}` only. Where the field name is data
 * (the provenance rows, the field changes of the history, the value an
 * override covers) the CI status reached the page as `"maintenance"` through
 * JSON.stringify(fv.value) and `${historyValue(change.new)}`; the real-stack
 * test e2e-real/i18n.spec.ts found it. The second scan below requires such a
 * field value to go through fieldValueText (components/cmdb/fieldValue.ts),
 * which shows the catalog label of a catalog-keyed field.
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

// Members that hold the value of a field whose name is data: provenance
// (value, discovered_value), history (old, new, old_value, new_value).
const FIELD_VALUE_PROPERTIES = new Set([
  'value',
  'discovered_value',
  'old',
  'new',
  'old_value',
  'new_value',
]);

// fieldValueExpressions returns every JSX child that renders a field value
// (a FIELD_VALUE_PROPERTIES member or JSON.stringify(...)) that does not go
// through fieldValueText(...). Nested JSX is checked as its own child, t()
// is a catalog lookup; neither is searched from the outer child.
function fieldValueExpressions(fileName: string, source: string): RawEnum[] {
  const sf = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const found: RawEnum[] = [];
  const isCallTo = (node: ts.Node, name: string) =>
    ts.isCallExpression(node) && node.expression.getText(sf) === name;
  const visit = (node: ts.Node) => {
    if (
      ts.isJsxExpression(node) &&
      node.expression &&
      (ts.isJsxElement(node.parent) || ts.isJsxFragment(node.parent))
    ) {
      let property = '';
      const search = (inner: ts.Node) => {
        if (property) return;
        if (
          ts.isJsxElement(inner) ||
          ts.isJsxSelfClosingElement(inner) ||
          ts.isJsxFragment(inner) ||
          isCallTo(inner, 't') ||
          isCallTo(inner, 'fieldValueText')
        ) {
          return;
        }
        if (isCallTo(inner, 'JSON.stringify')) {
          property = 'JSON.stringify';
          return;
        }
        if (ts.isPropertyAccessExpression(inner) && FIELD_VALUE_PROPERTIES.has(inner.name.text)) {
          property = inner.name.text;
          return;
        }
        ts.forEachChild(inner, search);
      };
      search(node.expression);
      if (property) {
        found.push({
          line: sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1,
          property,
          text: node.getText(sf).replace(/\s+/g, ' '),
        });
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
  return found;
}

// Field values shown without the catalog on purpose, by file and JSX child:
// the attribute JSON of a CI (free metamodel values, no catalog keys), the
// result payload of a job (technical JSON) and the latest metric sample (a
// number). An entry that no longer matches fails, so the list only shrinks.
const FIELD_VALUE_ALLOWLIST: Record<string, readonly string[]> = {
  '../pages/CIDetailPage.tsx': ['{JSON.stringify(ci.attributes, null, 2)}'],
  '../pages/JobCenterPage.tsx': ['{JSON.stringify(shown?.result, null, 2)}'],
  '../components/cmdb/CIDetailSections.tsx': ["{latest ? latest.value : '–'}"],
};

const FIELD_VALUE_FIXTURE = `
export function Fixture({ fv, change, entry, rows, t }) {
  return (
    <dl>
      <dd>{JSON.stringify(fv.value) ?? '—'}</dd>
      <dd>{fv.reason === 'override' ? fv.discovered_value : '—'}</dd>
      <li>{\`\${change.field}: \${show(change.old)} → \${show(change.new)}\`}</li>
      <dd>{entry.new_value}</dd>
      <dd>{fieldValueText(t, fv.field, fv.value, show)}</dd>
      <li>{\`\${change.field}: \${fieldValueText(t, change.field, change.new, show)}\`}</li>
      <dd>{t('ci.value', { value: fv.value })}</dd>
      <Input value={fv.value} />
      {rows.map((row) => (
        <span key={row.id}>{row.name}</span>
      ))}
    </dl>
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

describe('field values in JSX (WP-523)', () => {
  it('detects field values without fieldValueText in the negative fixture', () => {
    expect(
      fieldValueExpressions('Fixture.tsx', FIELD_VALUE_FIXTURE).map(({ line, property }) => ({
        line,
        property,
      })),
    ).toEqual([
      { line: 5, property: 'JSON.stringify' },
      { line: 6, property: 'discovered_value' },
      { line: 7, property: 'old' },
      { line: 8, property: 'new_value' },
    ]);
  });

  it('detects the provenance and history values of the CI detail before WP-523', () => {
    const before = `
      <dl>
        <dd className="font-mono font-semibold">{JSON.stringify(fv.value) ?? '—'}</dd>
        <li>{\`\${change.field}: \${historyValue(change.old)} → \${historyValue(change.new)}\`}</li>
        <dd>{display(provenance.discovered_value)}</dd>
      </dl>`;
    expect(fieldValueExpressions('Before.tsx', before).map((hit) => hit.property)).toEqual([
      'JSON.stringify',
      'old',
      'discovered_value',
    ]);
  });

  it('shows no field value without fieldValueText outside the allowlist', () => {
    const offenders: string[] = [];
    const stale: string[] = [];
    for (const [file, source] of Object.entries(sources)) {
      if (isTestFile(file)) continue;
      const allowed = FIELD_VALUE_ALLOWLIST[file] ?? [];
      const hits = fieldValueExpressions(file, source);
      for (const hit of hits) {
        if (!allowed.includes(hit.text)) offenders.push(`${file}:${hit.line} ${hit.text}`);
      }
      for (const text of allowed) {
        if (!hits.some((hit) => hit.text === text)) stale.push(`${file} ${text}`);
      }
    }
    expect(offenders, 'use fieldValueText(t, field, value, format)').toEqual([]);
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
