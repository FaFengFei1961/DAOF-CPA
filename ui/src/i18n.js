import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import HttpApi from 'i18next-http-backend';

i18n
  .use(HttpApi)
  .use(initReactI18next)
  .init({
    fallbackLng: 'zh-CN',
    lng: 'zh-CN', // Default language
    debug: false,
    backend: {
      loadPath: '/api/i18n/locales/{{lng}}',
    },
    interpolation: {
      escapeValue: false, // not needed for react as it escapes by default
    }
  });

export default i18n;
