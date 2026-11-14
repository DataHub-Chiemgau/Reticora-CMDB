import { useQuery } from '@tanstack/react-query';
import { ArrowRight, CheckCircle2, Circle } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { collectorApi, credentialApi, siteApi } from '../../api/client';
import { useClients } from '../../api/cmdbHooks';
import { useCIList, useEffectivePermissions } from '../../api/hooks';
import { useAuthStore } from '../../auth/authStore';
import { hasPermission, useNavAccess } from '../../hooks/useEntitlements';
import { useDashboardStore } from '../../stores/dashboard';
import type { AppPage } from '../CommandPalette';
import { Button } from '../ui/Button';
import { cn } from '../ui/utils';

/** The steps of the setup in their order (WP-857). */
export type SetupStep = 'client' | 'site' | 'credential' | 'collector' | 'cis';

interface StepDef {
  step: SetupStep;
  /** The page whose rule (navPageState) the step follows. */
  page: AppPage;
  /** The right the read of the step's list needs (route_permissions.txt). */
  permission: string;
  /** Where the step is done. */
  to: string;
}

// Credentials live on the discovery page (WP-856); a collector is enrolled
// in the onboarding, where the dashboard's setup banner leads as well.
const STEPS: StepDef[] = [
  { step: 'client', page: 'clients', permission: 'location:read', to: '/clients' },
  { step: 'site', page: 'locations', permission: 'location:read', to: '/locations' },
  { step: 'credential', page: 'discovery', permission: 'credential:read', to: '/discovery' },
  { step: 'collector', page: 'discovery', permission: 'discovery:read', to: '/onboarding' },
  { step: 'cis', page: 'cmdb', permission: 'ci:read', to: '/cmdb' },
];

/** The slug of the client every organization receives (WP-854). */
const DEFAULT_CLIENT_SLUG = 'default';

/** setupUserKey names the signed-in user for the per-user choice to hide the list. */
export function setupUserKey(sub: string | undefined): string {
  return sub ?? 'anonymous';
}

/**
 * useSetupSteps tells which steps apply to the user and which are done,
 * derived from the lists the API already has (no endpoint of its own): a
 * step applies when the user holds the right to read its list and its page
 * is shown (navPageState); it is done when the list has an entry. A list
 * that is loading or failed counts as not done.
 */
export function useSetupSteps() {
  const permissions = useEffectivePermissions().data?.permissions;
  const { stateOf } = useNavAccess();
  const applies = (def: StepDef) =>
    hasPermission(permissions, def.permission) && stateOf(def.page) === 'visible';
  const steps = STEPS.filter(applies);
  const has = (step: SetupStep) => steps.some((def) => def.step === step);

  // The client list is the one the selects read (one page of 200): every
  // organization has its default client, so a client of its own is one
  // with another slug.
  const clients = useClients(has('client'));
  const sites = useQuery({
    queryKey: ['sites', 'setup'],
    queryFn: () => siteApi.list({ limit: 1 }),
    enabled: has('site'),
  });
  const credentials = useQuery({
    queryKey: ['credentials', 'setup'],
    queryFn: () => credentialApi.list({ limit: 1 }),
    enabled: has('credential'),
    retry: false,
  });
  // The same request as the setup banner's (useCollectors).
  const collectors = useQuery({
    queryKey: ['collectors', { limit: 1, offset: 0 }],
    queryFn: () => collectorApi.list({ limit: 1, offset: 0 }),
    enabled: has('collector'),
  });
  const cis = useCIList({ limit: 1, offset: 0 }, { enabled: has('cis') });

  const done: Record<SetupStep, boolean> = {
    client: (clients.data?.data ?? []).some((client) => client.slug !== DEFAULT_CLIENT_SLUG),
    site: (sites.data?.total ?? 0) > 0,
    credential: (credentials.data?.total ?? 0) > 0,
    collector: (collectors.data?.total ?? 0) > 0,
    cis: (cis.data?.total ?? 0) > 0,
  };
  return steps.map((def) => ({ ...def, done: done[def.step] }));
}

/**
 * SetupChecklist is the entry of a new organization on the dashboard
 * (WP-857): customer, site, credentials, collector and first CIs, each with
 * a link to where it is done and ticked off from the existing data. Steps
 * without the user's right are left out; the user can hide the list.
 */
export function SetupChecklist() {
  const { t } = useTranslation();
  const user = setupUserKey(useAuthStore((state) => state.user?.sub));
  const hidden = useDashboardStore((state) => state.setupHiddenFor.includes(user));
  const setSetupHidden = useDashboardStore((state) => state.setSetupHidden);
  const steps = useSetupSteps();
  if (hidden || steps.length === 0) {
    return null;
  }
  const doneCount = steps.filter((step) => step.done).length;

  return (
    <section
      aria-labelledby="dashboard-setup-checklist"
      className="rounded-xl border border-line bg-panel p-5"
    >
      <div className="mb-3 flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="dashboard-setup-checklist" className="text-sm font-semibold text-fg">
            {t('discoverability.setup.title')}
          </h2>
          <p className="mt-0.5 text-sm text-fg-muted">
            {t('discoverability.setup.progress', { done: doneCount, total: steps.length })}
          </p>
        </div>
        <Button variant="ghost" size="sm" onClick={() => setSetupHidden(user, true)}>
          {t('discoverability.setup.hide')}
        </Button>
      </div>
      <ol className="divide-y divide-line">
        {steps.map(({ step, to, done }) => (
          <li key={step} className="flex items-center gap-3 py-2">
            {done ? (
              <CheckCircle2
                aria-hidden="true"
                className="h-4 w-4 shrink-0 text-green-600 dark:text-green-400"
              />
            ) : (
              <Circle aria-hidden="true" className="h-4 w-4 shrink-0 text-fg-subtle" />
            )}
            <Link
              to={to}
              className={cn(
                'inline-flex items-center gap-1 text-sm font-medium hover:underline',
                done ? 'text-fg-muted' : 'text-accent',
              )}
            >
              {t(`discoverability.setup.steps.${step}`)}
              <ArrowRight aria-hidden="true" className="h-3.5 w-3.5" />
            </Link>
            <span className="ml-auto text-xs text-fg-subtle">
              {done ? t('discoverability.setup.done') : t('discoverability.setup.open')}
            </span>
          </li>
        ))}
      </ol>
    </section>
  );
}
