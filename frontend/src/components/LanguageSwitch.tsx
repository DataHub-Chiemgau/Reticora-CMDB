/**
 * LanguageSwitch selects the UI language in the header (E-99, WP-523). The
 * choice is stored per browser; a native select keeps it operable by keyboard
 * and named for screen readers.
 */
import { useTranslation } from 'react-i18next';
import { UI_LANGUAGES, currentLanguage, setLanguage, type UILanguage } from '../i18n';
import { Select } from './ui/Select';

export function LanguageSwitch() {
  // useTranslation renders again on every language change.
  const { t } = useTranslation();
  const language = currentLanguage();

  return (
    <Select
      aria-label={t('language.label')}
      value={language}
      onChange={(event) => void setLanguage(event.target.value as UILanguage)}
      className="w-auto py-1"
      options={UI_LANGUAGES.map((value) => ({ value, label: t(`language.${value}`) }))}
    />
  );
}
