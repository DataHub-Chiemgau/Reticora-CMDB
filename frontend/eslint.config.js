import js from '@eslint/js';
import globals from 'globals';
import reactHooks from 'eslint-plugin-react-hooks';
import tseslint from 'typescript-eslint';

// GLO-10 (WP-527, F-174): status, severity, category and state values are
// English keys; a JSX child that is just such a member access shows the raw
// key instead of the catalog label t('<domain>.statuses.<value>', value).
// Attributes such as value={form.category} are no JSX children and stay
// allowed; optional chaining and a trailing `!` are caught as well.
const RAW_ENUM_PROPERTY = '[property.name=/^(status|severity|category|state)$/]';
const RAW_ENUM_MESSAGE =
  'Enum values come from the i18n catalog (GLO-10): use t(`<domain>.statuses.${value}`, value).';
const rawEnumChildren = [
  `JSXElement > JSXExpressionContainer > MemberExpression${RAW_ENUM_PROPERTY}`,
  `JSXFragment > JSXExpressionContainer > MemberExpression${RAW_ENUM_PROPERTY}`,
  `:matches(JSXElement, JSXFragment) > JSXExpressionContainer > ChainExpression > MemberExpression${RAW_ENUM_PROPERTY}`,
  `:matches(JSXElement, JSXFragment) > JSXExpressionContainer > TSNonNullExpression > MemberExpression${RAW_ENUM_PROPERTY}`,
].map((selector) => ({ selector, message: RAW_ENUM_MESSAGE }));

export default tseslint.config(
  { ignores: ['dist'] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    plugins: {
      'react-hooks': reactHooks,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
    },
  },
  // GLO-10: pages and components render enum values through the catalog
  // (negative fixture: src/i18n/rawEnum.test.ts).
  {
    files: ['src/pages/**/*.tsx', 'src/components/**/*.tsx'],
    rules: {
      'no-restricted-syntax': ['error', ...rawEnumChildren],
    },
  },
  // GLO-10: these pages render status values and labels from the i18n
  // catalog only; a literal text or label is a missing catalog key. Rule
  // options replace those of the block above, so the enum selectors repeat.
  {
    files: [
      'src/pages/DashboardPage.tsx',
      'src/pages/DiscoveryPage.tsx',
      'src/pages/StocktakeListPage.tsx',
      'src/pages/CITypeAdminPage.tsx',
    ],
    rules: {
      'no-restricted-syntax': [
        'error',
        ...rawEnumChildren,
        {
          selector: 'JSXText[value=/[A-Za-zÄÖÜäöüß]{2,}/]',
          message: 'UI text comes from the i18n catalog (GLO-10): use t().',
        },
        {
          selector: "Property[key.name='label'] > Literal[value=/[A-Za-zÄÖÜäöüß]{2,}/]",
          message: 'Labels come from the i18n catalog (GLO-10): use t().',
        },
      ],
    },
  },
);
