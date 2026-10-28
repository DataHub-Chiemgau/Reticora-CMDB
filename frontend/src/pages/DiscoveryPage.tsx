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
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';

interface EnrollmentCodeResponse {
  id: string;
  code: string;
  label: string;
  expires_at: string;
}

function createEnrollmentCode(label: string): Promise<EnrollmentCodeResponse> {
  return fetchAPI('/collectors/enrollment-codes', {
    method: 'POST',
    body: JSON.stringify({ label }),
  });
}

/** EnrollWizard guides connecting a collector: mint a code, show the command. */
function EnrollWizard({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [label, setLabel] = useState('');
  const [code, setCode] = useState<EnrollmentCodeResponse | null>(null);
  const [copied, setCopied] = useState(false);

  const mint = useMutation({
    mutationFn: () => createEnrollmentCode(label || 'collector'),
    onSuccess: (data) => {
      setCode(data);
      queryClient.invalidateQueries({ queryKey: ['collectors'] });
    },
  });

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
          onClose();
        }
      }}
      title={t('discovery.enroll.title', 'Collector verbinden')}
    >
      {!code ? (
        <div className="space-y-4">
          <p className="text-sm text-gray-600 dark:text-gray-300">
            {t(
              'discovery.enroll.intro',
              'Schließen Sie einen Collector an: erzeugen Sie einen einmaligen Enrollment-Code und geben Sie ihn im Collector ein. Die Infrastruktur erscheint automatisch.',
            )}
          </p>
          <label className="block text-sm">
            <span className="mb-1 block text-gray-700 dark:text-gray-300">
              {t('discovery.enroll.label', 'Bezeichnung (z. B. Standort)')}
            </span>
            <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="site-a" />
          </label>
          {mint.isError ? <p className="text-sm text-danger">{t('app.error')}</p> : null}
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={onClose}>
              {t('common.cancel', 'Abbrechen')}
            </Button>
            <Button onClick={() => mint.mutate()} disabled={mint.isPending}>
              {mint.isPending ? t('app.loading') : t('discovery.enroll.generate', 'Code erzeugen')}
            </Button>
          </div>
        </div>
      ) : (
        <div className="space-y-4">
          <p className="text-sm text-gray-600 dark:text-gray-300">
            {t(
              'discovery.enroll.once',
              'Dieser Code wird nur einmal angezeigt und läuft ab. Führen Sie im Kundennetz aus:',
            )}
          </p>
          <pre className="overflow-x-auto rounded-lg bg-gray-900 p-3 text-xs text-green-200 dark:bg-black">
            {enrollCommand}
          </pre>
          <p className="text-xs text-gray-500 dark:text-gray-400">
            {t('discovery.enroll.expires', 'Gültig bis')}:{' '}
            {new Date(code.expires_at).toLocaleString()}
          </p>
          <p className="text-sm text-gray-600 dark:text-gray-300">{t('discovery.enroll.after')}</p>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={copy}>
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

function formatHeartbeat(value?: string) {
  if (!value) {
    return '—';
  }

  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }

  return new Intl.DateTimeFormat('de-DE', {
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
    <ul className="w-full space-y-1 text-xs text-red-700 dark:text-red-300">
      {health.errors.map((err, i) => (
        <li key={`${err.protocol}-${err.target}-${i}`}>
          <span className="font-medium">{t(`discovery.scopes.cause.${err.cause}`, err.cause)}</span>
          {err.protocol ? ` · ${err.protocol}` : ''}
          {err.target ? ` · ${err.target}` : ''}
          {err.message ? (
            <span className="text-gray-500 dark:text-gray-400"> — {err.message}</span>
          ) : null}
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
    <Card title={t('discovery.suggestions.title')}>
      <p className="mb-3 text-sm text-gray-600 dark:text-gray-300">
        {t('discovery.suggestions.intro')}
      </p>
      {error ? (
        <p role="alert" className="mb-3 text-sm text-red-600 dark:text-red-400">
          {error}
        </p>
      ) : null}
      <ul className="space-y-2 text-sm">
        {items.map((item) => (
          <li key={item.id} className="flex flex-wrap items-center justify-between gap-2">
            <span className="text-gray-900 dark:text-gray-100">
              <span className="font-mono">{item.payload.cidr}</span>{' '}
              <span className="text-gray-500 dark:text-gray-400">
                {t('discovery.suggestions.from', {
                  collector: item.payload.collector_name ?? item.payload.collector_id ?? '',
                })}
              </span>
            </span>
            {canResolve ? (
              <span className="flex gap-2">
                <Button
                  size="sm"
                  disabled={resolve.isPending}
                  onClick={() => act(item.id, 'accept')}
                >
                  {t('discovery.suggestions.accept')}
                </Button>
                <Button
                  size="sm"
                  variant="secondary"
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
    </Card>
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
    <form className="space-y-4" onSubmit={submit} noValidate>
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
      <div className="space-y-1">
        <Input
          label={t('discovery.scopes.schedule')}
          value={values.schedule}
          onChange={(e) => set('schedule', e.target.value)}
          error={fieldError('schedule')}
        />
        <p className="text-xs text-gray-500 dark:text-gray-400">
          {t('discovery.scopes.form.scheduleHint')}
        </p>
      </div>
      <fieldset>
        <legend className="mb-1 text-sm font-medium text-gray-700 dark:text-gray-200">
          {t('discovery.scopes.form.plugins')}
        </legend>
        <div className="flex flex-wrap gap-x-4 gap-y-1">
          {DISCOVERY_PLUGINS.map((p) => (
            <label
              key={p}
              className="flex items-center gap-1 text-sm text-gray-700 dark:text-gray-300"
            >
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
          <p className="mt-1 text-sm text-red-600 dark:text-red-400">{fieldError('plugins')}</p>
        ) : null}
      </fieldset>
      <fieldset>
        <legend className="mb-1 text-sm font-medium text-gray-700 dark:text-gray-200">
          {t('discovery.scopes.form.credentials')}
        </legend>
        <p className="mb-1 text-xs text-gray-500 dark:text-gray-400">
          {t('discovery.scopes.form.credentialsHint')}
        </p>
        {credentials.isError ? (
          <p className="text-sm text-amber-700 dark:text-amber-300">
            {t('discovery.scopes.form.credentialsUnavailable')}
          </p>
        ) : credentialList.length === 0 ? (
          <p className="text-sm text-gray-500 dark:text-gray-400">
            {t('discovery.scopes.form.credentialsNone')}
          </p>
        ) : (
          <div className="flex flex-col gap-1">
            {credentialList.map((c) => (
              <label
                key={c.id}
                className="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300"
              >
                <input
                  type="checkbox"
                  checked={values.credential_ids.includes(c.id)}
                  onChange={() => set('credential_ids', toggled(values.credential_ids, c.id))}
                />
                {c.name}
                <span className="text-xs text-gray-500 dark:text-gray-400">({c.kind})</span>
              </label>
            ))}
          </div>
        )}
        {fieldError('credential_ids') ? (
          <p className="mt-1 text-sm text-red-600 dark:text-red-400">
            {fieldError('credential_ids')}
          </p>
        ) : null}
      </fieldset>
      {refusal ? (
        <p role="alert" className="text-sm text-red-600 dark:text-red-400">
          {refusal.message}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
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

  return (
    <div className="mt-4 border-t border-gray-200 pt-4 dark:border-gray-700">
      <div className="flex items-center justify-between gap-2">
        <h4 className="text-sm font-semibold text-gray-900 dark:text-gray-100">
          {t('discovery.scopes.title')}
        </h4>
        {canManage ? (
          <Button size="sm" onClick={() => setEditing('new')}>
            {t('discovery.scopes.add')}
          </Button>
        ) : null}
      </div>
      {actionError ? (
        <p role="alert" className="mt-2 text-sm text-red-600 dark:text-red-400">
          {actionError}
        </p>
      ) : null}
      {data?.data.length ? (
        <ul className="mt-2 space-y-2 text-sm">
          {data.data.map((scope) => (
            <li key={scope.id} className="flex flex-wrap items-center justify-between gap-2">
              <span className="min-w-0 text-gray-900 [overflow-wrap:anywhere] dark:text-gray-100">
                {scope.name && scope.name !== scope.target ? (
                  <span className="font-medium">{scope.name} </span>
                ) : null}
                <span className="font-mono">{scope.target}</span>{' '}
                <span className="text-gray-500 dark:text-gray-400">
                  ({t(`discovery.scopes.kind.${scope.kind}`, scope.kind)}, {scope.schedule},{' '}
                  {scope.plugins.join(', ')}
                  {scope.credential_ids.length > 0
                    ? `, ${t('discovery.scopes.credentialCount', { count: scope.credential_ids.length })}`
                    : ''}
                  )
                </span>
              </span>
              <span className="flex flex-wrap items-center gap-2">
                <ScopeHealthBadge health={scope.health} />
                <Badge variant={scope.enabled ? 'success' : 'neutral'}>
                  {scope.enabled ? t('discovery.scopes.enabled') : t('discovery.scopes.disabled')}
                </Badge>
                {canManage ? (
                  <>
                    <Button
                      size="sm"
                      variant="secondary"
                      aria-label={t('discovery.scopes.editNamed', { name: scope.name })}
                      onClick={() => setEditing(scope)}
                    >
                      {t('discovery.scopes.edit')}
                    </Button>
                    <Button
                      size="sm"
                      variant="secondary"
                      aria-label={t(
                        scope.enabled
                          ? 'discovery.scopes.disableNamed'
                          : 'discovery.scopes.enableNamed',
                        { name: scope.name },
                      )}
                      disabled={update.isPending}
                      onClick={() => switchScope(scope)}
                    >
                      {scope.enabled ? t('discovery.scopes.disable') : t('discovery.scopes.enable')}
                    </Button>
                    <Button
                      size="sm"
                      variant="danger"
                      aria-label={t('discovery.scopes.deleteNamed', { name: scope.name })}
                      onClick={() => setDeleting(scope)}
                    >
                      {t('discovery.scopes.delete')}
                    </Button>
                  </>
                ) : null}
              </span>
              <ScopeErrors health={scope.health} />
            </li>
          ))}
        </ul>
      ) : (
        <p className="mt-2 text-sm text-gray-500 dark:text-gray-400">
          {t('discovery.scopes.none')}
        </p>
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

function CollectorCard({ collector, canManage }: { collector: Collector; canManage: boolean }) {
  const { t } = useTranslation();

  return (
    <Card
      title={collector.name}
      actions={
        <Badge variant={getCollectorVariant(collector.status)}>
          {t(`discovery.collectorStatus.${collector.status}`, collector.status)}
        </Badge>
      }
    >
      <dl className="space-y-2 text-sm">
        <div className="flex justify-between gap-4">
          <dt className="text-gray-500 dark:text-gray-400">{t('discovery.version')}</dt>
          <dd className="font-medium text-gray-900 dark:text-gray-100">
            {collector.version || '—'}
          </dd>
        </div>
        <div className="flex justify-between gap-4">
          <dt className="text-gray-500 dark:text-gray-400">{t('discovery.lastHeartbeat')}</dt>
          <dd className="font-medium text-right text-gray-900 dark:text-gray-100">
            {formatHeartbeat(collector.last_heartbeat_at)}
          </dd>
        </div>
        <div className="flex justify-between gap-4">
          <dt className="text-gray-500 dark:text-gray-400">{t('discovery.createdAt')}</dt>
          <dd className="font-medium text-right text-gray-900 dark:text-gray-100">
            {formatHeartbeat(collector.created_at)}
          </dd>
        </div>
      </dl>
      <ScopeList collector={collector} canManage={canManage} />
    </Card>
  );
}

export function DiscoveryPage() {
  const { t } = useTranslation();
  const { data, isLoading, error } = useCollectors({ limit: 100, offset: 0 });
  const [enrollOpen, setEnrollOpen] = useState(false);
  // The server decides every request; the page offers only what the user
  // may do, fail-closed while the rights load: scopes need discovery:manage,
  // enrolling a collector collector:manage, resolving a suggestion
  // review:resolve besides discovery:manage (RBA-01, F-107).
  const canManage = useHasPermission('discovery:manage') === true;
  const canEnroll = useHasPermission('collector:manage') === true;
  const canResolve = useHasPermission('review:resolve') === true && canManage;

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
            {t('nav.discovery')}
          </h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('discovery.summary')}</p>
        </div>
        <div className="flex items-center gap-3">
          <span className="text-sm text-gray-500 dark:text-gray-400">
            {data?.total ?? 0} {t('discovery.collectors')}
          </span>
          {canEnroll ? (
            <Button size="sm" onClick={() => setEnrollOpen(true)}>
              {t('discovery.enroll.button', 'Collector verbinden')}
            </Button>
          ) : null}
        </div>
      </div>

      <EnrollWizard open={enrollOpen} onClose={() => setEnrollOpen(false)} />

      <ScopeSuggestions canResolve={canResolve} />

      {isLoading ? (
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('app.loading')}</p>
      ) : null}
      {error ? <p className="text-sm text-red-600 dark:text-red-400">{t('app.error')}</p> : null}

      {data?.data.length ? (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {data.data.map((collector) => (
            <CollectorCard key={collector.id} collector={collector} canManage={canManage} />
          ))}
        </div>
      ) : null}

      {data && data.data.length === 0 ? (
        <Card>
          <p className="text-sm text-gray-500 dark:text-gray-400">{t('common.noResults')}</p>
        </Card>
      ) : null}
    </div>
  );
}
