/**
 * GroupCredentialsPanel is the section "Zugangsdaten" of a monitoring group
 * (CRD-04, PRB-01, E-178, WP-1080): the group is the device group of the
 * credential resolution. It lists the bindings of GET
 * /credential-bindings?target_kind=device_group&target_id=<group> with
 * credential, namespace and priority; a credential is bound with namespace
 * and priority (POST), a binding changed (PATCH priority, namespace) or
 * removed (DELETE) over credentialBindingApi (WP-923). Without
 * credential:read the panel is hidden, without credential:bind read only
 * with a hint. A 422 (credential_id, target_id: a credential of another
 * client) appears at the field.
 */
import { Trash2 } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError, type CredentialKind, type MonitoringGroup } from '../../api/client';
import type { CredentialBinding, CredentialNamespace } from '../../api/cmdb';
import { useCredentials, useHasPermission } from '../../api/hooks';
import {
  useGroupCredentialBindings,
  useGroupCredentialMutations,
} from '../../api/monitoringSettingsHooks';
import { KIND_NAMESPACE } from '../cmdb/CredentialBindingSection';
import { Button, IconButton } from '../ui/Button';
import { Input } from '../ui/Input';
import { Select } from '../ui/Select';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  thClasses,
  theadClasses,
  trClasses,
} from '../ui/table';

const DEFAULT_PRIORITY = 100;

export function GroupCredentialsPanel({ group }: { group: MonitoringGroup }) {
  const canRead = useHasPermission('credential:read');
  if (canRead !== true) return null;
  return <GroupCredentialsBody group={group} />;
}

/** fieldOf maps the field of a 422 to the input that shows it. */
function fieldOf(err: unknown): string | undefined {
  if (!(err instanceof ApiError) || err.status !== 422 || !err.field) return undefined;
  return err.field === 'target_id' ? 'credential_id' : err.field;
}

function GroupCredentialsBody({ group }: { group: MonitoringGroup }) {
  const { t } = useTranslation();
  const canBind = useHasPermission('credential:bind') === true;
  const bindings = useGroupCredentialBindings(group.id);
  const credentials = useCredentials();
  const { create, update, remove } = useGroupCredentialMutations(group.id);
  const [credentialId, setCredentialId] = useState('');
  const [namespace, setNamespace] = useState('');
  const [priority, setPriority] = useState(String(DEFAULT_PRIORITY));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<string>();

  const all = credentials.data?.data ?? [];
  // A credential of a client binds only to a group of that client.
  const offered = all.filter((c) => !c.client_id || c.client_id === group.client_id);
  const nameOf = (id: string) => all.find((c) => c.id === id)?.name ?? id;
  const chosen = all.find((c) => c.id === credentialId);
  const namespaces = chosen
    ? [KIND_NAMESPACE[chosen.kind as CredentialKind]].filter(Boolean)
    : [...new Set(Object.values(KIND_NAMESPACE))];
  const rows = bindings.data?.data ?? [];

  const refused = (err: unknown) => {
    const field = fieldOf(err);
    if (field && err instanceof ApiError) {
      setErrors({ [field]: err.detail || t('monitoring.settings.credentials.invalid') });
      return;
    }
    setFailure(
      err instanceof ApiError && err.status === 403
        ? t('monitoring.settings.credentials.forbidden')
        : t('monitoring.settings.credentials.saveFailed'),
    );
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setErrors({});
    setFailure(undefined);
    if (!credentialId) {
      setErrors({ credential_id: t('monitoring.settings.credentials.chooseRequired') });
      return;
    }
    const prio = Number(priority);
    if (!Number.isInteger(prio) || prio < 0 || prio > 10000) {
      setErrors({ priority: t('monitoring.settings.credentials.priorityRange') });
      return;
    }
    create.mutate(
      {
        credential_id: credentialId,
        ...(namespace ? { namespace: namespace as CredentialNamespace } : {}),
        priority: prio,
      },
      {
        onSuccess: () => {
          setCredentialId('');
          setNamespace('');
          setPriority(String(DEFAULT_PRIORITY));
        },
        onError: refused,
      },
    );
  };

  return (
    <section
      aria-label={t('monitoring.settings.credentials.title')}
      className="space-y-3 rounded-lg border border-line p-3"
    >
      <h4 className="text-sm font-semibold text-fg">
        {t('monitoring.settings.credentials.title')}
      </h4>
      {!canBind ? (
        <p className="text-xs text-fg-muted">{t('monitoring.settings.credentials.readOnly')}</p>
      ) : null}
      {failure ? (
        <p role="alert" className="text-sm text-danger">
          {failure}
        </p>
      ) : null}
      {bindings.isError ? (
        <p role="alert" className="text-sm text-danger">
          {t('monitoring.settings.credentials.loadFailed')}
        </p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-fg-muted">
          {bindings.isPending ? t('app.loading') : t('monitoring.settings.credentials.none')}
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className={tableClasses}>
            <thead className={theadClasses}>
              <tr>
                <th scope="col" className={thClasses}>
                  {t('monitoring.settings.credentials.credential')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('monitoring.settings.credentials.namespace')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('monitoring.settings.credentials.priority')}
                </th>
                {canBind ? (
                  <th scope="col" className={thClasses}>
                    <span className="sr-only">{t('common.actions')}</span>
                  </th>
                ) : null}
              </tr>
            </thead>
            <tbody>
              {rows.map((binding) => (
                <BindingRow
                  key={binding.id}
                  binding={binding}
                  name={nameOf(binding.credential_id)}
                  canBind={canBind}
                  busy={update.isPending || remove.isPending}
                  onUpdate={(data, onError) => update.mutate({ id: binding.id, data }, { onError })}
                  onRemove={() =>
                    remove.mutate(binding.id, {
                      onError: () => setFailure(t('monitoring.settings.credentials.saveFailed')),
                    })
                  }
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
      {canBind ? (
        <form onSubmit={submit} noValidate className="grid gap-3 sm:grid-cols-4 sm:items-end">
          <Select
            label={t('monitoring.settings.credentials.credential')}
            value={credentialId}
            error={errors.credential_id}
            onChange={(event) => {
              setCredentialId(event.target.value);
              setNamespace('');
            }}
            options={[
              { value: '', label: t('monitoring.settings.credentials.choose') },
              ...offered.map((c) => ({
                value: c.id,
                label: `${c.name} (${t(`discovery.credentials.kinds.${c.kind}`, c.kind)})`,
              })),
            ]}
          />
          <Select
            label={t('monitoring.settings.credentials.namespace')}
            value={namespace}
            error={errors.namespace}
            onChange={(event) => setNamespace(event.target.value)}
            options={[
              { value: '', label: t('monitoring.settings.credentials.namespaceDerived') },
              ...namespaces.map((ns) => ({
                value: ns,
                label: t(`credentials.binding.namespaces.${ns}`, ns),
              })),
            ]}
          />
          <Input
            label={t('monitoring.settings.credentials.priority')}
            inputMode="numeric"
            value={priority}
            error={errors.priority}
            onChange={(event) => setPriority(event.target.value)}
          />
          <Button type="submit" loading={create.isPending}>
            {t('monitoring.settings.credentials.add')}
          </Button>
        </form>
      ) : null}
    </section>
  );
}

function BindingRow({
  binding,
  name,
  canBind,
  busy,
  onUpdate,
  onRemove,
}: {
  binding: CredentialBinding;
  name: string;
  canBind: boolean;
  busy: boolean;
  onUpdate: (
    data: { priority?: number; namespace?: CredentialNamespace },
    onError: (err: unknown) => void,
  ) => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  const [priority, setPriority] = useState(String(binding.priority));
  const [error, setError] = useState<string>();
  const [namespaceError, setNamespaceError] = useState<string>();
  const namespaceLabel = t(
    `credentials.binding.namespaces.${binding.namespace}`,
    binding.namespace,
  );
  const savePriority = () => {
    const prio = Number(priority);
    if (prio === binding.priority) return;
    if (!Number.isInteger(prio) || prio < 0 || prio > 10000) {
      setError(t('monitoring.settings.credentials.priorityRange'));
      return;
    }
    setError(undefined);
    onUpdate({ priority: prio }, (err) =>
      setError(
        err instanceof ApiError && err.detail
          ? err.detail
          : t('monitoring.settings.credentials.saveFailed'),
      ),
    );
  };
  return (
    <tr className={trClasses} data-testid={`group-binding-${binding.id}`}>
      <td className={tdPrimaryClasses}>{name}</td>
      <td className={tdClasses}>
        {canBind ? (
          <Select
            aria-label={t('monitoring.settings.credentials.namespaceOf', { name })}
            value={binding.namespace}
            error={namespaceError}
            onChange={(event) => {
              setNamespaceError(undefined);
              onUpdate({ namespace: event.target.value as CredentialNamespace }, (err) =>
                setNamespaceError(
                  err instanceof ApiError && err.detail
                    ? err.detail
                    : t('monitoring.settings.credentials.saveFailed'),
                ),
              );
            }}
            options={[...new Set([binding.namespace, ...Object.values(KIND_NAMESPACE)])].map(
              (ns) => ({ value: ns, label: t(`credentials.binding.namespaces.${ns}`, ns) }),
            )}
          />
        ) : (
          namespaceLabel
        )}
      </td>
      <td className={tdClasses}>
        {canBind ? (
          <Input
            aria-label={t('monitoring.settings.credentials.priorityOf', { name })}
            inputMode="numeric"
            className="w-24"
            value={priority}
            error={error}
            onChange={(event) => setPriority(event.target.value)}
            onBlur={savePriority}
            onKeyDown={(event) => {
              if (event.key === 'Enter') {
                event.preventDefault();
                savePriority();
              }
            }}
          />
        ) : (
          binding.priority
        )}
      </td>
      {canBind ? (
        <td className={tdClasses}>
          <IconButton
            size="sm"
            icon={<Trash2 />}
            label={t('monitoring.settings.credentials.removeNamed', { name })}
            className="text-danger hover:text-danger"
            disabled={busy}
            onClick={onRemove}
          />
        </td>
      ) : null}
    </tr>
  );
}
