/**
 * IpamTabList is the tab bar of the IPAM page (WP-918): subnets, VLANs,
 * VRFs and nameserver sets. The arrow keys move between the tabs (roving
 * tabindex); the panels carry the ids ipam-panel-<tab>.
 */
import { useRef, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { cn } from '../ui/utils';

export const IPAM_TABS = ['subnets', 'vlans', 'vrfs', 'nameservers'] as const;
export type IpamTab = (typeof IPAM_TABS)[number];

export function IpamTabList({ tab, onChange }: { tab: IpamTab; onChange: (tab: IpamTab) => void }) {
  const { t } = useTranslation();
  const refs = useRef<Partial<Record<IpamTab, HTMLButtonElement | null>>>({});
  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const step = event.key === 'ArrowRight' ? 1 : event.key === 'ArrowLeft' ? -1 : 0;
    if (step === 0) return;
    event.preventDefault();
    const next = IPAM_TABS[(index + step + IPAM_TABS.length) % IPAM_TABS.length];
    if (!next) return;
    onChange(next);
    refs.current[next]?.focus();
  };
  return (
    <div
      role="tablist"
      aria-label={t('ipam.tabs.label')}
      className="flex gap-1 border-b border-line"
    >
      {IPAM_TABS.map((id, index) => (
        <button
          key={id}
          ref={(node) => {
            refs.current[id] = node;
          }}
          type="button"
          role="tab"
          id={`ipam-tab-${id}`}
          aria-selected={tab === id}
          aria-controls={`ipam-panel-${id}`}
          tabIndex={tab === id ? 0 : -1}
          onClick={() => onChange(id)}
          onKeyDown={(event) => onKeyDown(event, index)}
          className={cn(
            '-mb-px border-b-2 px-3 py-2 text-sm font-medium focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent',
            tab === id ? 'border-accent text-fg' : 'border-transparent text-fg-muted hover:text-fg',
          )}
        >
          {t(`ipam.tabs.${id}`)}
        </button>
      ))}
    </div>
  );
}
