import { useTranslation } from 'react-i18next';

function App() {
  const { t } = useTranslation();

  return (
    <div className="min-h-screen bg-surface text-gray-900 dark:text-gray-100">
      <header className="border-b px-6 py-4">
        <h1 className="text-xl font-semibold">{t('app.title')}</h1>
      </header>
      <main className="p-6">
        <p>{t('nav.dashboard')}</p>
      </main>
    </div>
  );
}

export default App;
