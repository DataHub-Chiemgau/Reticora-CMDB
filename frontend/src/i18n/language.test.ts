/**
 * E-99 (WP-523): the UI language is the choice stored in this browser; without
 * one the browser language decides (de* → de-DE, en* → en-US), anything else
 * is de-DE.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import i18n, {
  LANGUAGE_STORAGE_KEY,
  detectLanguage,
  languageOfBrowser,
  readStoredLanguage,
  setLanguage,
} from '.';

describe('languageOfBrowser', () => {
  it('maps German and English tags and nothing else', () => {
    expect(languageOfBrowser('de')).toBe('de-DE');
    expect(languageOfBrowser('de-AT')).toBe('de-DE');
    expect(languageOfBrowser('DE_ch')).toBe('de-DE');
    expect(languageOfBrowser('en')).toBe('en-US');
    expect(languageOfBrowser('en-GB')).toBe('en-US');
    expect(languageOfBrowser('fr-FR')).toBeNull();
    expect(languageOfBrowser('dex')).toBeNull();
    expect(languageOfBrowser('')).toBeNull();
    expect(languageOfBrowser(undefined)).toBeNull();
  });
});

describe('detectLanguage', () => {
  it('prefers the stored choice over the browser language', () => {
    expect(detectLanguage('en-US', 'de-DE')).toBe('en-US');
    expect(detectLanguage('de-DE', 'en-US')).toBe('de-DE');
    expect(detectLanguage('de-DE', 'fr-FR')).toBe('de-DE');
  });

  it('uses the browser language without a valid stored choice', () => {
    expect(detectLanguage(null, 'en-GB')).toBe('en-US');
    expect(detectLanguage(null, 'de-CH')).toBe('de-DE');
    // A stored value that is no UI language is ignored.
    expect(detectLanguage('fr-FR', 'en-US')).toBe('en-US');
    expect(detectLanguage('en', 'de-DE')).toBe('de-DE');
  });

  it('falls back to de-DE for any other browser language', () => {
    expect(detectLanguage(null, 'fr-FR')).toBe('de-DE');
    expect(detectLanguage(undefined, undefined)).toBe('de-DE');
    expect(detectLanguage('', '')).toBe('de-DE');
  });
});

describe('UI language at start-up and persistence', () => {
  afterEach(async () => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    vi.resetModules();
    window.localStorage.clear();
    await i18n.changeLanguage('de-DE');
  });

  /** startWith loads the i18n module afresh for a browser language. */
  async function startWith(browserLanguage: string) {
    vi.stubGlobal('navigator', { ...window.navigator, language: browserLanguage });
    vi.resetModules();
    return (await import('.')).default;
  }

  it('starts in the stored language, else in the browser language, else in German', async () => {
    window.localStorage.setItem(LANGUAGE_STORAGE_KEY, 'en-US');
    expect((await startWith('de-DE')).language).toBe('en-US');

    window.localStorage.clear();
    expect((await startWith('en-GB')).language).toBe('en-US');
    expect((await startWith('de-AT')).language).toBe('de-DE');
    expect((await startWith('ja-JP')).language).toBe('de-DE');
  });

  it('stores the choice of setLanguage and switches the UI and document language', async () => {
    await setLanguage('en-US');
    expect(window.localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBe('en-US');
    expect(readStoredLanguage()).toBe('en-US');
    expect(i18n.language).toBe('en-US');
    expect(i18n.t('language.label')).toBe('Language');
    expect(document.documentElement.lang).toBe('en-US');

    // The stored choice wins over the German browser on the next start.
    expect((await startWith('de-DE')).language).toBe('en-US');

    await setLanguage('de-DE');
    expect(window.localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBe('de-DE');
    expect(document.documentElement.lang).toBe('de-DE');
  });

  it('works without browser storage', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    expect(readStoredLanguage()).toBeNull();
    expect((await startWith('en-US')).language).toBe('en-US');
    await setLanguage('en-US');
    expect(i18n.language).toBe('en-US');
  });
});
