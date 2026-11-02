import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import deDE from './de-DE.json';
import enUS from './en-US.json';

/*
 * UI language (E-99, WP-523): the choice of the language switch in the header
 * is stored per browser; without a stored choice the browser language decides
 * (de* → de-DE, en* → en-US), anything else falls back to de-DE.
 */

export const UI_LANGUAGES = ['de-DE', 'en-US'] as const;
export type UILanguage = (typeof UI_LANGUAGES)[number];

export const DEFAULT_LANGUAGE: UILanguage = 'de-DE';
export const LANGUAGE_STORAGE_KEY = 'reticora-language';

function isUILanguage(value: unknown): value is UILanguage {
  return UI_LANGUAGES.includes(value as UILanguage);
}

/** languageOfBrowser maps a browser language tag: de* → de-DE, en* → en-US, else null. */
export function languageOfBrowser(tag: string | null | undefined): UILanguage | null {
  const primary = (tag ?? '').trim().toLowerCase().split(/[-_]/)[0];
  if (primary === 'de') return 'de-DE';
  if (primary === 'en') return 'en-US';
  return null;
}

/**
 * detectLanguage picks the UI language: a valid stored choice first, then the
 * browser language, then de-DE.
 */
export function detectLanguage(
  stored: string | null | undefined,
  browserLanguage: string | null | undefined,
): UILanguage {
  if (isUILanguage(stored)) return stored;
  return languageOfBrowser(browserLanguage) ?? DEFAULT_LANGUAGE;
}

/** readStoredLanguage returns the stored choice; storage may be unavailable. */
export function readStoredLanguage(): string | null {
  try {
    return typeof window === 'undefined' ? null : window.localStorage.getItem(LANGUAGE_STORAGE_KEY);
  } catch {
    return null;
  }
}

/** setLanguage switches the UI language and stores the choice for this browser. */
export async function setLanguage(language: UILanguage): Promise<void> {
  try {
    window.localStorage.setItem(LANGUAGE_STORAGE_KEY, language);
  } catch {
    // Without storage the choice holds for this page only.
  }
  await i18n.changeLanguage(language);
}

/** The language of the UI as one of the supported tags. */
export function currentLanguage(): UILanguage {
  return isUILanguage(i18n.language) ? i18n.language : DEFAULT_LANGUAGE;
}

// The document language follows the UI language (screen readers, hyphenation).
i18n.on('languageChanged', (language) => {
  if (typeof document !== 'undefined') {
    document.documentElement.lang = language;
  }
});

i18n.use(initReactI18next).init({
  resources: {
    'de-DE': { translation: deDE },
    'en-US': { translation: enUS },
  },
  lng: detectLanguage(
    readStoredLanguage(),
    typeof navigator === 'undefined' ? null : navigator.language,
  ),
  fallbackLng: 'en-US',
  interpolation: {
    escapeValue: false,
  },
});

export default i18n;
