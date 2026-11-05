import * as DropdownMenu from '@radix-ui/react-dropdown-menu';
import { Check, LogOut } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { UI_LANGUAGES, currentLanguage, setLanguage } from '../../i18n';
import type { UILanguage } from '../../i18n';

function initialsOf(name: string): string {
  const parts = name
    .trim()
    .split(/[\s@.]+/)
    .filter(Boolean);
  const letters = (parts.length > 1 ? [parts[0], parts[parts.length - 1]] : [parts[0]])
    .map((part) => part?.[0] ?? '')
    .join('');
  return (letters || '?').toUpperCase();
}

const itemClasses =
  'flex h-8 cursor-default select-none items-center gap-2 rounded-md px-2 text-sm text-fg outline-none data-[highlighted]:bg-fg/[0.07]';

/**
 * UserMenu is the account control: who is signed in, the interface language
 * and sign-out. Appearance stays a direct header control (ThemeToggle).
 */
export function UserMenu({
  name,
  email,
  onSignOut,
}: {
  name?: string;
  email?: string;
  onSignOut: () => void;
}) {
  const { t } = useTranslation();
  const display = name || email || t('shell.account');
  const language = currentLanguage();

  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button
          type="button"
          aria-label={t('shell.accountMenu', { name: display })}
          className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-accent-soft text-xs font-semibold text-accent-strong ring-1 ring-inset ring-accent/25 transition-colors hover:ring-accent/50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent data-[state=open]:ring-accent/60"
        >
          {initialsOf(display)}
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          align="end"
          sideOffset={8}
          className="z-[65] w-60 animate-menu-in rounded-xl border border-line bg-raised p-1.5 text-gray-900 shadow-lg dark:text-gray-100"
        >
          <div className="px-2 pb-2 pt-1.5">
            <p className="truncate text-sm font-medium text-fg">{display}</p>
            {email && email !== display ? (
              <p className="truncate text-xs text-fg-muted">{email}</p>
            ) : null}
          </div>
          <DropdownMenu.Separator className="-mx-1.5 my-1 h-px bg-line" />
          <DropdownMenu.Label className="px-2 pb-1 pt-1.5 text-xs font-medium text-fg-subtle">
            {t('language.label')}
          </DropdownMenu.Label>
          <DropdownMenu.RadioGroup
            value={language}
            onValueChange={(value) => void setLanguage(value as UILanguage)}
          >
            {UI_LANGUAGES.map((value) => (
              <DropdownMenu.RadioItem key={value} value={value} className={itemClasses}>
                <span className="flex-1">{t(`language.${value}`)}</span>
                <DropdownMenu.ItemIndicator>
                  <Check aria-hidden="true" className="h-4 w-4 text-accent" />
                </DropdownMenu.ItemIndicator>
              </DropdownMenu.RadioItem>
            ))}
          </DropdownMenu.RadioGroup>
          <DropdownMenu.Separator className="-mx-1.5 my-1 h-px bg-line" />
          <DropdownMenu.Item onSelect={onSignOut} className={itemClasses}>
            <LogOut aria-hidden="true" className="h-4 w-4 text-fg-muted" />
            {t('auth.logout')}
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}
