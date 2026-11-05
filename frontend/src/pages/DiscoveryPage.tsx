import {
  Check,
  Copy,
  Pencil,
  Plug,
  Plus,
  Power,
  PowerOff,
  Radar,
  Trash2,
  TriangleAlert,
} from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import {
  useCollectors,
  useCredentials,
  useDiscoveryScopeMutations,
  useDiscoveryScopes,
  useHasPermission,
  useResolveScopeSuggestion,
  useScopeSuggestions,
  useSites,
  useVrfs,
} from '../api/hooks';
import type {
  Collector,
  DiscoveryPlugin,
  DiscoveryScope,
  DiscoveryScopeHealth,
  DiscoveryScopeKind,
} from '../api/client';
import { ApiError, fetchAPI } from '../api/client';
import {
  EnrollmentClientSelect,
  describeEnrollmentRefusal,
  useEnrollmentClientChoice,
} from '../components/EnrollmentClientSelect';
import { ScrollRegion } from '../components/ScrollRegion';
import { Panel } from '../components/spatial/Panel';
import { Badge } from '../components/ui/Badge';
import { Button, IconButton } from '../components/ui/Button';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';

interface EnrollmentCodeResponse {
  id: string;
  code: string;
  label: string;
  expires_at: string;
}

function createEnrollmentCode(label: string, clientId: string): Promise<EnrollmentCodeResponse> {
  return fetchAPI('/collectors/enrollment-codes', {
    method: 'POST',
    body: JSON.stringify({ label, ...(clientId ? { client_id: clientId } : {}) }),
  });
}

/**
 * EnrollWizard guides connecting a collector: mint a code, show the command.
 * A user restricted to several clients chooses the collector's client first
 * (F-425); a refusal shows the server's reason.
 */
function EnrollWizard({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [label, setLabel] = useState('');
  const [clientId, setClientId] = useState('');
  const [code, setCode] = useState<EnrollmentCodeResponse | null>(null);
  const [copied, setCopied] = useState(false);
  const choice = useEnrollmentClientChoice(open);

  const mint = useMutation({
    mutationFn: () => createEnrollmentCode(label || 'collector', choice.required ? clientId : ''),
    onSuccess: (data) => {
      setCode(data);
      queryClient.invalidateQueries({ queryKey: ['collectors'] });
    },
  });

  const refusal = describeEnrollmentRefusal(mint.error, t, choice.required);

  const enrollCommand = code
    ? `reticora-collector enroll --server ${window.location.origin} --code ${code.code}`
    : '';

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(enrollCommand);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      /* clipboard unavailable */
    }
  };

  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        if (!o) {
          setCode(null);
          setLabel('');
          setClientId('');
          mint.reset();
          onClose();
        }
      }}
      title={t('discovery.enroll.title', 'Collector verbinden')}
    >
      {!code ? (
        <div className="space-y-4">
          <p className="text-sm text-fg-muted">
            {t(
              'discovery.enroll.intro',
              'Schließen Sie einen Collector an: Erzeugen Sie einen einmaligen Enrollment-Code und geben Sie ihn im Collector ein. Der Collector muss den Server über Port 4222/TCP erreichen. Die Subnetze, die er meldet, erscheinen hier als Vorschläge; erst bestätigte Subnetze scannt er.',
            )}
          </p>
          <Input
            label={t('discovery.enroll.label', 'Bezeichnung (z. B. Standort)')}
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            placeholder="site-a"
          />
          <EnrollmentClientSelect
            choice={choice}
            value={clientId}
            onChange={setClientId}
            error={refusal.client}
          />
          {refusal.message ? (
            <p role="alert" className="text-sm text-danger">
              {refusal.message}
            </p>
          ) : null}
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={onClose}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button
              onClick={() => mint.mutate()}
              disabled={mint.isPending || (choice.required && !clientId)}
            >
              {mint.isPending ? t('app.loading') : t('discovery.enroll.generate', 'Code erzeugen')}
            </Button>
          </div>
        </div>
      ) : (
        <div className="space-y-4">
          <p className="text-sm text-fg-muted">
            {t(
              'discovery.enroll.once',
              'Dieser Code wird nur einmal angezeigt und läuft ab. Führen Sie im Kundennetz aus:',
            )}
          </p>
          <pre
            tabIndex={0}
            className="overflow-x-auto rounded-lg border border-line bg-canvas p-3 font-mono text-xs text-fg focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
          >
            {enrollCommand}
          </pre>
          <p className="text-xs text-fg-muted">
            {t('discovery.enroll.expires', 'Gültig bis')}:{' '}
            {new Date(code.expires_at).toLocaleString()}
          </p>
          <p className="text-sm text-fg-muted">{t('discovery.enroll.after')}</p>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" icon={copied ? <Check /> : <Copy />} onClick={copy}>
              {copied ? t('common.copied', 'Kopiert!') : t('common.copy', 'Kopieren')}
            </Button>
            <Button
              onClick={() => {
                setCode(null);
                setLabel('');
                onClose();
              }}
            >
              {t('common.done', 'Fertig')}
            </Button>
          </div>
        </div>
      )}
    </Modal>
  );
}

function formatHeartbeat(value: string | undefined, language: string) {
  if (!value) {
    return '—';
  }

  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }

  return new Intl.DateTimeFormat(language, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(date);
}

function getCollectorVariant(
  status: string,
): 'success' | 'warning' | 'danger' | 'neutral' | 'info' {
  switch (status) {
    case 'active':
      return 'success';
    case 'offline':
      return 'danger';
    case 'paused':
      return 'warning';
    case 'pending':
      return 'info';
    default:
      return 'neutral';
  }
}

const healthVariant: Record<
  DiscoveryScopeHealth['state'],
  'success' | 'warning' | 'danger' | 'neutral'
> = {
  ok: 'success',
  partial: 'warning',
  failed: 'danger',
  never_run: 'neutral',
};

/** ScopeHealthBadge shows the state of the last run of a scope (DIS-08). */
function ScopeHealthBadge({ health }: { health?: DiscoveryScopeHealth }) {
  const { t } = useTranslation();
  const state = health?.state ?? 'never_run';
  return <Badge variant={healthVariant[state]}>{t(`discovery.scopes.health.${state}`)}</Badge>;
}

/** ScopeErrors lists the failures of the last run with their causes. */
function ScopeErrors({ health }: { health?: DiscoveryScopeHealth }) {
  const { t } = useTranslation();
  if (!health?.errors?.length) return null;
  return (
    <ul className="space-y-1 text-xs text-red-700 dark:text-red-300">
      {health.errors.map((err, i) => (
        <li key={`${err.protocol}-${err.target}-${i}`}>
          <span className="font-medium">{t(`discovery.scopes.cause.${err.cause}`, err.cause)}</span>
          {err.protocol ? ` · ${err.protocol}` : ''}
          {err.target ? ` · ${err.target}` : ''}
          {err.message ? <span className="text-fg-muted"> — {err.message}</span> : null}
        </li>
      ))}
    </ul>
  );
}

/**
 * describeActionError names why a scope action failed: a missing right, a
 * scope that is gone, or the reason the server gave (F-267).
 */
function describeActionError(error: unknown, t: TFunction): string {
  if (error instanceof ApiError) {
    if (error.status === 403) return t('discovery.scopes.errors.forbidden');
    if (error.status === 404) return t('discovery.scopes.errors.notFound');
    return t('discovery.scopes.errors.reason', { detail: error.detail || error.message });
  }
  return t('discovery.scopes.errors.reason', {
    detail: error instanceof Error ? error.message : String(error),
  });
}

/**
 * ScopeSuggestions lists the subnets the collectors reported after their
 * enrollment; confirming one creates the scope (INS-06, DIS-09). Accepting
 * and dismissing need review:resolve and discovery:manage (F-031); a failed
 * resolve says why (F-267).
 */
function ScopeSuggestions({ canResolve }: { canResolve: boolean }) {
  const { t } = useTranslation();
  const { data } = useScopeSuggestions();
  const resolve = useResolveScopeSuggestion();
  const [error, setError] = useState<string | null>(null);
  const items = data?.data ?? [];
  if (items.length === 0) return null;
  const act = (id: string, action: 'accept' | 'dismiss') => {
    setError(null);
    resolve.mutate(
      { id, action },
      {
        onError: (err) =>
          setError(t('discovery.suggestions.failed', { reason: describeActionError(err, t) })),
      },
    );
  };
  return (
    <Panel title={t('discovery.suggestions.title')} leading={<Radar className="text-accent" />}>
      <p className="mb-3 text-sm text-fg-muted">{t('discovery.suggestions.intro')}</p>
      {error ? (
        <p role="alert" className="mb-3 text-sm text-danger">
          {error}
        </p>
      ) : null}
      <ul className="-my-1.5 divide-y divide-line text-sm">
        {items.map((item) => (
          <li key={item.id} className="flex flex-wrap items-center justify-between gap-2 py-2.5">
            <span className="text-fg">
              <span className="font-mono">{item.payload.cidr}</span>{' '}
              <span className="text-fg-muted">
                {t('discovery.suggestions.from', {
                  collector: item.payload.collector_name ?? item.payload.collector_id ?? '',
                })}
              </span>
            </span>
            {canResolve ? (
              <span className="flex gap-2">
                <Button
                  size="sm"
                  variant="secondary"
                  icon={<Check />}
                  disabled={resolve.isPending}
                  onClick={() => act(item.id, 'accept')}
                >
                  {t('discovery.suggestions.accept')}
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={resolve.isPending}
                  onClick={() => act(item.id, 'dismiss')}
                >
                  {t('discovery.suggestions.dismiss')}
                </Button>
              </span>
            ) : null}
          </li>
        ))}
      </ul>
    </Panel>
  );
}

/** The plugins a scope can run (DiscoveryScope.plugins in api/openapi.yaml). */
export const DISCOVERY_PLUGINS: readonly DiscoveryPlugin[] = [
  'sweep',
  'snmp',
  'ssh',
  'wmi',
  'ipmi',
  'redfish',
  'power',
  'nas',
];

/** The plugins of a new scope, as the server defaults them. */
const defaultPlugins: DiscoveryPlugin[] = ['sweep', 'snmp'];

const scopeKinds: readonly DiscoveryScopeKind[] = ['cidr', 'range', 'host'];

type ScopeField =
  'name' | 'kind' | 'target' | 'site_id' | 'vrf_id' | 'schedule' | 'credential_ids' | 'plugins';

const scopeFields: readonly ScopeField[] = [
  'name',
  'kind',
  'target',
  'site_id',
  'vrf_id',
  'schedule',
  'credential_ids',
  'plugins',
];

interface ScopeFormValues {
  name: string;
  kind: DiscoveryScopeKind;
  target: string;
  site_id: string;
  vrf_id: string;
  schedule: string;
  credential_ids: string[];
  plugins: string[];
}

/** A refusal as the form shows it: a summary and per-field messages. */
interface ScopeRefusal {
  message: string;
  fields: Partial<Record<ScopeField, string>>;
}

/**
 * describeScopeRefusal turns a failed save into a localized message; a
 * validation error (422 naming the field, DIS-09) goes to its field (F-107).
 */
function describeScopeRefusal(error: unknown, t: TFunction): ScopeRefusal {
  if (!(error instanceof ApiError) || (error.status !== 400 && error.status !== 422)) {
    return {
      message: t('discovery.scopes.errors.saveFailed', { reason: describeActionError(error, t) }),
      fields: {},
    };
  }
  const detail = error.detail ?? error.message;
  const byField: Record<string, string> = error.violationsByField();
  if (error.field) {
    // A FieldProblem's detail is "<field>: <message>".
    const prefix = `${error.field}: `;
    byField[error.field] = detail.startsWith(prefix) ? detail.slice(prefix.length) : detail;
  }
  const fields: Partial<Record<ScopeField, string>> = {};
  const others: string[] = [];
  for (const [field, message] of Object.entries(byField)) {
    if ((scopeFields as readonly string[]).includes(field)) {
      fields[field as ScopeField] = t('discovery.scopes.errors.invalidValue', {
        detail: message || field,
      });
    } else {
      others.push(`${field}: ${message}`);
    }
  }
  if (Object.keys(fields).length === 0 && others.length === 0) {
    others.push(detail);
  }
  return {
    message: others.length
      ? t('discovery.scopes.errors.rejected', { detail: others.join('; ') })
      : t('discovery.scopes.errors.notSaved'),
    fields,
  };
}

function initialValues(scope?: DiscoveryScope): ScopeFormValues {
  return {
    name: scope?.name ?? '',
    kind: scope?.kind ?? 'cidr',
    target: scope?.target ?? '',
    site_id: scope?.site_id ?? '',
    vrf_id: scope?.vrf_id ?? '',
    schedule: scope?.schedule ?? '24h',
    credential_ids: scope?.credential_ids ?? [],
    plugins: scope?.plugins ?? [...defaultPlugins],
  };
}

function toggled(list: string[], value: string): string[] {
  return list.includes(value) ? list.filter((v) => v !== value) : [...list, value];
}

/**
 * ScopeForm creates or edits a scope with the fields of DIS-09: name, kind,
 * target, site, VRF, plugins, credentials and schedule (F-107). Without a
 * credential the credentials of the scope's client and the
 * organization-wide ones apply (E-67); a cron schedule runs in the time zone
 * of the organization (E-84). A refused field shows the server's reason.
 */
function ScopeForm({
  collector,
  scope,
  onClose,
}: {
  collector: Collector;
  scope?: DiscoveryScope;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const { create, update } = useDiscoveryScopeMutations(collector.id);
  const sites = useSites(collector.client_id);
  const vrfs = useVrfs();
  const credentials = useCredentials();
  const [values, setValues] = useState<ScopeFormValues>(() => initialValues(scope));
  const [refusal, setRefusal] = useState<ScopeRefusal | null>(null);
  const pending = create.isPending || update.isPending;

  const set = <K extends keyof ScopeFormValues>(key: K, value: ScopeFormValues[K]) =>
    setValues((v) => ({ ...v, [key]: value }));

  const siteList = sites.data?.data ?? [];
  const site = siteList.find((s) => s.id === values.site_id);
  // A scope may use the credentials of its client and the organization-wide
  // ones; the site decides the client, else the collector (E-67). Selected
  // credentials stay visible so that they can be removed.
  const clientId = site?.client_id ?? (values.site_id ? scope?.client_id : collector.client_id);
  const credentialList = (credentials.data?.data ?? []).filter(
    (c) => !c.client_id || c.client_id === clientId || values.credential_ids.includes(c.id),
  );

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const fields: Partial<Record<ScopeField, string>> = {};
    if (!values.target.trim()) fields.target = t('discovery.scopes.form.targetRequired');
    if (values.plugins.length === 0) fields.plugins = t('discovery.scopes.form.pluginsRequired');
    if (Object.keys(fields).length > 0) {
      setRefusal({ message: t('discovery.scopes.errors.notSaved'), fields });
      return;
    }
    setRefusal(null);
    const onError = (error: unknown) => setRefusal(describeScopeRefusal(error, t));
    const common = {
      kind: values.kind,
      target: values.target.trim(),
      schedule: values.schedule.trim() || undefined,
      credential_ids: values.credential_ids,
      plugins: values.plugins,
    };
    if (scope) {
      // An empty site or VRF removes it from the scope.
      update.mutate(
        {
          id: scope.id,
          patch: {
            ...common,
            name: values.name.trim(),
            site_id: values.site_id,
            vrf_id: values.vrf_id,
          },
        },
        { onSuccess: onClose, onError },
      );
      return;
    }
    create.mutate(
      {
        collector_id: collector.id,
        ...common,
        name: values.name.trim() || undefined,
        site_id: values.site_id || undefined,
        vrf_id: values.vrf_id || undefined,
      },
      { onSuccess: onClose, onError },
    );
  };

  const fieldError = (field: ScopeField) => refusal?.fields[field];

  return (
    <form className="space-y-5" onSubmit={submit} noValidate>
      <Input
        label={t('discovery.scopes.form.name')}
        placeholder={t('discovery.scopes.form.namePlaceholder')}
        value={values.name}
        onChange={(e) => set('name', e.target.value)}
        error={fieldError('name')}
      />
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <Select
          label={t('discovery.scopes.kindLabel')}
          value={values.kind}
          onChange={(e) => set('kind', e.target.value as DiscoveryScopeKind)}
          options={scopeKinds.map((k) => ({ value: k, label: t(`discovery.scopes.kind.${k}`) }))}
          error={fieldError('kind')}
        />
        <Input
          label={t('discovery.scopes.target')}
          placeholder={t('discovery.scopes.targetPlaceholder')}
          value={values.target}
          onChange={(e) => set('target', e.target.value)}
          error={fieldError('target')}
          className="font-mono"
        />
        <Select
          label={t('discovery.scopes.form.site')}
          value={values.site_id}
          onChange={(e) => set('site_id', e.target.value)}
          options={[
            { value: '', label: t('discovery.scopes.form.siteNone') },
            ...siteList.map((s) => ({ value: s.id, label: s.name })),
          ]}
          error={fieldError('site_id')}
        />
        <Select
          label={t('discovery.scopes.form.vrf')}
          value={values.vrf_id}
          onChange={(e) => set('vrf_id', e.target.value)}
          options={[
            { value: '', label: t('discovery.scopes.form.vrfNone') },
            ...(vrfs.data?.data ?? []).map((v) => ({ value: v.id, label: v.name })),
          ]}
          error={fieldError('vrf_id')}
        />
      </div>
      <div className="space-y-1.5">
        <Input
          label={t('discovery.scopes.schedule')}
          value={values.schedule}
          onChange={(e) => set('schedule', e.target.value)}
          error={fieldError('schedule')}
          className="font-mono"
        />
        <p className="text-xs text-fg-muted">{t('discovery.scopes.form.scheduleHint')}</p>
      </div>
      <div className="border-t border-line pt-4">
        <fieldset className="space-y-2">
          <legend className="mb-2 text-sm font-medium text-fg">
            {t('discovery.scopes.form.plugins')}
          </legend>
          <div className="grid grid-cols-2 gap-x-4 gap-y-2 sm:grid-cols-4">
            {DISCOVERY_PLUGINS.map((p) => (
              <label key={p} className="flex items-center gap-2 text-sm text-fg">
                <input
                  type="checkbox"
                  checked={values.plugins.includes(p)}
                  onChange={() => set('plugins', toggled(values.plugins, p))}
                />
                {t(`discovery.scopes.plugin.${p}`)}
              </label>
            ))}
          </div>
          {fieldError('plugins') ? (
            <p className="text-sm text-danger">{fieldError('plugins')}</p>
          ) : null}
        </fieldset>
      </div>
      <div className="border-t border-line pt-4">
        <fieldset className="space-y-2">
          <legend className="mb-1 text-sm font-medium text-fg">
            {t('discovery.scopes.form.credentials')}
          </legend>
          <p className="text-xs text-fg-muted">{t('discovery.scopes.form.credentialsHint')}</p>
          {credentials.isError ? (
            <p className="text-sm text-amber-700 dark:text-amber-300">
              {t('discovery.scopes.form.credentialsUnavailable')}
            </p>
          ) : credentialList.length === 0 ? (
            <p className="text-sm text-fg-muted">{t('discovery.scopes.form.credentialsNone')}</p>
          ) : (
            <div className="flex flex-col gap-2">
              {credentialList.map((c) => (
                <label key={c.id} className="flex items-center gap-2 text-sm text-fg">
                  <input
                    type="checkbox"
                    checked={values.credential_ids.includes(c.id)}
                    onChange={() => set('credential_ids', toggled(values.credential_ids, c.id))}
                  />
                  {c.name}
                  <span className="text-xs text-fg-muted">({c.kind})</span>
                </label>
              ))}
            </div>
          )}
          {fieldError('credential_ids') ? (
            <p className="text-sm text-danger">{fieldError('credential_ids')}</p>
          ) : null}
        </fieldset>
      </div>
      {refusal ? (
        <p role="alert" className="text-sm text-danger">
          {refusal.message}
        </p>
      ) : null}
      <div className="flex justify-end gap-2 border-t border-line pt-4">
        <Button variant="secondary" onClick={onClose}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" disabled={pending}>
          {pending ? t('app.loading') : t('common.save')}
        </Button>
      </div>
    </form>
  );
}

/**
 * ScopeRows is the row of a scope in the table of its collector and, below
 * it, the causes of the failed targets of its last run (DIS-08).
 */
function ScopeRows({
  scope,
  canManage,
  switching,
  onEdit,
  onSwitch,
  onDelete,
}: {
  scope: DiscoveryScope;
  canManage: boolean;
  switching: boolean;
  onEdit: () => void;
  onSwitch: () => void;
  onDelete: () => void;
}) {
  const { t } = useTranslation();
  const hasErrors = (scope.health?.errors?.length ?? 0) > 0;
  const cell = hasErrors ? 'border-b-0 pb-1.5' : '';
  return (
    <>
      <tr className={trClasses}>
        <td className={cn(tdPrimaryClasses, cell, 'min-w-[9rem]')}>
          {scope.name && scope.name !== scope.target ? scope.name : '—'}
        </td>
        <td className={cn(tdMonoClasses, cell, 'whitespace-nowrap')}>{scope.target}</td>
        <td className={cn(tdClasses, cell)}>
          {t(`discovery.scopes.kind.${scope.kind}`, scope.kind)}
        </td>
        <td className={cn(tdClasses, cell, 'font-mono text-xs text-fg')}>{scope.schedule}</td>
        <td className={cn(tdClasses, cell)}>
          <span>{scope.plugins.join(', ')}</span>
          {scope.credential_ids.length > 0 ? (
            <span className="block text-xs text-fg-subtle">
              {t('discovery.scopes.credentialCount', { count: scope.credential_ids.length })}
            </span>
          ) : null}
        </td>
        <td className={cn(tdClasses, cell)}>
          <span className="flex flex-wrap items-center gap-1.5">
            <ScopeHealthBadge health={scope.health} />
            <Badge variant={scope.enabled ? 'success' : 'neutral'}>
              {scope.enabled ? t('discovery.scopes.enabled') : t('discovery.scopes.disabled')}
            </Badge>
          </span>
        </td>
        {canManage ? (
          <td className={cn(tdClasses, cell)}>
            <span className="flex items-center justify-end gap-0.5">
              <IconButton
                size="sm"
                label={t('discovery.scopes.editNamed', { name: scope.name })}
                icon={<Pencil />}
                onClick={onEdit}
              />
              <IconButton
                size="sm"
                label={t(
                  scope.enabled ? 'discovery.scopes.disableNamed' : 'discovery.scopes.enableNamed',
                  { name: scope.name },
                )}
                icon={scope.enabled ? <PowerOff /> : <Power />}
                disabled={switching}
                onClick={onSwitch}
              />
              <IconButton
                size="sm"
                label={t('discovery.scopes.deleteNamed', { name: scope.name })}
                icon={<Trash2 />}
                className="text-danger hover:text-danger"
                onClick={onDelete}
              />
            </span>
          </td>
        ) : null}
      </tr>
      {hasErrors ? (
        <tr>
          <td colSpan={canManage ? 7 : 6} className="border-b border-line px-4 pb-3 pt-0">
            <ScopeErrors health={scope.health} />
          </td>
        </tr>
      ) : null}
    </>
  );
}

/**
 * ScopeList shows the discovery scopes of a collector (DIS-09). Creating,
 * editing, switching and deleting need discovery:manage; deleting asks
 * first, and a failed action says why (F-107, F-267).
 */
function ScopeList({ collector, canManage }: { collector: Collector; canManage: boolean }) {
  const { t } = useTranslation();
  const { data } = useDiscoveryScopes(collector.id);
  const { update, remove } = useDiscoveryScopeMutations(collector.id);
  const [editing, setEditing] = useState<DiscoveryScope | 'new' | null>(null);
  const [deleting, setDeleting] = useState<DiscoveryScope | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const switchScope = (scope: DiscoveryScope) => {
    setActionError(null);
    update.mutate(
      { id: scope.id, patch: { enabled: !scope.enabled } },
      {
        onError: (err) =>
          setActionError(
            t('discovery.scopes.errors.toggleFailed', {
              name: scope.name,
              reason: describeActionError(err, t),
            }),
          ),
      },
    );
  };

  const confirmDelete = () => {
    if (!deleting) return;
    const scope = deleting;
    setActionError(null);
    remove.mutate(scope.id, {
      onSettled: () => setDeleting(null),
      onError: (err) =>
        setActionError(
          t('discovery.scopes.errors.deleteFailed', {
            name: scope.name,
            reason: describeActionError(err, t),
          }),
        ),
    });
  };

  const scopes = data?.data ?? [];

  return (
    <div className="border-t border-line">
      <div className="flex items-center justify-between gap-2 px-4 py-2.5">
        <h4 className="text-sm font-semibold text-fg">{t('discovery.scopes.title')}</h4>
        {canManage ? (
          <Button size="sm" variant="secondary" icon={<Plus />} onClick={() => setEditing('new')}>
            {t('discovery.scopes.add')}
          </Button>
        ) : null}
      </div>
      {actionError ? (
        <p role="alert" className="px-4 pb-2 text-sm text-danger">
          {actionError}
        </p>
      ) : null}
      {scopes.length ? (
        <ScrollRegion label={`${t('discovery.scopes.title')} ${collector.name}`}>
          <table className={cn(tableClasses, 'min-w-[40rem]')}>
            <thead className={cn(theadClasses, 'border-t border-line')}>
              <tr>
                <th scope="col" className={cn(thClasses, 'text-fg-muted')}>
                  {t('discovery.scopes.form.name')}
                </th>
                <th scope="col" className={cn(thClasses, 'text-fg-muted')}>
                  {t('discovery.scopes.target')}
                </th>
                <th scope="col" className={cn(thClasses, 'text-fg-muted')}>
                  {t('discovery.scopes.kindLabel')}
                </th>
                <th scope="col" className={cn(thClasses, 'text-fg-muted')}>
                  {t('discovery.scopes.columns.schedule')}
                </th>
                <th scope="col" className={cn(thClasses, 'text-fg-muted')}>
                  {t('discovery.scopes.form.plugins')}
                </th>
                <th scope="col" className={cn(thClasses, 'text-fg-muted')}>
                  {t('discovery.scopes.columns.status')}
                </th>
                {canManage ? (
                  <th scope="col" className={cn(thClasses, 'text-right text-fg-muted')}>
                    {t('common.actions')}
                  </th>
                ) : null}
              </tr>
            </thead>
            <tbody>
              {scopes.map((scope) => (
                <ScopeRows
                  key={scope.id}
                  scope={scope}
                  canManage={canManage}
                  switching={update.isPending}
                  onEdit={() => setEditing(scope)}
                  onSwitch={() => switchScope(scope)}
                  onDelete={() => setDeleting(scope)}
                />
              ))}
            </tbody>
          </table>
        </ScrollRegion>
      ) : (
        <p className="px-4 pb-4 text-sm text-fg-muted">{t('discovery.scopes.none')}</p>
      )}
      {canManage ? (
        <Modal
          open={editing !== null}
          onOpenChange={(o) => {
            if (!o) setEditing(null);
          }}
          title={
            editing === 'new'
              ? t('discovery.scopes.form.createTitle', { collector: collector.name })
              : t('discovery.scopes.form.editTitle', { name: editing?.name ?? '' })
          }
        >
          {editing !== null ? (
            <ScopeForm
              key={editing === 'new' ? 'new' : editing.id}
              collector={collector}
              scope={editing === 'new' ? undefined : editing}
              onClose={() => setEditing(null)}
            />
          ) : null}
        </Modal>
      ) : null}
      {canManage ? (
        <Modal
          open={deleting !== null}
          onOpenChange={(o) => {
            if (!o) setDeleting(null);
          }}
          title={t('discovery.scopes.confirmDelete.title')}
          description={t('discovery.scopes.confirmDelete.text', {
            name: deleting?.name ?? '',
            target: deleting?.target ?? '',
          })}
        >
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setDeleting(null)}>
              {t('common.cancel')}
            </Button>
            <Button variant="danger" disabled={remove.isPending} onClick={confirmDelete}>
              {t('discovery.scopes.confirmDelete.confirm')}
            </Button>
          </div>
        </Modal>
      ) : null}
    </div>
  );
}

/** The anchor of the card of the scope suggestions, the target of CollectorHints. */
const SUGGESTIONS_ANCHOR = 'scope-suggestions';

const hintClass =
  'flex gap-2.5 rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-100';

/**
 * CollectorHints says why a collector delivers nothing: it has no edge
 * connection (offline, or never a heartbeat: data reach the server only over
 * the mTLS endpoint on 4222/TCP, COL-08), or it has no enabled scope, so it
 * scans nothing until a suggested subnet is confirmed or a scope is added
 * (INS-06, DIS-09). A disabled collector is switched off on purpose and gets
 * no hint. The link leads to the suggestions of the collector; confirming
 * them needs review:resolve besides discovery:manage, like the Accept button.
 */
function CollectorHints({ collector, canResolve }: { collector: Collector; canResolve: boolean }) {
  const { t } = useTranslation();
  const scopes = useDiscoveryScopes(collector.id);
  const suggestions = useScopeSuggestions();
  if (collector.status === 'disabled') return null;
  const noEdge = collector.status === 'offline' || !collector.last_heartbeat_at;
  // Only once the scopes have loaded: a failed or pending read is no hint.
  const noScope = scopes.data !== undefined && !scopes.data.data.some((s) => s.enabled);
  const suggested = (suggestions.data?.data ?? []).filter(
    (item) => item.payload.collector_id === collector.id,
  ).length;
  if (!noEdge && !noScope) return null;
  return (
    <div className="space-y-2">
      {noEdge ? (
        <div role="note" className={hintClass}>
          <TriangleAlert aria-hidden="true" className="mt-0.5 h-4 w-4 shrink-0" />
          <div>
            <p className="font-medium">{t('discovery.collectorHints.noEdge')}</p>
            <p className="mt-1">{t('discovery.collectorHints.noEdgeCauses')}</p>
          </div>
        </div>
      ) : null}
      {noScope ? (
        <div role="note" className={hintClass}>
          <TriangleAlert aria-hidden="true" className="mt-0.5 h-4 w-4 shrink-0" />
          <div>
            <p className="font-medium">{t('discovery.collectorHints.noScope')}</p>
            {suggested > 0 ? (
              <a
                href={`#${SUGGESTIONS_ANCHOR}`}
                className="mt-1 inline-block font-medium underline underline-offset-2"
              >
                {t('discovery.collectorHints.showSuggestions', { count: suggested })}
              </a>
            ) : null}
            {!canResolve ? (
              <p className="mt-1">{t('discovery.collectorHints.noScopeRights')}</p>
            ) : null}
          </div>
        </div>
      ) : null}
    </div>
  );
}

function CollectorCard({
  collector,
  canManage,
  canResolve,
}: {
  collector: Collector;
  canManage: boolean;
  canResolve: boolean;
}) {
  const { t, i18n } = useTranslation();

  return (
    <Panel
      title={collector.name}
      actions={
        <Badge variant={getCollectorVariant(collector.status)}>
          {t(`discovery.collectorStatus.${collector.status}`, collector.status)}
        </Badge>
      }
      padded={false}
    >
      <div className="space-y-3 px-4 py-3">
        <CollectorHints collector={collector} canResolve={canResolve} />
        <dl className="grid grid-cols-1 gap-x-6 gap-y-2 text-sm sm:grid-cols-3">
          <div>
            <dt className="text-xs text-fg-muted">{t('discovery.version')}</dt>
            <dd className="font-medium text-fg">{collector.version || '—'}</dd>
          </div>
          <div>
            <dt className="text-xs text-fg-muted">{t('discovery.lastHeartbeat')}</dt>
            <dd className="font-medium tabular-nums text-fg">
              {formatHeartbeat(collector.last_heartbeat_at, i18n.language)}
            </dd>
          </div>
          <div>
            <dt className="text-xs text-fg-muted">{t('discovery.createdAt')}</dt>
            <dd className="font-medium tabular-nums text-fg">
              {formatHeartbeat(collector.created_at, i18n.language)}
            </dd>
          </div>
        </dl>
      </div>
      <ScopeList collector={collector} canManage={canManage} />
    </Panel>
  );
}

export function DiscoveryPage() {
  const { t } = useTranslation();
  const { data, isLoading, error, refetch } = useCollectors({ limit: 100, offset: 0 });
  const [enrollOpen, setEnrollOpen] = useState(false);
  // The server decides every request; the page offers only what the user
  // may do, fail-closed while the rights load: scopes need discovery:manage,
  // enrolling a collector collector:manage, resolving a suggestion
  // review:resolve besides discovery:manage (RBA-01, F-107).
  const canManage = useHasPermission('discovery:manage') === true;
  const canEnroll = useHasPermission('collector:manage') === true;
  const canResolve = useHasPermission('review:resolve') === true && canManage;

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('nav.discovery')}
        description={t('discovery.summary')}
        meta={
          <Badge variant="neutral">
            {data?.total ?? 0} {t('discovery.collectors')}
          </Badge>
        }
        actions={
          canEnroll ? (
            <Button icon={<Plug />} onClick={() => setEnrollOpen(true)}>
              {t('discovery.enroll.button', 'Collector verbinden')}
            </Button>
          ) : null
        }
      />

      <EnrollWizard open={enrollOpen} onClose={() => setEnrollOpen(false)} />

      {/* Without suggestions the anchor is empty and takes no space. */}
      <div id={SUGGESTIONS_ANCHOR} className="scroll-mt-6 empty:hidden">
        <ScopeSuggestions canResolve={canResolve} />
      </div>

      {isLoading ? <SkeletonList rows={3} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      {data?.data.length ? (
        <div className="space-y-4">
          {data.data.map((collector) => (
            <CollectorCard
              key={collector.id}
              collector={collector}
              canManage={canManage}
              canResolve={canResolve}
            />
          ))}
        </div>
      ) : null}

      {data && data.data.length === 0 ? (
        <EmptyState
          icon={<Radar />}
          title={t('discovery.empty')}
          description={t('discovery.emptyHint')}
        />
      ) : null}
    </div>
  );
}
