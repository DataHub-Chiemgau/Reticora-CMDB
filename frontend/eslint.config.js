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
      // WP-813: the stocktake detail and the label dialog (also matched by
      // src/components/flows/**/*.tsx below).
      'src/pages/StocktakeDetailPage.tsx',
      'src/components/flows/LabelPrintDialog.tsx',
      'src/pages/CITypeAdminPage.tsx',
      // WP-818: the impact panel and the topology page.
      'src/pages/TopologyPage.tsx',
      'src/components/graph/ImpactPanel.tsx',
      // WP-314: the template page and the metamodel components (WP-313).
      'src/pages/TemplatesPage.tsx',
      'src/components/metamodel/**/*.tsx',
      // WP-799: the work area of the metamodel drafts.
      'src/pages/MetamodelDraftsPage.tsx',
      // WP-318: the assignment page, the pickers and the flow dialogs.
      'src/pages/AssignmentListPage.tsx',
      'src/components/pickers/**/*.tsx',
      'src/components/flows/**/*.tsx',
      // WP-323: the relationship type administration and the edge dialog.
      'src/pages/RelationshipTypesPage.tsx',
      'src/components/cmdb/CIRelationshipsCard.tsx',
      // WP-315: the click builder and the saved views page.
      'src/components/rules/**/*.tsx',
      'src/pages/SavedViewsPage.tsx',
      // WP-817: the tag administration (the saved views page is listed above).
      'src/pages/TagsPage.tsx',
      // WP-801: the rule catalog.
      'src/pages/RulesPage.tsx',
      // WP-316: the form builder (LayoutEditor is matched by src/components/metamodel/**).
      'src/pages/FormBuilderPage.tsx',
      // WP-806: the inventory page and the object history.
      'src/pages/InventoryPage.tsx',
      'src/components/cmdb/ObjectHistory.tsx',
      // WP-320: the warehouse pages of the quantity items.
      'src/pages/ConsumableListPage.tsx',
      'src/pages/ConsumableDetailPage.tsx',
      // WP-321: the document pages, the upload and the document components.
      'src/pages/DocumentListPage.tsx',
      'src/pages/DocumentDetailPage.tsx',
      'src/components/upload/**/*.tsx',
      'src/components/documents/**/*.tsx',
      // WP-322: the import assistant and its mapping editor.
      'src/pages/ImportPage.tsx',
      'src/components/import/**/*.tsx',
    ],
    ignores: [
      'src/components/metamodel/**/*.test.tsx',
      'src/components/pickers/**/*.test.tsx',
      'src/components/flows/**/*.test.tsx',
      'src/components/rules/**/*.test.tsx',
      'src/components/upload/**/*.test.tsx',
      'src/components/documents/**/*.test.tsx',
      'src/components/import/**/*.test.tsx',
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
