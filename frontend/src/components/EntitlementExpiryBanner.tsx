/**
 * EntitlementExpiryBanner — the dashboard banner of ENT-07 (WP-464): every
 * enabled feature of the tenant whose valid_until lies at most 14 days ahead
 * gets a notice with the feature and the date. The notifications 14, 7 and
 * 1 day ahead come from the server run entitlement.notices (WP-425); the
 * banner shows the whole window so it is visible from the first warning
 * until the end.
 */
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { entitlementApi } from '../api/client';
import type { Entitlement } from '../api/client';
import { useAuthStore } from '../auth/authStore';

const DAY_MS = 24 * 60 * 60 * 1000;

/** EXPIRY_WARNING_DAYS is the earliest warning of ENT-07 (14/7/1 days). */
export const EXPIRY_WARNING_DAYS = 14;

export interface ExpiringFeature {
  feature: string;
  validUntil: Date;
  /** Days until valid_until, rounded up as the server's days_left. */
  daysLeft: number;
}

/**
 * expiringFeatures returns the enabled features whose valid_until lies in
 * (now, now + 14 days], soonest first. Expired features are left out: they
 * are no longer active (ENT-07, collector paused).
 */
export function expiringFeatures(entitlements: Entitlement[], now: number): ExpiringFeature[] {
  const out: ExpiringFeature[] = [];
  for (const e of entitlements) {
    if (!e.enabled || !e.valid_until) continue;
    const until = new Date(e.valid_until);
    const left = until.getTime() - now;
    if (Number.isNaN(left) || left <= 0 || left > EXPIRY_WARNING_DAYS * DAY_MS) continue;
    out.push({ feature: e.feature_key, validUntil: until, daysLeft: Math.ceil(left / DAY_MS) });
  }
  return out.sort((a, b) => a.validUntil.getTime() - b.validUntil.getTime());
}

export function EntitlementExpiryBanner() {
  const { t, i18n } = useTranslation();
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated());
  const query = useQuery({
    queryKey: ['entitlements', 'expiry'],
    queryFn: async () => (await entitlementApi.list({ limit: 200 })).data ?? [],
    enabled: isAuthenticated,
    staleTime: 60_000,
  });

  const expiring = expiringFeatures(query.data ?? [], Date.now());
  if (expiring.length === 0) return null;

  const formatDate = new Intl.DateTimeFormat(i18n.language, { dateStyle: 'long' });
  return (
    <section
      aria-label={t('entitlementExpiry.title')}
      className="border-b border-amber-300 bg-amber-50 px-6 py-3 text-sm text-amber-900 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-100"
    >
      <ul className="flex flex-col gap-1">
        {expiring.map(({ feature, validUntil, daysLeft }) => {
          const values = {
            feature: t(`entitlementExpiry.features.${feature}`, { defaultValue: feature }),
            date: formatDate.format(validUntil),
            days: daysLeft,
          };
          return (
            <li key={feature} data-feature={feature}>
              {daysLeft === 1
                ? t('entitlementExpiry.messageOneDay', values)
                : t('entitlementExpiry.message', values)}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
