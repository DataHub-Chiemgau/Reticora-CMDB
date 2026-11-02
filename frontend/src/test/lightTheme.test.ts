/**
 * UI-18/NFR-07: the theme the user chose is the only source of the page
 * theme, on either OS setting. Before, src/index.css took the dark surface from
 * @media (prefers-color-scheme: dark) while every dark: variant follows the
 * .dark class (tailwind.config.ts darkMode 'class'): the light theme on a dark
 * OS put dark text on a near-black canvas (1:1), and dialogs portaled under
 * <body> (outside the App shell's text colour) showed the browser default
 * black on the dark dialog. src/test/darkVariant.test.ts covers the dark side
 * of low-contrast text; this file covers the rest:
 *
 * - src/index.css: a prefers-color-scheme block only as the pre-JS fallback
 *   (:not([data-theme]), stores/theme.ts sets data-theme), color-scheme per
 *   theme, the dark surface on .dark and a theme text colour on body.
 * - Radix portals: every <X.Content> inside an <X.Portal> carries a base and a
 *   dark: text colour, since it inherits nothing from the App shell.
 * - Light completeness: a class string with a dark: colour also names the
 *   light colour of that property (bg, text, border, ...), instead of leaving
 *   the light theme to browser defaults.
 * - Hard-coded hex colours in pages and components bypass the theme (no dark:
 *   variant); SVG takes fill-/stroke- classes. Canvas (WebGL) colours cannot,
 *   they are listed with their reason.
 */
import ts from 'typescript';
import { describe, expect, it } from 'vitest';

const allSources = import.meta.glob<string>(['../**/*.tsx'], {
  query: '?raw',
  import: 'default',
  eager: true,
});

// Read from disk next to this directory: Vitest turns CSS imports (also
// ?raw) into empty modules.
const css =
  ts.sys.readFile(
    decodeURIComponent(new URL(import.meta.url).pathname).replace(/test\/[^/]+$/, 'index.css'),
  ) ?? '';

function isTestFile(file: string): boolean {
  return /\.(test|spec|auditdefect)\.tsx$/.test(file);
}

const productionSources = Object.entries(allSources).filter(([file]) => !isTestFile(file));
const pageAndComponentSources = productionSources.filter(([file]) =>
  /^\.\.\/(pages|components)\//.test(file),
);
// The pages and components plus the App shell (header, navigation).
const uiSources = productionSources.filter(
  ([file]) => file === '../App.tsx' || /^\.\.\/(pages|components)\//.test(file),
);

interface Hit {
  line: number;
  text: string;
}

function parse(fileName: string, source: string): ts.SourceFile {
  return ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
}

function lineOf(sf: ts.SourceFile, node: ts.Node): number {
  return sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1;
}

function isClassText(node: ts.Node): node is ts.StringLiteral | ts.TemplateLiteralLikeNode {
  return (
    ts.isStringLiteral(node) ||
    ts.isNoSubstitutionTemplateLiteral(node) ||
    ts.isTemplateHead(node) ||
    ts.isTemplateMiddle(node) ||
    ts.isTemplateTail(node)
  );
}

// --- src/index.css ---------------------------------------------------------

// block returns the body of the rule that opens at the brace at index open.
function block(source: string, open: number): string {
  let depth = 0;
  for (let i = open; i < source.length; i += 1) {
    if (source[i] === '{') depth += 1;
    if (source[i] === '}') {
      depth -= 1;
      if (depth === 0) return source.slice(open + 1, i);
    }
  }
  return source.slice(open + 1);
}

function stripComments(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\//g, '');
}

// unguardedSchemeSelectors returns the selectors inside a
// @media (prefers-color-scheme: ...) block that do not carry
// :not([data-theme]): they would follow the OS after stores/theme.ts has
// applied the chosen theme.
function unguardedSchemeSelectors(source: string): string[] {
  const text = stripComments(source);
  const found: string[] = [];
  for (const match of text.matchAll(/@media[^{]*prefers-color-scheme[^{]*\{/g)) {
    const body = block(text, match.index + match[0].length - 1);
    for (const rule of body.matchAll(/([^{}]+)\{[^{}]*\}/g)) {
      for (const selector of rule[1]!.split(',').map((s) => s.trim())) {
        if (selector && !selector.includes(':not([data-theme])')) found.push(selector);
      }
    }
  }
  return found;
}

// topLevelRule returns the declarations of the first rule with exactly this
// selector outside any at-rule.
function topLevelRule(source: string, selector: string): string | undefined {
  const text = stripComments(source);
  let depth = 0;
  let start = 0;
  for (let i = 0; i < text.length; i += 1) {
    if (text[i] === '{') {
      if (depth === 0 && text.slice(start, i).trim() === selector) return block(text, i);
      depth += 1;
    } else if (text[i] === '}') {
      depth -= 1;
      if (depth === 0) start = i + 1;
    } else if (depth === 0 && text[i] === ';') {
      start = i + 1;
    }
  }
  return undefined;
}

const NEGATIVE_CSS = `
:root { --color-surface: 0 0% 100%; }
@media (prefers-color-scheme: dark) {
  :root { --color-surface: 220 13% 10%; }
  :root:not([data-theme]), body { color-scheme: dark; }
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme]) { color-scheme: dark; }
}
`;

describe('theme source in src/index.css', () => {
  it('detects the unguarded selectors of the negative fixture', () => {
    expect(unguardedSchemeSelectors(NEGATIVE_CSS)).toEqual([':root', 'body']);
    expect(topLevelRule(NEGATIVE_CSS, ':root')).toContain('--color-surface: 0 0% 100%');
    expect(topLevelRule(NEGATIVE_CSS, '.dark')).toBeUndefined();
  });

  it('follows the OS preference only before the theme is applied', () => {
    expect(css).not.toBe('');
    expect(unguardedSchemeSelectors(css), 'scope it to :root:not([data-theme])').toEqual([]);
  });

  it('declares the colour scheme and surface of both themes', () => {
    const root = topLevelRule(css, ':root');
    const dark = topLevelRule(css, '.dark');
    expect(root).toMatch(/(^|;|\s)color-scheme:\s*light\s*;/);
    expect(root).toMatch(/--color-surface:\s*0 0% 100%\s*;/);
    expect(dark).toMatch(/(^|;|\s)color-scheme:\s*dark\s*;/);
    expect(dark).toMatch(/--color-surface:\s*220 13% 10%\s*;/);
    expect(topLevelRule(css, 'html')).toMatch(
      /background-color:\s*hsl\(var\(--color-surface\)\)\s*;/,
    );
  });

  it('gives body the theme text colour for portaled content', () => {
    expect(css).toMatch(
      /@layer base\s*\{\s*body\s*\{\s*@apply text-gray-900 dark:text-gray-100;\s*\}\s*\}/,
    );
  });
});

// --- Radix portals ---------------------------------------------------------

const BASE_TEXT_COLOUR = /^text-(?:white|black|[a-z]+-\d{2,3})$/;

function tagName(node: ts.JsxElement | ts.JsxSelfClosingElement): string {
  const tag = ts.isJsxElement(node) ? node.openingElement.tagName : node.tagName;
  return tag.getText();
}

// classTokens returns the classes of every string in the className
// attribute (a literal, a template or the arguments of cn()).
function classTokens(node: ts.JsxElement | ts.JsxSelfClosingElement): string[] {
  const attributes = ts.isJsxElement(node) ? node.openingElement.attributes : node.attributes;
  const tokens: string[] = [];
  for (const attribute of attributes.properties) {
    if (!ts.isJsxAttribute(attribute) || attribute.name.getText() !== 'className') continue;
    const visit = (child: ts.Node) => {
      if (isClassText(child)) tokens.push(...child.text.split(/\s+/).filter(Boolean));
      ts.forEachChild(child, visit);
    };
    if (attribute.initializer) visit(attribute.initializer);
  }
  return tokens;
}

// portalContentWithoutText returns every <X.Content> inside an <X.Portal>
// whose className lacks a base text colour or a dark:text-* class.
function portalContentWithoutText(fileName: string, source: string): Hit[] {
  const sf = parse(fileName, source);
  const found: Hit[] = [];
  const visit = (node: ts.Node, inPortal: boolean) => {
    let portal = inPortal;
    if (ts.isJsxElement(node) || ts.isJsxSelfClosingElement(node)) {
      const name = tagName(node);
      if (/(^|\.)Portal$/.test(name)) portal = true;
      if (portal && /\.Content$/.test(name)) {
        const tokens = classTokens(node);
        if (
          !tokens.some((c) => BASE_TEXT_COLOUR.test(c)) ||
          !tokens.some((c) => c.startsWith('dark:text-'))
        ) {
          found.push({ line: lineOf(sf, node), text: name });
        }
      }
    }
    ts.forEachChild(node, (child) => visit(child, portal));
  };
  visit(sf, false);
  return found;
}

const PORTAL_FIXTURE = `
export function Fixture() {
  return (
    <Dialog.Root>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 bg-gray-950/50" />
        <Dialog.Content className="bg-white p-6 dark:bg-gray-900">missing</Dialog.Content>
      </Dialog.Portal>
      <Popover.Portal>
        <div>
          <Popover.Content className="text-sm text-gray-900" />
        </div>
      </Popover.Portal>
      <DropdownMenu.Portal>
        <DropdownMenu.Content className={cn('rounded', 'text-gray-900 dark:text-gray-100')} />
      </DropdownMenu.Portal>
      <Dialog.Content className="bg-white">inline, inherits the shell</Dialog.Content>
    </Dialog.Root>
  );
}
`;

describe('text colour of portaled content', () => {
  it('detects the elements of the negative fixture', () => {
    expect(portalContentWithoutText('Fixture.tsx', PORTAL_FIXTURE)).toEqual([
      { line: 7, text: 'Dialog.Content' },
      { line: 11, text: 'Popover.Content' },
    ]);
  });

  it('finds the portals of the shared dialogs', () => {
    const files = productionSources.map(([file]) => file);
    expect(files).toContain('../components/ui/Modal.tsx');
    expect(files).toContain('../components/CommandPalette.tsx');
    expect(files).toContain('../App.tsx');
  });

  it('gives every portaled content a base and a dark text colour', () => {
    const offenders: string[] = [];
    for (const [file, source] of productionSources) {
      for (const hit of portalContentWithoutText(file, source)) {
        offenders.push(`${file}:${hit.line} <${hit.text}>`);
      }
    }
    expect(offenders, 'add text-gray-900 dark:text-gray-100 (outside the App shell)').toEqual([]);
  });
});

// --- light completeness ----------------------------------------------------

// A colour utility: the property (bg, text, border, ...) and a colour value,
// so text-sm or border-b do not count.
const COLOUR_UTILITY =
  /^(bg|text|border|divide|ring|fill|stroke|placeholder|outline|accent)-(?:[trblxy]-)?(?:inherit|current|transparent|black|white|primary|surface|muted|danger|[a-z]+-\d{2,3})(?:\/\d+)?$/;

// darkWithoutBase returns every class string that holds a dark:<property>-
// colour without an unprefixed colour of the same property: the light theme
// would fall back to browser defaults there.
function darkWithoutBase(fileName: string, source: string): Hit[] {
  const sf = parse(fileName, source);
  const found: Hit[] = [];
  const visit = (node: ts.Node) => {
    if (isClassText(node)) {
      const base = new Set<string>();
      const dark = new Set<string>();
      for (const token of node.text.split(/\s+/).filter(Boolean)) {
        const variants = token.split(':');
        const property = COLOUR_UTILITY.exec(variants.pop() ?? '')?.[1];
        if (!property) continue;
        if (variants.length === 0) base.add(property);
        else if (variants.length === 1 && variants[0] === 'dark') dark.add(property);
      }
      if ([...dark].some((property) => !base.has(property))) {
        found.push({ line: lineOf(sf, node), text: node.text.trim() });
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
  return found;
}

// Justified exceptions, per file the class strings that keep a dark: colour
// without its light counterpart. Empty: none is left. An entry needs a reason;
// an entry that no longer matches fails, so the list only shrinks.
const BASE_EXCEPTIONS: Record<string, readonly string[]> = {};

const BASE_FIXTURE = `
export function Fixture() {
  return (
    <div>
      <input className="border border-gray-300 px-2 dark:bg-gray-900 dark:text-white" />
      <thead className="border-b bg-gray-50 dark:border-gray-800 dark:bg-gray-950" />
      <input className="border border-gray-300 bg-white text-gray-900 dark:bg-gray-900 dark:text-white" />
      <p className="text-sm text-muted dark:hover:text-gray-100">ok</p>
      <button className="hover:bg-gray-100 dark:hover:bg-gray-800">ok</button>
    </div>
  );
}
`;

describe('light counterpart of dark colours', () => {
  it('detects the class strings of the negative fixture', () => {
    expect(darkWithoutBase('Fixture.tsx', BASE_FIXTURE)).toEqual([
      { line: 5, text: 'border border-gray-300 px-2 dark:bg-gray-900 dark:text-white' },
      { line: 6, text: 'border-b bg-gray-50 dark:border-gray-800 dark:bg-gray-950' },
    ]);
  });

  it('names the light colour next to every dark colour', () => {
    const files = uiSources.map(([file]) => file);
    expect(files).toContain('../App.tsx');
    for (const file of Object.keys(BASE_EXCEPTIONS)) expect(files, file).toContain(file);
    const offenders: string[] = [];
    const stale: string[] = [];
    for (const [file, source] of uiSources) {
      const allowed = BASE_EXCEPTIONS[file] ?? [];
      const hits = darkWithoutBase(file, source);
      for (const hit of hits) {
        if (!allowed.includes(hit.text)) offenders.push(`${file}:${hit.line} "${hit.text}"`);
      }
      for (const text of allowed) {
        if (!hits.some((hit) => hit.text === text)) stale.push(`${file} "${text}"`);
      }
    }
    expect(offenders, 'add the light colour (bg-white, text-gray-900, ...)').toEqual([]);
    expect(stale, 'remove exceptions that no longer match').toEqual([]);
  });
});

// --- hard-coded colours ----------------------------------------------------

const HEX_COLOUR = /^#(?:[0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})$/i;

// hexColours returns every string literal of the source that is a hex colour.
function hexColours(fileName: string, source: string): Hit[] {
  const sf = parse(fileName, source);
  const found: Hit[] = [];
  const visit = (node: ts.Node) => {
    if (
      (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) &&
      HEX_COLOUR.test(node.text)
    ) {
      found.push({ line: lineOf(sf, node), text: node.text.toLowerCase() });
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
  return found;
}

// The hex colours that stay, per file with the reason. An entry that no
// longer occurs fails, so the list only shrinks.
const HEX_EXCEPTIONS: Record<string, { colours: readonly string[]; reason: string }> = {
  '../pages/TopologyPage.tsx': {
    colours: ['#16a34a', '#6b7280', '#d97706', '#dc2626', '#4f46e5'],
    reason: 'status colours of the graph nodes, drawn by sigma on a WebGL canvas (no CSS classes)',
  },
  '../components/graph/TopologyGraph.tsx': {
    colours: ['#d1d5db', '#e5e7eb', '#dc2626', '#4f46e5'],
    reason: 'dimmed, highlight and default colours of the sigma WebGL graph (no CSS classes)',
  },
  '../components/rack/RackSVG.tsx': {
    colours: ['#4f46e5'],
    reason:
      'default fill of a rack item: items carry their own data colour as fill; white label 6.3:1 in both themes',
  },
};

const HEX_FIXTURE = `
export function Fixture({ color }) {
  return (
    <svg>
      <rect fill="#1f2937" stroke="#374151" className="fill-gray-100" />
      <text fill={color ?? '#9CA3AF'}>1</text>
      <a href="#main-content">skip</a>
    </svg>
  );
}
`;

describe('hard-coded colours in pages and components', () => {
  it('detects the colours of the negative fixture', () => {
    expect(hexColours('Fixture.tsx', HEX_FIXTURE)).toEqual([
      { line: 5, text: '#1f2937' },
      { line: 5, text: '#374151' },
      { line: 6, text: '#9ca3af' },
    ]);
  });

  it('uses theme classes instead of hex colours outside the listed canvases', () => {
    const files = pageAndComponentSources.map(([file]) => file);
    for (const file of Object.keys(HEX_EXCEPTIONS)) expect(files, file).toContain(file);
    const offenders: string[] = [];
    const stale: string[] = [];
    for (const [file, source] of pageAndComponentSources) {
      const allowed = HEX_EXCEPTIONS[file]?.colours ?? [];
      const hits = hexColours(file, source);
      for (const hit of hits) {
        if (!allowed.includes(hit.text)) offenders.push(`${file}:${hit.line} ${hit.text}`);
      }
      for (const colour of allowed) {
        if (!hits.some((hit) => hit.text === colour)) stale.push(`${file} ${colour}`);
      }
    }
    expect(offenders, 'use fill-/stroke-/text- classes with a dark: variant').toEqual([]);
    expect(stale, 'remove exceptions that no longer match').toEqual([]);
  });
});
