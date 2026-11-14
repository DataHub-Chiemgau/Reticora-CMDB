/**
 * CredentialBindingSection is the section "Zugangsdaten" of the CI detail
 * (CRD-07, UI-14, WP-923). Per protocol namespace it shows the effective
 * credential of the CI (GET /cis/{id}/credentials/effective): name, kind,
 * source (stored or vault reference), origin (probe, CI, device group with
 * its name, subnet with its CIDR, scope, client, organization) and the
 * usage at this CI (last success, last failure with the error class, GET
 * /credentials/{id}/usage?ci_id=). A namespace without one shows "keine".
 *
 * "Zugangsart setzen" binds a credential to the CI for the namespace in one
 * of three ways: create a device-specific one (CredentialForm, POST
 * /credentials with the client of the CI, then PUT /cis/{id}/credentials),
 * choose an existing one of the namespace's kinds and the CI's client, or
 * create a vault reference (CredentialForm with source vault_ref), then
 * bind. A CI binding can be removed; the next origin applies then.
 *
 * Secrets are write-only (WP-856): nothing here reads one, no answer
 * carries one. The section needs credential:read (otherwise nothing is
 * shown or read), binding and removing credential:bind, creating
 * credential:manage; the server decides every request.
 */
import { KeyRound, Link2, Trash2 } from 'lucide-react';
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { Credential, CredentialKind } from '../../api/client';
import {
  type CICredentialBindingEntry,
  type CredentialBinding,
  type CredentialNamespace,
  type EffectiveCredential,
} from '../../api/cmdb';
import {
  useCICredentialBindings,
  useCIEffectiveCredentials,
  useCredentialOriginTarget,
  useCredentials,
  useCredentialUsage,
  useHasPermission,
  useReplaceCICredentials,
} from '../../api/hooks';
import {
  CREDENTIAL_KINDS,
  CredentialForm,
  describeRefusal,
  useClientOptions,
} from '../discovery/CredentialsSection';
import { ScrollRegion } from '../ScrollRegion';
import { Button } from '../ui/Button';
import { Card } from '../ui/Card';
import { ErrorState } from '../ui/ErrorState';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';
import { SkeletonList } from '../ui/Skeleton';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../ui/table';
import { cn } from '../ui/utils';

/**
 * KIND_NAMESPACE maps a kind of the form to the protocol namespace of the
 * collector (KindNamespace in backend/internal/credential/binding.go).
 */
export const KIND_NAMESPACE: Record<CredentialKind, CredentialNamespace> = {
  snmp_v2c: 'snmp',
  snmp_v3: 'snmp',
  ssh_password: 'ssh',
  ssh_key: 'ssh',
  redfish: 'redfish',
  ipmi: 'ipmi',
  wmi: 'wmi',
  api_token: 'api',
  nut: 'nut',
  nas: 'nas',
};

/** The namespaces the section always lists, in the order of the kinds. */
const NAMESPACES: CredentialNamespace[] = [
  ...new Set(CREDENTIAL_KINDS.map((k) => KIND_NAMESPACE[k])),
];

/** The binding priority of a binding set here (DefaultBindingPriority). */
const DEFAULT_PRIORITY = 100;

type SetWay = 'create' | 'existing' | 'vault';

export interface CredentialBindingSectionProps {
  ciId: string;
  /** The client of the CI: preselected for a new credential, filter of the existing ones. */
  clientId?: string;
}

/**
 * CredentialBindingSection renders nothing without credential:read (and
 * while the rights load), so the CI detail sends no credential request.
 */
export function CredentialBindingSection(props: CredentialBindingSectionProps) {
  const canRead = useHasPermission('credential:read');
  if (canRead !== true) return null;
  return <CredentialBindingBody {...props} />;
}

function formatDate(value: string | null | undefined, language: string): string | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(language, { dateStyle: 'medium', timeStyle: 'short' }).format(
    date,
  );
}

function CredentialBindingBody({ ciId, clientId }: CredentialBindingSectionProps) {
  const { t } = useTranslation();
  const canBind = useHasPermission('credential:bind') === true;
  const canManage = useHasPermission('credential:manage') === true;
  const effective = useCIEffectiveCredentials(ciId);
  const bindings = useCICredentialBindings(ciId);
  const credentials = useCredentials();
  const replace = useReplaceCICredentials(ciId);
  const [setting, setSetting] = useState<CredentialNamespace | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const ciBindings = bindings.data?.data ?? [];
  const byNamespace = new Map<string, EffectiveCredential>();
  for (const entry of effective.data?.data ?? []) byNamespace.set(entry.namespace, entry);
  const namespaces = [
    ...new Set<CredentialNamespace>([
      ...NAMESPACES,
      ...byNamespace.keys(),
      ...ciBindings.map((b) => b.namespace),
    ] as CredentialNamespace[]),
  ];
  const credentialList = credentials.data?.data ?? [];
  const sourceOf = (id: string) => credentialList.find((c) => c.id === id)?.source;

  const entriesWithout = (namespace: CredentialNamespace): CICredentialBindingEntry[] =>
    ciBindings
      .filter((b) => b.namespace !== namespace)
      .map((b) => ({
        credential_id: b.credential_id,
        namespace: b.namespace,
        priority: b.priority,
      }));

  const bind = (namespace: CredentialNamespace, credentialId: string, onDone?: () => void) => {
    setActionError(null);
    replace.mutate(
      [
        ...entriesWithout(namespace),
        { credential_id: credentialId, namespace, priority: DEFAULT_PRIORITY },
      ],
      {
        onSuccess: () => onDone?.(),
        onError: (err) =>
          setActionError(
            t('credentials.binding.errors.bindFailed', { reason: describeRefusal(err, t) }),
          ),
      },
    );
  };

  const unbind = (binding: CredentialBinding) => {
    setActionError(null);
    replace.mutate(
      ciBindings
        .filter((b) => b.id !== binding.id)
        .map((b) => ({
          credential_id: b.credential_id,
          namespace: b.namespace,
          priority: b.priority,
        })),
      {
        onError: (err) =>
          setActionError(
            t('credentials.binding.errors.unbindFailed', { reason: describeRefusal(err, t) }),
          ),
      },
    );
  };

  const loading = effective.isPending || bindings.isPending;
  const failed = effective.isError || bindings.isError;

  return (
    <div role="region" aria-label={t('credentials.binding.title')}>
      <Card title={t('credentials.binding.title')}>
        <p className="mb-3 text-sm text-fg-muted">{t('credentials.binding.intro')}</p>
        {actionError ? (
          <p role="alert" className="mb-3 text-sm text-danger">
            {actionError}
          </p>
        ) : null}
        {loading ? (
          <SkeletonList rows={3} label={t('app.loading')} />
        ) : failed ? (
          <ErrorState
            title={t('credentials.binding.loadFailed')}
            retryLabel={t('common.retry')}
            onRetry={() => {
              void effective.refetch();
              void bindings.refetch();
            }}
          />
        ) : (
          <ScrollRegion label={t('credentials.binding.listLabel')}>
            <table className={cn(tableClasses, 'min-w-[48rem]')}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={cn(thClasses, 'text-fg-muted')}>
                    {t('credentials.binding.columns.namespace')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-fg-muted')}>
                    {t('credentials.binding.columns.credential')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-fg-muted')}>
                    {t('credentials.binding.columns.origin')}
                  </th>
                  <th scope="col" className={cn(thClasses, 'text-fg-muted')}>
                    {t('credentials.binding.columns.usage')}
                  </th>
                  {canBind ? (
                    <th scope="col" className={cn(thClasses, 'text-right text-fg-muted')}>
                      {t('common.actions')}
                    </th>
                  ) : null}
                </tr>
              </thead>
              <tbody>
                {namespaces.map((namespace) => (
                  <NamespaceRow
                    key={namespace}
                    ciId={ciId}
                    namespace={namespace}
                    entry={byNamespace.get(namespace)}
                    source={sourceOf(byNamespace.get(namespace)?.credential.id ?? '')}
                    ciBinding={ciBindings.find((b) => b.namespace === namespace)}
                    canBind={canBind}
                    busy={replace.isPending}
                    onSet={() => setSetting(namespace)}
                    onUnbind={unbind}
                  />
                ))}
              </tbody>
            </table>
          </ScrollRegion>
        )}
      </Card>
      {canBind ? (
        <Modal
          open={setting !== null}
          onOpenChange={(open) => {
            if (!open) setSetting(null);
          }}
          title={t('credentials.binding.set.title', {
            namespace: setting ? t(`credentials.binding.namespaces.${setting}`, setting) : '',
          })}
          size="md"
        >
          {setting ? (
            <SetCredentialDialog
              namespace={setting}
              clientId={clientId}
              canManage={canManage}
              credentials={credentialList}
              busy={replace.isPending}
              onBind={(credentialId) => bind(setting, credentialId, () => setSetting(null))}
              onClose={() => setSetting(null)}
            />
          ) : null}
        </Modal>
      ) : null}
    </div>
  );
}

function NamespaceRow({
  ciId,
  namespace,
  entry,
  source,
  ciBinding,
  canBind,
  busy,
  onSet,
  onUnbind,
}: {
  ciId: string;
  namespace: CredentialNamespace;
  entry?: EffectiveCredential;
  source?: Credential['source'];
  ciBinding?: CredentialBinding;
  canBind: boolean;
  busy: boolean;
  onSet: () => void;
  onUnbind: (binding: CredentialBinding) => void;
}) {
  const { t } = useTranslation();
  const label = t(`credentials.binding.namespaces.${namespace}`, namespace);
  return (
    <tr className={trClasses} data-testid={`credential-namespace-${namespace}`}>
      <th scope="row" className={cn(tdPrimaryClasses, 'text-left font-medium')}>
        {label}
      </th>
      {entry ? (
        <>
          <td className={tdClasses}>
            <div className="flex items-center gap-2">
              <KeyRound className="size-4 shrink-0 text-fg-muted" aria-hidden />
              <span className="font-medium text-fg">{entry.credential.name}</span>
            </div>
            <div className="text-xs text-fg-muted">
              {t(`discovery.credentials.kinds.${entry.credential.kind}`, entry.credential.kind)}
              {source ? ` · ${t(`credentials.binding.sources.${source}`)}` : null}
            </div>
          </td>
          <td className={tdClasses}>
            <OriginLabel entry={entry} />
          </td>
          <td className={tdClasses}>
            <UsageCell credentialId={entry.credential.id} ciId={ciId} namespace={namespace} />
          </td>
        </>
      ) : (
        <td className={cn(tdClasses, 'text-fg-muted')} colSpan={3}>
          {t('credentials.binding.none')}
        </td>
      )}
      {canBind ? (
        <td className={cn(tdClasses, 'whitespace-nowrap text-right')}>
          <div className="flex justify-end gap-2">
            <Button
              size="sm"
              variant="secondary"
              icon={<Link2 />}
              onClick={onSet}
              aria-label={t('credentials.binding.set.actionNamed', { namespace: label })}
            >
              {t('credentials.binding.set.action')}
            </Button>
            {ciBinding ? (
              <Button
                size="sm"
                variant="ghost"
                icon={<Trash2 />}
                className="text-danger hover:text-danger"
                disabled={busy}
                onClick={() => onUnbind(ciBinding)}
                aria-label={t('credentials.binding.unbindNamed', { namespace: label })}
              >
                {t('credentials.binding.unbind')}
              </Button>
            ) : null}
          </div>
        </td>
      ) : null}
    </tr>
  );
}

/** OriginLabel names the level of the effective credential, with group name or CIDR. */
function OriginLabel({ entry }: { entry: EffectiveCredential }) {
  const { t } = useTranslation();
  const withTarget = entry.origin === 'device_group' || entry.origin === 'subnet';
  const target = useCredentialOriginTarget(withTarget ? entry.source_id : undefined);
  const origin = t(`credentials.binding.origins.${entry.origin}`, entry.origin);
  return (
    <span data-testid="credential-origin">
      {target.data ? `${origin}: ${target.data}` : origin}
    </span>
  );
}

/** UsageCell shows the last success and the last failure with its error class. */
function UsageCell({
  credentialId,
  ciId,
  namespace,
}: {
  credentialId: string;
  ciId: string;
  namespace: CredentialNamespace;
}) {
  const { t, i18n } = useTranslation();
  const usage = useCredentialUsage(credentialId, ciId);
  if (usage.isPending) return <span className="text-fg-muted">{t('app.loading')}</span>;
  if (usage.isError) {
    return <span className="text-fg-muted">{t('credentials.binding.usage.unavailable')}</span>;
  }
  const row = (usage.data?.data ?? []).find((u) => u.namespace === namespace);
  const success = formatDate(row?.last_success_at, i18n.language);
  const failure = formatDate(row?.last_failure_at, i18n.language);
  if (!success && !failure) {
    return <span className="text-fg-muted">{t('credentials.binding.usage.never')}</span>;
  }
  return (
    <div className="space-y-0.5 text-sm" data-testid="credential-usage">
      <div>
        {success
          ? t('credentials.binding.usage.lastSuccess', { at: success })
          : t('credentials.binding.usage.noSuccess')}
      </div>
      {failure ? (
        <div className="text-danger">
          {t('credentials.binding.usage.lastFailure', {
            at: failure,
            errorClass: row?.last_error_class
              ? t(`credentials.binding.errorClasses.${row.last_error_class}`, row.last_error_class)
              : t('credentials.binding.usage.unknownClass'),
          })}
        </div>
      ) : null}
    </div>
  );
}

/**
 * SetCredentialDialog offers the three ways to set the credential of a
 * namespace. Creating (device-specific or vault reference) needs
 * credential:manage; without it only the existing ones can be chosen.
 */
function SetCredentialDialog({
  namespace,
  clientId,
  canManage,
  credentials,
  busy,
  onBind,
  onClose,
}: {
  namespace: CredentialNamespace;
  clientId?: string;
  canManage: boolean;
  credentials: Credential[];
  busy: boolean;
  onBind: (credentialId: string) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const groupId = useId();
  const ways: SetWay[] = canManage ? ['create', 'existing', 'vault'] : ['existing'];
  const [way, setWay] = useState<SetWay>(ways[0] ?? 'existing');
  const [chosen, setChosen] = useState('');
  const [chooseError, setChooseError] = useState<string | undefined>();
  const kinds = CREDENTIAL_KINDS.filter((k) => KIND_NAMESPACE[k] === namespace);
  const clients = useClientOptions(canManage);
  // A credential of a client acts only on CIs of that client (CRD-04).
  const candidates = credentials.filter(
    (c) =>
      KIND_NAMESPACE[c.kind as CredentialKind] === namespace &&
      (!c.client_id || c.client_id === clientId),
  );
  // A CI without a client takes only organisation-wide credentials; the
  // server refuses a client credential on it (credential_binding_client_rule).
  const clientOptions = clientId
    ? (clients.data?.data ?? [])
        .filter((c) => c.id === clientId)
        .map((c) => ({ value: c.id, label: c.name }))
    : [];

  return (
    <div className="space-y-4">
      <fieldset className="space-y-2">
        <legend className="text-sm font-medium text-fg">{t('credentials.binding.set.way')}</legend>
        <div className="flex flex-col gap-2 text-sm text-fg sm:flex-row sm:flex-wrap sm:gap-4">
          {ways.map((value) => (
            <label key={value} className="inline-flex items-center gap-2">
              <input
                type="radio"
                name={`${groupId}-way`}
                value={value}
                checked={way === value}
                onChange={() => setWay(value)}
              />
              {t(`credentials.binding.set.ways.${value}`)}
            </label>
          ))}
        </div>
      </fieldset>
      {way === 'existing' ? (
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (!chosen) {
              setChooseError(t('credentials.binding.set.chooseRequired'));
              return;
            }
            onBind(chosen);
          }}
          noValidate
        >
          {candidates.length === 0 ? (
            <p className="text-sm text-fg-muted">{t('credentials.binding.set.noCandidates')}</p>
          ) : (
            <Select
              label={t('credentials.binding.set.existing')}
              value={chosen}
              onChange={(e) => {
                setChosen(e.target.value);
                setChooseError(undefined);
              }}
              error={chooseError}
              options={[
                { value: '', label: t('credentials.binding.set.choose') },
                ...candidates.map((c) => ({
                  value: c.id,
                  label: `${c.name} (${t(`discovery.credentials.kinds.${c.kind}`, c.kind)})`,
                })),
              ]}
            />
          )}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="secondary" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={busy || candidates.length === 0}>
              {t('credentials.binding.set.bind')}
            </Button>
          </div>
        </form>
      ) : (
        <CredentialForm
          key={way}
          clients={clientOptions}
          kinds={kinds}
          initialClientId={clientId ?? ''}
          allowVault
          initialSource={way === 'vault' ? 'vault_ref' : 'stored'}
          // Bound right after creating; a refused binding shows in the section.
          onCreated={(created) => onBind(created.id)}
          onClose={onClose}
        />
      )}
    </div>
  );
}
