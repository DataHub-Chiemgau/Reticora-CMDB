import '@testing-library/jest-dom/vitest';

// jsdom reports an English browser (en-US). The UI language follows the
// browser language without a stored choice (E-99), so the tests run as a
// German browser, the primary language the existing assertions expect.
Object.defineProperty(window.navigator, 'language', { value: 'de-DE', configurable: true });
Object.defineProperty(window.navigator, 'languages', {
  value: ['de-DE', 'de'],
  configurable: true,
});
