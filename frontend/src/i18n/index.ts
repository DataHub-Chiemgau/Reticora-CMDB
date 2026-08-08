import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import deDE from './de-DE.json';
import enUS from './en-US.json';

i18n.use(initReactI18next).init({
  resources: {
    'de-DE': { translation: deDE },
    'en-US': { translation: enUS },
  },
  lng: 'de-DE',
  fallbackLng: 'en-US',
  interpolation: {
    escapeValue: false,
  },
});

export default i18n;
