import js from '@eslint/js';
import globals from 'globals';
import reactHooks from 'eslint-plugin-react-hooks';
import tseslint from 'typescript-eslint';

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
  // GLO-10: these pages render status values and labels from the i18n
  // catalog only; a literal text or label is a missing catalog key.
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
