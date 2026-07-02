import { useTranslation } from 'react-i18next';
import { useState } from 'react';
import { CIListPage } from './pages/CIListPage';

type Page = 'dashboard' | 'cmdb' | 'discovery';

function App() {
  const { t } = useTranslation();
  const [page, setPage] = useState<Page>('cmdb');

  return (
    <div className="min-h-screen bg-surface text-gray-900 dark:text-gray-100">
      <header className="flex items-center justify-between border-b px-6 py-4">
        <h1 className="text-xl font-semibold">{t('app.title')}</h1>
        <nav className="flex gap-4">
          <NavButton active={page === 'dashboard'} onClick={() => setPage('dashboard')}>
            {t('nav.dashboard')}
          </NavButton>
          <NavButton active={page === 'cmdb'} onClick={() => setPage('cmdb')}>
            {t('nav.cmdb')}
          </NavButton>
          <NavButton active={page === 'discovery'} onClick={() => setPage('discovery')}>
            {t('nav.discovery')}
          </NavButton>
        </nav>
      </header>
      <main className="p-6">
        {page === 'dashboard' && <p>{t('nav.dashboard')}</p>}
        {page === 'cmdb' && <CIListPage />}
        {page === 'discovery' && <p>{t('nav.discovery')}</p>}
      </main>
    </div>
  );
}

function NavButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      onClick={onClick}
      className={`rounded px-3 py-1.5 text-sm font-medium transition-colors ${
        active
          ? 'bg-blue-600 text-white'
          : 'text-gray-600 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-gray-800'
      }`}
    >
      {children}
    </button>
  );
}

export default App;
