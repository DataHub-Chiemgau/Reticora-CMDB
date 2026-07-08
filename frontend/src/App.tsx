import { useMemo, useState } from 'react';
import { Routes, Route, useNavigate, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { CommandPalette } from './components/CommandPalette';
import type { AppPage } from './components/CommandPalette';
import { Button } from './components/ui/Button';
import { AssetListPage } from './pages/AssetListPage';
import { AssignmentListPage } from './pages/AssignmentListPage';
import { CIFormModal } from './pages/CIFormModal';
import { CIListPage } from './pages/CIListPage';
import { DashboardPage } from './pages/DashboardPage';
import { DiscoveryPage } from './pages/DiscoveryPage';
import { DocumentListPage } from './pages/DocumentListPage';
import { StocktakeListPage } from './pages/StocktakeListPage';
import { TicketListPage } from './pages/TicketListPage';
import { UserManagementPage } from './pages/UserManagementPage';
import { useThemeStore } from './stores/theme';

const pageToPath: Record<AppPage, string> = {
  dashboard: '/dashboard',
  cmdb: '/cmdb',
  discovery: '/discovery',
  assets: '/assets',
  assignments: '/assignments',
  documents: '/documents',
  stocktake: '/stocktake',
  tickets: '/tickets',
  users: '/users',
};

const pathToPage: Record<string, AppPage> = Object.fromEntries(
  Object.entries(pageToPath).map(([k, v]) => [v, k as AppPage])
);

function App() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const [isCreateOpen, setIsCreateOpen] = useState(false);
  const mode = useThemeStore((state) => state.mode);
  const setMode = useThemeStore((state) => state.setMode);

  const currentPage: AppPage = pathToPage[location.pathname] || 'cmdb';

  const shortcutHint = useMemo(() => {
    if (typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)) {
      return '⌘K';
    }
    return 'Ctrl+K';
  }, []);

  const isDark =
    mode === 'dark' ||
    (mode === 'system' && typeof document !== 'undefined' && document.documentElement.classList.contains('dark'));

  function toggleDarkMode() {
    setMode(isDark ? 'light' : 'dark');
  }

  function handleNavigate(page: AppPage) {
    navigate(pageToPath[page] || '/cmdb');
  }

  function openCreateCI() {
    navigate('/cmdb');
    setIsCreateOpen(true);
  }

  return (
    <div className="min-h-screen bg-surface text-gray-900 transition-colors dark:text-gray-100">
      <header className="border-b border-gray-200 bg-white/90 px-6 py-4 backdrop-blur dark:border-gray-800 dark:bg-gray-950/90">
        <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <h1 className="text-xl font-semibold">{t('app.title')}</h1>
          </div>
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
            <nav className="flex flex-wrap gap-2" aria-label={t('accessibility.primaryNavigation')}>
              <NavButton active={currentPage === 'dashboard'} onClick={() => handleNavigate('dashboard')}>
                {t('nav.dashboard')}
              </NavButton>
              <NavButton active={currentPage === 'cmdb'} onClick={() => handleNavigate('cmdb')}>
                {t('nav.cmdb')}
              </NavButton>
              <NavButton active={currentPage === 'assets'} onClick={() => handleNavigate('assets')}>
                {t('nav.assets', 'Inventar')}
              </NavButton>
              <NavButton active={currentPage === 'tickets'} onClick={() => handleNavigate('tickets')}>
                {t('nav.tickets', 'Tickets')}
              </NavButton>
              <NavButton active={currentPage === 'assignments'} onClick={() => handleNavigate('assignments')}>
                {t('nav.assignments', 'Zuweisungen')}
              </NavButton>
              <NavButton active={currentPage === 'documents'} onClick={() => handleNavigate('documents')}>
                {t('nav.documents', 'Dokumente')}
              </NavButton>
              <NavButton active={currentPage === 'stocktake'} onClick={() => handleNavigate('stocktake')}>
                {t('nav.stocktake', 'Inventur')}
              </NavButton>
              <NavButton active={currentPage === 'discovery'} onClick={() => handleNavigate('discovery')}>
                {t('nav.discovery')}
              </NavButton>
              <NavButton active={currentPage === 'users'} onClick={() => handleNavigate('users')}>
                {t('nav.users', 'Benutzer')}
              </NavButton>
            </nav>
            <div className="flex items-center gap-2 text-sm text-gray-500 dark:text-gray-400">
              <span>{shortcutHint}</span>
              <Button
                variant="secondary"
                size="sm"
                onClick={toggleDarkMode}
                aria-label={t('accessibility.toggleDarkMode')}
              >
                {isDark ? (
                  <svg viewBox="0 0 24 24" className="h-4 w-4" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="2">
                    <circle cx="12" cy="12" r="4" />
                    <path d="M12 2v2" />
                    <path d="M12 20v2" />
                    <path d="m4.93 4.93 1.41 1.41" />
                    <path d="m17.66 17.66 1.41 1.41" />
                    <path d="M2 12h2" />
                    <path d="M20 12h2" />
                    <path d="m6.34 17.66-1.41 1.41" />
                    <path d="m19.07 4.93-1.41 1.41" />
                  </svg>
                ) : (
                  <svg viewBox="0 0 24 24" className="h-4 w-4" aria-hidden="true" fill="currentColor">
                    <path d="M21 12.79A9 9 0 0 1 11.21 3c0-.34.02-.67.05-1A10 10 0 1 0 22 12c0-.05 0-.11-.01-.16-.3.63-.64.95-.99.95Z" />
                  </svg>
                )}
              </Button>
            </div>
          </div>
        </div>
      </header>
      <main className="p-6">
        <Routes>
          <Route path="/dashboard" element={<DashboardPage />} />
          <Route path="/cmdb" element={<CIListPage onCreateCI={openCreateCI} />} />
          <Route path="/discovery" element={<DiscoveryPage />} />
          <Route path="/assets" element={<AssetListPage />} />
          <Route path="/assignments" element={<AssignmentListPage />} />
          <Route path="/documents" element={<DocumentListPage />} />
          <Route path="/stocktake" element={<StocktakeListPage />} />
          <Route path="/tickets" element={<TicketListPage />} />
          <Route path="/users" element={<UserManagementPage />} />
          <Route path="*" element={<CIListPage onCreateCI={openCreateCI} />} />
        </Routes>
      </main>

      <CommandPalette onNavigate={handleNavigate} onCreateCI={openCreateCI} onToggleDarkMode={toggleDarkMode} />
      <CIFormModal open={isCreateOpen} onOpenChange={setIsCreateOpen} />
    </div>
  );
}

function NavButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <Button variant={active ? 'primary' : 'ghost'} size="sm" onClick={onClick}>
      {children}
    </Button>
  );
}

export default App;
