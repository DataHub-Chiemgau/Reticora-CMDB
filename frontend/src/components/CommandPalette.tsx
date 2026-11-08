import * as Dialog from '@radix-ui/react-dialog';
import { useEffect, useId, useMemo, useRef, useState } from 'react';
import type { KeyboardEvent as ReactKeyboardEvent } from 'react';
import { CornerDownLeft, Search } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useSearch } from '../api/hooks';
import type { SearchHit } from '../api/client';
import { navFeatureFor } from '../hooks/useEntitlements';
import { Kbd } from './ui/Kbd';
import { NAV_GROUPS } from './shell/navigation';
import { OPEN_PALETTE_EVENT } from './shell/paletteEvent';
import { Button } from './ui/Button';
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
  | 'users'
  | 'permissions'
  | 'slas'
  | 'forms'
  | 'workflows'
  | 'compliance'
  | 'iga'
  | 'assistant'
  | 'webhooks'
  | 'export'
  | 'monitoring'
  | 'audit'
  | 'security'
  | 'map'
  | 'roomplan'
  | 'consumables'
  | 'orders'
  | 'maintenance'
  | 'disposal'
  | 'keys'
  | 'trainings'
  | 'desks'
  | 'agents'
  | 'findings'
  | 'reviews'
  | 'jobs'
  // Enterprise CMDB + asset/inventory extension (Phase E).
  | 'ci-types'
  | 'rules'
  | 'relationship-types'
  | 'locations'
  | 'inventory'
  | 'saved-views'
  // Tag catalog (CI-08, WP-817): core without a module gate (E-105).
  | 'tags'
  // Import assistant (UI-11, WP-322): the nav page of the module import (E-105).
  | 'import'
  // Contracts and suppliers (MGT-12, WP-808): nav pages of the module inventory (E-105).
  | 'contracts'
  | 'suppliers';

export const pageToPath: Record<AppPage, string> = {
  dashboard: '/dashboard',
  cmdb: '/cmdb',
  topology: '/topology',
  racks: '/racks',
  discovery: '/discovery',
  assets: '/assets',
  assignments: '/assignments',
  documents: '/documents',
  stocktake: '/stocktake',
  tickets: '/tickets',
  users: '/users',
  permissions: '/permissions',
  slas: '/slas',
  forms: '/forms',
  workflows: '/workflows',
  compliance: '/compliance',
  iga: '/iga',
  assistant: '/assistant',
  webhooks: '/webhooks',
  export: '/export',
  monitoring: '/monitoring',
  audit: '/audit',
  security: '/security',
  map: '/map',
  roomplan: '/roomplan',
  consumables: '/consumables',
  orders: '/orders',
  maintenance: '/maintenance',
  disposal: '/disposal',
  keys: '/keys',
  trainings: '/trainings',
  desks: '/desks',
  agents: '/agents',
  findings: '/findings',
  reviews: '/reviews',
  jobs: '/jobs',
  'ci-types': '/ci-types',
  rules: '/rules',
  'relationship-types': '/relationship-types',
  locations: '/locations',
  inventory: '/inventory',
  'saved-views': '/saved-views',
  tags: '/tags',
  import: '/imports',
  contracts: '/contracts',
  suppliers: '/suppliers',
};

interface CommandPaletteProps {
  onNavigate: (page: AppPage) => void;
  onCreateCI: () => void;
  /**
   * onToggleDarkMode switches to the next theme mode, relative to the OS
   * theme: system -> opposite of the OS -> OS theme -> system.
   */
  onToggleDarkMode: () => void;
  /**
   * themeToggleLabel names the theme command with the current and the next
   * mode, as the header toggle does. Defaults to a generic label.
   */
  themeToggleLabel?: string;
  /**
   * isFeatureEnabled reports whether an entitlement-gated module is available.
   * Defaults to always-true so tests and ungated contexts keep full commands.
   */
  isFeatureEnabled?: (feature: string | undefined) => boolean;
  /**
   * isPageEnabled reports whether the release serves the page's module
   * (WP-214). Defaults to always-true like isFeatureEnabled.
   */
  isPageEnabled?: (page: AppPage) => boolean;
  /**
   * onScan opens the scan dialog (INV-03, WP-807). The caller passes it only
   * with the feature barcode and the module inventory; without it the
   * palette has no "Scan" command.
   */
  onScan?: () => void;
}

interface CommandItem {
  id: string;
  label: string;
  keywords: string[];
  action: () => void;
  /** A contact hit: selecting it shows the contact card instead of running action. */
  contact?: SearchHit;
}

/** The details of a contact hit the card shows below the name (metadata keys). */
const CONTACT_FIELDS = ['email', 'phone', 'role'] as const;

/**
 * ContactCard shows a contact search hit: contacts have no page of their own
 * in phase 1 (F-334), so the palette shows the name, e-mail, phone and role
 * the hit carries. It takes focus when it opens; "back" returns to the list.
 */
function ContactCard({ hit, onBack }: { hit: SearchHit; onBack: () => void }) {
  const { t } = useTranslation();
  const headingId = useId();
  const cardRef = useRef<HTMLElement>(null);
  useEffect(() => {
    cardRef.current?.focus();
  }, []);
  const link = (key: (typeof CONTACT_FIELDS)[number], value: string) => {
    const linkClass = 'text-accent underline-offset-2 hover:underline';
    if (key === 'email') {
      return (
        <a className={linkClass} href={`mailto:${value}`}>
          {value}
        </a>
      );
    }
    if (key === 'phone') {
      return (
        <a className={linkClass} href={`tel:${value.replace(/[^+\d]/g, '')}`}>
          {value}
        </a>
      );
    }
    return value;
  };
  return (
    <section ref={cardRef} tabIndex={-1} aria-labelledby={headingId} className="p-4 outline-none">
      <p className="text-xs font-medium text-fg-subtle">{t('commandPalette.contact.label')}</p>
      <h3 id={headingId} className="mt-1 text-lg font-semibold text-fg [overflow-wrap:anywhere]">
        {hit.title || '—'}
      </h3>
      <dl className="mt-3 space-y-2 text-sm">
        {CONTACT_FIELDS.map((key) => {
          const value = hit.metadata?.[key];
          return (
            <div key={key} className="grid grid-cols-[6rem_minmax(0,1fr)] gap-2">
              <dt className="text-fg-subtle">{t(`commandPalette.contact.${key}`)}</dt>
              <dd className="min-w-0 text-fg [overflow-wrap:anywhere]">
                {value ? link(key, value) : '—'}
              </dd>
            </div>
          );
        })}
      </dl>
      <div className="mt-4 flex justify-end">
        <Button variant="secondary" size="sm" onClick={onBack}>
          {t('commandPalette.contact.back')}
        </Button>
      </div>
    </section>
  );
}

/**
 * Where focus goes when the palette closes: back to the element that had it
 * before the palette opened, to the main content of the page a navigation
 * command opened, or nowhere (a dialog the command opened takes focus).
 */
type FocusAfterClose = 'restore' | 'main' | 'none';

/** The page's main landmark (App.tsx); a navigation command lands there. */
const MAIN_CONTENT_ID = 'main-content';

/** focusAfterCommand: navigation lands on the new page, create-ci and scan open a dialog. */
function focusAfterCommand(id: string): FocusAfterClose {
  if (id.startsWith('nav-')) {
    return 'main';
  }
  if (id === 'create-ci' || id === 'scan') {
    return 'none';
  }
  return 'restore';
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

export function CommandPalette({
  onNavigate,
  onCreateCI,
  onToggleDarkMode,
  themeToggleLabel,
  isFeatureEnabled = () => true,
  isPageEnabled = () => true,
  onScan,
}: CommandPaletteProps) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [selectedIndex, setSelectedIndex] = useState(0);
  // The contact hit whose card the palette shows instead of the list.
  const [contact, setContact] = useState<SearchHit | null>(null);
  const search = useSearch({ q: open ? query : '', limit: 5 });
  const listboxId = useId();
  const listRef = useRef<HTMLDivElement>(null);
  // Radix returns focus only to a Dialog.Trigger, and the palette has none:
  // it remembers the focused element itself when it opens and decides on
  // close where focus goes (UI-10, F-171).
  const openRef = useRef(false);
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const focusAfterCloseRef = useRef<FocusAfterClose>('restore');

  const commands = useMemo<CommandItem[]>(() => {
    const written: CommandItem[] = [
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
        label: themeToggleLabel ?? t('commandPalette.commands.toggleDarkMode'),
        keywords: ['theme dark light system appearance farbschema hell dunkel'],
        action: onToggleDarkMode,
      },
      ...(onScan
        ? [
            {
              id: 'scan',
              label: t('commandPalette.commands.scan'),
              keywords: ['scan scannen barcode qr code etikett label kamera camera'],
              action: onScan,
            },
          ]
        : []),
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
        id: 'nav-contracts',
        label: t('commandPalette.commands.contracts'),
        keywords: ['contracts verträge vertrag laufzeit kündigung expiry notice'],
        action: () => onNavigate('contracts'),
      },
      {
        id: 'nav-suppliers',
        label: t('commandPalette.commands.suppliers'),
        keywords: ['suppliers lieferanten dienstleister vendor'],
        action: () => onNavigate('suppliers'),
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
      {
        id: 'nav-permissions',
        label: t('commandPalette.commands.permissions'),
        keywords: ['permissions berechtigungen rbac abac'],
        action: () => onNavigate('permissions'),
      },
      {
        id: 'nav-slas',
        label: t('commandPalette.commands.slas'),
        keywords: ['sla service levels breach policies'],
        action: () => onNavigate('slas'),
      },
      {
        id: 'nav-forms',
        label: t('commandPalette.commands.forms'),
        keywords: ['forms formulare submissions'],
        action: () => onNavigate('forms'),
      },
      {
        id: 'nav-workflows',
        label: t('commandPalette.commands.workflows'),
        keywords: ['workflow automation approvals'],
        action: () => onNavigate('workflows'),
      },
      {
        id: 'nav-compliance',
        label: t('commandPalette.commands.compliance'),
        keywords: ['compliance iso nis2 score'],
        action: () => onNavigate('compliance'),
      },
      {
        id: 'nav-iga',
        label: t('commandPalette.commands.iga'),
        keywords: ['iga provisioning scim access reviews'],
        action: () => onNavigate('iga'),
      },
      {
        id: 'nav-assistant',
        label: t('commandPalette.commands.assistant'),
        keywords: ['assistant ki ai rag'],
        action: () => onNavigate('assistant'),
      },
      {
        id: 'nav-webhooks',
        label: t('commandPalette.commands.webhooks'),
        keywords: ['webhooks subscriptions deliveries dead letter'],
        action: () => onNavigate('webhooks'),
      },
      {
        id: 'nav-import',
        label: t('commandPalette.commands.import'),
        keywords: ['import csv xlsx excel upload mapping profil dry run'],
        action: () => onNavigate('import'),
      },
      {
        id: 'nav-export',
        label: t('commandPalette.commands.export'),
        keywords: ['export csv datev json download'],
        action: () => onNavigate('export'),
      },
      {
        id: 'nav-monitoring',
        label: t('commandPalette.commands.monitoring'),
        keywords: ['monitoring metrics alerts'],
        action: () => onNavigate('monitoring'),
      },
      {
        id: 'nav-audit',
        label: t('commandPalette.commands.audit', 'Audit-Protokoll öffnen'),
        keywords: ['audit protokoll hash chain compliance'],
        action: () => onNavigate('audit'),
      },
      {
        id: 'nav-security',
        label: t('commandPalette.commands.security', 'Sicherheit & DSGVO öffnen'),
        keywords: ['security sicherheit dsgvo gdpr privacy retention compliance report'],
        action: () => onNavigate('security'),
      },
      {
        id: 'nav-map',
        label: t('commandPalette.commands.map', 'Karte öffnen'),
        keywords: ['map karte gis standorte sites'],
        action: () => onNavigate('map'),
      },
      {
        id: 'nav-tags',
        label: t('commandPalette.commands.tags'),
        keywords: ['tags schlagwörter labels katalog catalog'],
        action: () => onNavigate('tags'),
      },
      {
        id: 'nav-roomplan',
        label: t('commandPalette.commands.roomplan', 'Raumplan öffnen'),
        keywords: ['roomplan raumplan floorplan raum position'],
        action: () => onNavigate('roomplan'),
      },
      {
        id: 'nav-rules',
        label: t('commandPalette.commands.rules'),
        keywords: ['rules regeln regelkatalog validation validierung bedingung condition'],
        action: () => onNavigate('rules'),
      },
    ];
    const writtenIds = new Set(written.map((command) => command.id));
    // Every page of the navigation is reachable from the keyboard: the pages
    // without a hand-written command above get a generic one.
    const generated: CommandItem[] = NAV_GROUPS.flatMap((group) => group.items)
      .filter(({ page }) => !writtenIds.has(`nav-${page}`))
      .map(({ page }) => ({
        id: `nav-${page}`,
        label: t('commandPalette.openPage', { page: t(`nav.${page}`) }),
        keywords: [page, t(`nav.${page}`)],
        action: () => onNavigate(page),
      }));
    return [...written, ...generated].filter((cmd) => {
      // Hide navigation commands for modules the tenant's plan does not
      // include, mirroring the gated main navigation.
      if (!cmd.id.startsWith('nav-')) return true;
      const page = cmd.id.slice(4) as AppPage;
      return isFeatureEnabled(navFeatureFor[page]) && isPageEnabled(page);
    });
  }, [
    onCreateCI,
    onScan,
    onNavigate,
    onToggleDarkMode,
    themeToggleLabel,
    t,
    isFeatureEnabled,
    isPageEnabled,
  ]);

  const filteredCommands = useMemo(() => {
    const local = commands.filter((command) =>
      fuzzyMatch(`${command.label} ${command.keywords.join(' ')}`, query),
    );
    const remote =
      search.data?.data.map((hit) => ({
        id: `search-${hit.entity_type}-${hit.entity_id}`,
        label: `${hit.title} · ${hit.entity_type}`,
        keywords: [hit.summary ?? '', hit.entity_type],
        // A contact has no page (empty url, F-334): it opens the card.
        contact: hit.entity_type === 'contact' ? hit : undefined,
        action: () => {
          if (hit.url) window.location.assign(hit.url);
        },
      })) ?? [];
    return [...remote, ...local];
  }, [commands, query, search.data?.data]);

  useEffect(() => {
    // Remember where focus was before the search field's autoFocus moves it.
    const rememberFocus = () => {
      if (!openRef.current) {
        returnFocusRef.current =
          document.activeElement instanceof HTMLElement && document.activeElement !== document.body
            ? document.activeElement
            : null;
        focusAfterCloseRef.current = 'restore';
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        rememberFocus();
        setOpen((current) => !current);
      }
    };
    // The header's search field opens the palette without a key press.
    const handleOpenRequest = () => {
      rememberFocus();
      setOpen(true);
    };

    window.addEventListener('keydown', handleKeyDown);
    window.addEventListener(OPEN_PALETTE_EVENT, handleOpenRequest);
    return () => {
      window.removeEventListener('keydown', handleKeyDown);
      window.removeEventListener(OPEN_PALETTE_EVENT, handleOpenRequest);
    };
  }, []);

  useEffect(() => {
    openRef.current = open;
    if (open) {
      setQuery('');
      setSelectedIndex(0);
      setContact(null);
    }
  }, [open]);

  useEffect(() => {
    if (selectedIndex > filteredCommands.length - 1) {
      setSelectedIndex(0);
    }
  }, [filteredCommands.length, selectedIndex]);

  // Keep the keyboard-selected option visible in the scrollable list.
  useEffect(() => {
    const option = listRef.current?.querySelector<HTMLElement>('[aria-selected="true"]');
    option?.scrollIntoView?.({ block: 'nearest' });
  }, [selectedIndex, open]);

  const optionId = (index: number) => `${listboxId}-option-${index}`;

  function handleSelect(command: CommandItem | undefined) {
    if (!command) {
      return;
    }
    if (command.contact) {
      // The palette stays open and shows the contact.
      setContact(command.contact);
      return;
    }

    focusAfterCloseRef.current = focusAfterCommand(command.id);
    command.action();
    setOpen(false);
  }

  function handleCloseAutoFocus(event: Event) {
    const mode = focusAfterCloseRef.current;
    const previous = returnFocusRef.current;
    focusAfterCloseRef.current = 'restore';
    returnFocusRef.current = null;
    if (mode === 'none') {
      event.preventDefault();
      return;
    }
    const main = mode === 'main' ? document.getElementById(MAIN_CONTENT_ID) : null;
    const target = main ?? (previous && previous.isConnected ? previous : null);
    if (target) {
      event.preventDefault();
      target.focus();
    }
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

    if (event.key === 'Home' || event.key === 'End') {
      if (filteredCommands.length === 0) {
        return;
      }
      event.preventDefault();
      setSelectedIndex(event.key === 'Home' ? 0 : filteredCommands.length - 1);
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
        <Dialog.Overlay className="fixed inset-0 z-40 animate-fade-in bg-gray-950/55 backdrop-blur-[2px]" />
        {/* Portaled under <body>, outside the App shell: the content sets the
            theme text colour itself instead of inheriting it from the shell. */}
        <Dialog.Content
          onCloseAutoFocus={handleCloseAutoFocus}
          className="fixed left-1/2 top-[14vh] z-50 w-[min(94vw,40rem)] -translate-x-1/2 animate-pop-in-top overflow-hidden rounded-xl border border-line bg-raised text-gray-900 shadow-2xl outline-none dark:text-gray-100"
        >
          <Dialog.Title className="sr-only">{t('commandPalette.title')}</Dialog.Title>
          {contact ? (
            <ContactCard hit={contact} onBack={() => setContact(null)} />
          ) : (
            <>
              <div className="flex items-center gap-3 border-b border-line px-4">
                <Search aria-hidden="true" className="h-4 w-4 shrink-0 text-fg-subtle" />
                <input
                  autoFocus
                  role="combobox"
                  aria-expanded={filteredCommands.length > 0}
                  aria-controls={listboxId}
                  aria-autocomplete="list"
                  aria-activedescendant={
                    filteredCommands.length > 0 ? optionId(selectedIndex) : undefined
                  }
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  onKeyDown={handleListNavigation}
                  placeholder={t('commandPalette.searchPlaceholder')}
                  aria-label={t('accessibility.commandPaletteSearch')}
                  className="h-12 w-full bg-transparent text-[15px] text-fg outline-none placeholder:text-fg-subtle"
                />
                <Kbd>Esc</Kbd>
              </div>
              <div
                ref={listRef}
                id={listboxId}
                // Without options the container only shows the "no results" note.
                role={filteredCommands.length > 0 ? 'listbox' : undefined}
                aria-label={filteredCommands.length > 0 ? t('commandPalette.title') : undefined}
                className="scroll-thin max-h-80 overflow-y-auto p-2"
              >
                {filteredCommands.length === 0 ? (
                  <p className="px-3 py-8 text-center text-sm text-fg-muted">
                    {t('common.noResults')}
                  </p>
                ) : (
                  filteredCommands.map((command, index) => (
                    // Options stay out of the Tab order: focus remains in the
                    // search field and the arrow keys move the active option
                    // (aria-activedescendant), so Tab only cycles between the
                    // dialog's controls.
                    <button
                      key={command.id}
                      id={optionId(index)}
                      type="button"
                      role="option"
                      aria-selected={index === selectedIndex}
                      tabIndex={-1}
                      onMouseEnter={() => setSelectedIndex(index)}
                      onClick={() => handleSelect(command)}
                      className={cn(
                        'flex h-9 w-full items-center justify-between gap-3 rounded-lg px-3 text-left text-sm transition-colors',
                        index === selectedIndex
                          ? 'bg-accent-soft text-accent-strong'
                          : 'text-fg-muted',
                      )}
                    >
                      <span className="truncate">{command.label}</span>
                      {index === selectedIndex ? (
                        <CornerDownLeft aria-hidden="true" className="h-3.5 w-3.5 shrink-0" />
                      ) : null}
                    </button>
                  ))
                )}
              </div>
              <div
                aria-hidden="true"
                className="flex items-center gap-4 border-t border-line bg-canvas px-4 py-2 text-xs text-fg-subtle"
              >
                <span className="inline-flex items-center gap-1.5">
                  <Kbd>↑</Kbd>
                  <Kbd>↓</Kbd>
                  {t('commandPalette.hintNavigate')}
                </span>
                <span className="inline-flex items-center gap-1.5">
                  <Kbd>↵</Kbd>
                  {t('commandPalette.hintOpen')}
                </span>
                <span className="inline-flex items-center gap-1.5">
                  <Kbd>Esc</Kbd>
                  {t('commandPalette.hintClose')}
                </span>
              </div>
            </>
          )}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
