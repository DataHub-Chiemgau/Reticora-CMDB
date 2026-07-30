import * as Dialog from '@radix-ui/react-dialog';
import { useEffect, useMemo, useState } from 'react';
import type { KeyboardEvent as ReactKeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Badge } from './ui/Badge';
import { cn } from './ui/utils';

export type AppPage =
  | 'dashboard'
  | 'cmdb'
  | 'topology'
  | 'racks'
  | 'discovery'
  | 'assets'
  | 'assignments'
  | 'documents'
  | 'stocktake'
  | 'tickets'
  | 'users';

interface CommandPaletteProps {
  onNavigate: (page: AppPage) => void;
  onCreateCI: () => void;
  onToggleDarkMode: () => void;
}

interface CommandItem {
  id: string;
  label: string;
  keywords: string[];
  action: () => void;
}

function fuzzyMatch(value: string, query: string) {
  if (!query) {
    return true;
  }

  const normalizedValue = value.toLowerCase();
  const normalizedQuery = query.toLowerCase().trim();

  if (normalizedValue.includes(normalizedQuery)) {
    return true;
  }

  let queryIndex = 0;
  for (const char of normalizedValue) {
    if (char === normalizedQuery[queryIndex]) {
      queryIndex += 1;
      if (queryIndex === normalizedQuery.length) {
        return true;
      }
    }
  }

  return false;
}

export function CommandPalette({ onNavigate, onCreateCI, onToggleDarkMode }: CommandPaletteProps) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [selectedIndex, setSelectedIndex] = useState(0);

  const commands = useMemo<CommandItem[]>(
    () => [
      {
        id: 'nav-dashboard',
        label: t('commandPalette.commands.dashboard'),
        keywords: ['dashboard overview'],
        action: () => onNavigate('dashboard'),
      },
      {
        id: 'nav-cmdb',
        label: t('commandPalette.commands.cmdb'),
        keywords: ['cmdb cis configuration items'],
        action: () => onNavigate('cmdb'),
      },
      {
        id: 'nav-topology',
        label: t('commandPalette.commands.topology'),
        keywords: ['topology topologie graph netzwerk'],
        action: () => onNavigate('topology'),
      },
      {
        id: 'nav-racks',
        label: t('commandPalette.commands.racks'),
        keywords: ['racks rack schrank he units'],
        action: () => onNavigate('racks'),
      },
      {
        id: 'nav-discovery',
        label: t('commandPalette.commands.discovery'),
        keywords: ['discovery collectors'],
        action: () => onNavigate('discovery'),
      },
      {
        id: 'create-ci',
        label: t('commandPalette.commands.createCI'),
        keywords: ['create ci new asset item'],
        action: onCreateCI,
      },
      {
        id: 'toggle-theme',
        label: t('commandPalette.commands.toggleDarkMode'),
        keywords: ['theme dark light appearance'],
        action: onToggleDarkMode,
      },
      {
        id: 'nav-assets',
        label: t('commandPalette.commands.assets', 'Inventar öffnen'),
        keywords: ['assets inventar hardware'],
        action: () => onNavigate('assets'),
      },
      {
        id: 'nav-assignments',
        label: t('commandPalette.commands.assignments', 'Zuweisungen öffnen'),
        keywords: ['assignments zuweisungen transfer'],
        action: () => onNavigate('assignments'),
      },
      {
        id: 'nav-documents',
        label: t('commandPalette.commands.documents', 'Dokumente öffnen'),
        keywords: ['documents dokumente files'],
        action: () => onNavigate('documents'),
      },
      {
        id: 'nav-stocktake',
        label: t('commandPalette.commands.stocktake', 'Inventur öffnen'),
        keywords: ['stocktake inventur scan'],
        action: () => onNavigate('stocktake'),
      },
      {
        id: 'nav-tickets',
        label: t('commandPalette.commands.tickets', 'Tickets öffnen'),
        keywords: ['tickets support helpdesk'],
        action: () => onNavigate('tickets'),
      },
      {
        id: 'nav-users',
        label: t('commandPalette.commands.users', 'Benutzer & Teams öffnen'),
        keywords: ['users teams roles benutzer rollen'],
        action: () => onNavigate('users'),
      },
    ],
    [onCreateCI, onNavigate, onToggleDarkMode, t],
  );

  const filteredCommands = useMemo(
    () =>
      commands.filter((command) =>
        fuzzyMatch(`${command.label} ${command.keywords.join(' ')}`, query),
      ),
    [commands, query],
  );

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        setOpen((current) => !current);
      }
    };

    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, []);

  useEffect(() => {
    if (open) {
      setQuery('');
      setSelectedIndex(0);
    }
  }, [open]);

  useEffect(() => {
    if (selectedIndex > filteredCommands.length - 1) {
      setSelectedIndex(0);
    }
  }, [filteredCommands.length, selectedIndex]);

  function handleSelect(command: CommandItem | undefined) {
    if (!command) {
      return;
    }

    command.action();
    setOpen(false);
  }

  function handleListNavigation(event: ReactKeyboardEvent<HTMLInputElement>) {
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      setSelectedIndex((current) =>
        filteredCommands.length === 0 ? 0 : (current + 1) % filteredCommands.length,
      );
      return;
    }

    if (event.key === 'ArrowUp') {
      event.preventDefault();
      setSelectedIndex((current) => {
        if (filteredCommands.length === 0) {
          return 0;
        }
        return current === 0 ? filteredCommands.length - 1 : current - 1;
      });
      return;
    }

    if (event.key === 'Enter') {
      event.preventDefault();
      handleSelect(filteredCommands[selectedIndex]);
      return;
    }

    if (event.key === 'Escape') {
      setOpen(false);
    }
  }

  return (
    <Dialog.Root open={open} onOpenChange={setOpen}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-gray-950/50 backdrop-blur-sm" />
        <Dialog.Content className="fixed left-1/2 top-[18vh] z-50 w-[min(92vw,36rem)] -translate-x-1/2 rounded-2xl border border-gray-200 bg-white shadow-2xl outline-none dark:border-gray-800 dark:bg-gray-900">
          <Dialog.Title className="sr-only">{t('commandPalette.title')}</Dialog.Title>
          <div className="border-b border-gray-200 p-3 dark:border-gray-800">
            <input
              autoFocus
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              onKeyDown={handleListNavigation}
              placeholder={t('commandPalette.searchPlaceholder')}
              aria-label={t('accessibility.commandPaletteSearch')}
              className="w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 outline-none focus:border-primary focus:ring-2 focus:ring-primary/20 dark:border-gray-700 dark:bg-gray-950 dark:text-gray-100"
            />
          </div>
          <div className="max-h-80 overflow-y-auto p-2">
            {filteredCommands.length === 0 ? (
              <p className="px-3 py-6 text-sm text-gray-500 dark:text-gray-400">
                {t('common.noResults')}
              </p>
            ) : (
              filteredCommands.map((command, index) => (
                <button
                  key={command.id}
                  type="button"
                  onMouseEnter={() => setSelectedIndex(index)}
                  onClick={() => handleSelect(command)}
                  className={cn(
                    'flex w-full items-center justify-between rounded-xl px-3 py-2 text-left text-sm transition-colors',
                    index === selectedIndex
                      ? 'bg-primary/10 text-primary dark:bg-primary/20'
                      : 'text-gray-700 hover:bg-gray-100 dark:text-gray-200 dark:hover:bg-gray-800',
                  )}
                >
                  <span>{command.label}</span>
                  <Badge variant="neutral">↵</Badge>
                </button>
              ))
            )}
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
