/**
 * CredentialsSection — the credentials of the discovery on the Discovery page
 * (UI-14, WP-856): the list shows name, kind, client and creation date;
 * creating a credential asks for the fields its kind needs, deleting asks
 * first. Reading needs credential:read, creating and deleting
 * credential:manage; the server decides every request.
 *
 * Secrets are write-only: their fields are password fields without
 * autocompletion (a private key is read from a file), nothing shows them,
 * and the form drops them from its state as soon as it sends them. No
 * response of the server contains a secret (SEC-01, COL-02).
 */
import { KeyRound, Plus, Trash2 } from 'lucide-react';
import { useId, useState } from 'react';
import type { FormEvent } from 'react';
import { useQuery } from '@tanstack/react-query';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import { ApiError } from '../../api/client';
import type { Credential, CredentialInput, CredentialKind } from '../../api/client';
import { clientApi } from '../../api/cmdb';
import {
  useCredentialMutations,
  useCredentials,
  useHasPermission,
  useVaultConnections,
} from '../../api/hooks';
import { ScrollRegion } from '../ScrollRegion';
import { Panel } from '../spatial/Panel';
import { Button, IconButton } from '../ui/Button';
import { Input, labelClasses } from '../ui/Input';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../ui/table';
import { cn } from '../ui/utils';

/** The kinds of a credential, in the order the form offers them. */
export const CREDENTIAL_KINDS: readonly CredentialKind[] = [
  'snmp_v2c',
  'snmp_v3',
  'ssh_password',
  'ssh_key',
  'redfish',
  'ipmi',
  'wmi',
  'nut',
  'nas',
  'api_token',
];

/** One field of the secret of a kind, under the key the collector plugins read. */
interface SecretField {
  key: string;
  /** text: shown while typed (a user name); secret: a password field; file: read from a file. */
  type: 'text' | 'secret' | 'select' | 'file';
  required: boolean;
  options?: { value: string; label: string }[];
}

/** The SNMPv3 protocols the collector allows (collector/plugins/snmp/v3.go). */
const AUTH_PROTOCOLS = [
  { value: 'sha', label: 'SHA' },
  { value: 'sha224', label: 'SHA-224' },
  { value: 'sha256', label: 'SHA-256' },
  { value: 'sha384', label: 'SHA-384' },
  { value: 'sha512', label: 'SHA-512' },
];
const PRIV_PROTOCOLS = [
  { value: 'aes', label: 'AES-128' },
  { value: 'aes192', label: 'AES-192' },
  { value: 'aes256', label: 'AES-256' },
];

const USERNAME: SecretField = { key: 'username', type: 'text', required: true };
const LOGIN: SecretField[] = [USERNAME, { key: 'password', type: 'secret', required: true }];

/** The fields of each kind, as the collector plugins read them (COL-02). */
export const SECRET_FIELDS: Record<CredentialKind, SecretField[]> = {
  snmp_v2c: [{ key: 'community', type: 'secret', required: true }],
  snmp_v3: [
    USERNAME,
    { key: 'auth_protocol', type: 'select', required: true, options: AUTH_PROTOCOLS },
    { key: 'auth_password', type: 'secret', required: true },
    { key: 'priv_protocol', type: 'select', required: true, options: PRIV_PROTOCOLS },
    { key: 'priv_password', type: 'secret', required: true },
  ],
  ssh_password: LOGIN,
  ssh_key: [
    USERNAME,
    { key: 'private_key', type: 'file', required: true },
    { key: 'passphrase', type: 'secret', required: false },
  ],
  redfish: LOGIN,
  ipmi: LOGIN,
  wmi: LOGIN,
  nut: LOGIN,
  nas: LOGIN,
  api_token: [{ key: 'token', type: 'secret', required: true }],
};

/** The secret a kind starts with: only the default protocols of SNMPv3. */
function initialSecret(kind: CredentialKind): Record<string, string> {
  return kind === 'snmp_v3' ? { auth_protocol: 'sha256', priv_protocol: 'aes' } : {};
}

/** readText reads a file as text (the private key of an SSH credential). */
function readText(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result ?? ''));
    reader.onerror = () => reject(reader.error ?? new Error('read failed'));
    reader.readAsText(file);
  });
}

/** describeRefusal names why a write failed: a missing right or the server's reason. */
export function describeRefusal(error: unknown, t: TFunction): string {
  if (error instanceof ApiError) {
    if (error.status === 403) return t('discovery.credentials.errors.forbidden');
    if (error.status === 404) return t('discovery.credentials.errors.notFound');
    return t('discovery.credentials.errors.reason', { detail: error.detail || error.message });
  }
  return t('discovery.credentials.errors.reason', {
    detail: error instanceof Error ? error.message : String(error),
  });
}

function formatDate(value: string | undefined, language: string) {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(language, { dateStyle: 'medium', timeStyle: 'short' }).format(
    date,
  );
}

/** The clients a credential can belong to; the same query as the scope banner. */
export function useClientOptions(enabled: boolean) {
  return useQuery({
    queryKey: ['scope', 'clients'],
    queryFn: () => clientApi.list(),
    enabled,
    retry: false,
    staleTime: 60_000,
  });
}

/**
 * VAULT_FIELDS are the fields a vault reference maps for each kind: the
 * required fields of the kind (KindFields in
 * backend/internal/credential/vault_ref.go, WP-894).
 */
export const VAULT_FIELDS: Record<CredentialKind, string[]> = {
  snmp_v2c: ['community'],
  snmp_v3: ['username'],
  ssh_password: ['username', 'password'],
  ssh_key: ['username', 'private_key'],
  redfish: ['username', 'password'],
  ipmi: ['username', 'password'],
  wmi: ['username', 'password'],
  nut: ['username', 'password'],
  nas: ['username', 'password'],
  api_token: ['token'],
};

/** The source of a new credential: the encrypted secret or a vault reference (CRD-02). */
export type CredentialSource = 'stored' | 'vault_ref';

export interface CredentialFormProps {
  clients: { value: string; label: string }[];
  onClose: () => void;
  /** Called with the created credential (its ID) before onClose. */
  onCreated?: (credential: Credential) => void;
  /**
   * Offers the choice stored | vault reference. Without it the source stays
   * stored (Discovery page until WP-924).
   */
  allowVault?: boolean;
  /** The source the form starts with (only with allowVault). */
  initialSource?: CredentialSource;
  /** The kinds offered (default all); the first is preselected. */
  kinds?: readonly CredentialKind[];
  /** The client preselected (the client of a CI). */
  initialClientId?: string;
}

/**
 * CredentialForm creates a credential: name, kind, optional client and
 * either the fields of the kind or, with allowVault and the source vault
 * reference, connection, path, version and the vault field of each
 * required field. The secret leaves the form state when it is sent; a
 * refusal asks to enter it again.
 */
export function CredentialForm({
  clients,
  onClose,
  onCreated,
  allowVault = false,
  initialSource = 'stored',
  kinds = CREDENTIAL_KINDS,
  initialClientId = '',
}: CredentialFormProps) {
  const { t } = useTranslation();
  const { create } = useCredentialMutations();
  const fileId = useId();
  const firstKind = kinds[0] ?? 'snmp_v2c';
  const [name, setName] = useState('');
  const [kind, setKind] = useState<CredentialKind>(firstKind);
  const [clientId, setClientId] = useState(initialClientId);
  const [source, setSource] = useState<CredentialSource>(allowVault ? initialSource : 'stored');
  const [secret, setSecret] = useState<Record<string, string>>(() => initialSecret(firstKind));
  const [vaultConnection, setVaultConnection] = useState('');
  const [vaultPath, setVaultPath] = useState('');
  const [vaultVersion, setVaultVersion] = useState('');
  const [fieldMap, setFieldMap] = useState<Record<string, string>>({});
  // A new key for the file field empties it together with the secret.
  const [fileKey, setFileKey] = useState(0);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [refusal, setRefusal] = useState<string | null>(null);
  const vault = source === 'vault_ref';
  const connections = useVaultConnections(allowVault && vault);

  const fields = SECRET_FIELDS[kind];
  const vaultFields = VAULT_FIELDS[kind];
  const label = (key: string) => t(`discovery.credentials.fields.${key}`);
  const setField = (key: string, value: string) => setSecret((s) => ({ ...s, [key]: value }));
  const clearSecret = (next: CredentialKind) => {
    setSecret(initialSecret(next));
    setFileKey((k) => k + 1);
  };
  // A connection of the credential's client or an org-wide one (WP-894).
  const connectionOptions = (connections.data ?? []).filter(
    (c) => !c.client_id || c.client_id === clientId,
  );

  const validate = (): Record<string, string> => {
    const found: Record<string, string> = {};
    if (!name.trim()) found.name = t('discovery.credentials.form.nameRequired');
    if (vault) {
      if (!vaultConnection) found.connection = t('credentials.binding.vault.connectionRequired');
      if (!vaultPath.trim()) found.path = t('credentials.binding.vault.pathRequired');
      if (vaultVersion.trim() && !/^[1-9][0-9]*$/.test(vaultVersion.trim())) {
        found.version = t('credentials.binding.vault.versionInvalid');
      }
      for (const key of vaultFields) {
        if (!fieldMap[key]?.trim()) {
          found[`map.${key}`] = t('discovery.credentials.form.fieldRequired', {
            field: label(key),
          });
        }
      }
      return found;
    }
    for (const field of fields) {
      const value = field.type === 'text' ? secret[field.key]?.trim() : secret[field.key];
      if (field.required && !value) {
        found[field.key] = t('discovery.credentials.form.fieldRequired', {
          field: label(field.key),
        });
      }
    }
    return found;
  };

  const buildInput = (): CredentialInput => {
    const base = { name: name.trim(), kind, ...(clientId ? { client_id: clientId } : {}) };
    if (vault) {
      const map: Record<string, string> = {};
      for (const key of vaultFields) map[key] = (fieldMap[key] ?? '').trim();
      const version = vaultVersion.trim();
      return {
        ...base,
        source: 'vault_ref',
        vault_ref: {
          connection_id: vaultConnection,
          path: vaultPath.trim(),
          ...(version ? { version: Number(version) } : {}),
          field_map: map,
        },
      };
    }
    const payload: Record<string, string> = {};
    for (const field of fields) {
      const value = field.type === 'text' ? secret[field.key]?.trim() : secret[field.key];
      if (value) payload[field.key] = value;
    }
    return { ...base, source: 'stored', secret: payload };
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const found = validate();
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    const input = buildInput();
    setRefusal(null);
    // The secret is sent once and leaves the form state right away.
    clearSecret(kind);
    create.mutate(input, {
      onSuccess: (created) => {
        onCreated?.(created);
        onClose();
      },
      onError: (err) => {
        if (err instanceof ApiError && err.status === 422 && err.field) {
          setErrors({ [formField(err.field)]: err.detail || err.message });
        }
        setRefusal(describeRefusal(err, t));
      },
    });
  };

  return (
    <form className="space-y-4" onSubmit={submit} noValidate autoComplete="off">
      <Input
        label={t('discovery.credentials.columns.name')}
        value={name}
        onChange={(e) => setName(e.target.value)}
        error={errors.name}
        autoComplete="off"
      />
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <Select
          label={t('discovery.credentials.columns.kind')}
          value={kind}
          onChange={(e) => {
            const next = e.target.value as CredentialKind;
            setKind(next);
            setErrors({});
            clearSecret(next);
            setFieldMap({});
          }}
          options={kinds.map((k) => ({
            value: k,
            label: t(`discovery.credentials.kinds.${k}`),
          }))}
        />
        <Select
          label={t('discovery.credentials.columns.client')}
          value={clientId}
          onChange={(e) => {
            setClientId(e.target.value);
            setVaultConnection('');
          }}
          options={[{ value: '', label: t('discovery.credentials.allClients') }, ...clients]}
        />
      </div>
      {allowVault ? (
        <fieldset className="space-y-2">
          <legend className={labelClasses}>{t('credentials.binding.vault.sourceLegend')}</legend>
          <div className="flex flex-wrap gap-4 text-sm text-fg">
            {(['stored', 'vault_ref'] as const).map((value) => (
              <label key={value} className="inline-flex items-center gap-2">
                <input
                  type="radio"
                  name={`${fileId}-source`}
                  value={value}
                  checked={source === value}
                  onChange={() => {
                    setSource(value);
                    setErrors({});
                    clearSecret(kind);
                  }}
                />
                {t(`credentials.binding.sources.${value}`)}
              </label>
            ))}
          </div>
        </fieldset>
      ) : null}
      {vault ? (
        <fieldset className="space-y-4 rounded-lg border border-line p-3">
          <legend className="px-1 text-sm font-medium text-fg">
            {t('credentials.binding.vault.legend')}
          </legend>
          <p className="text-xs text-fg-muted">{t('credentials.binding.vault.hint')}</p>
          <Select
            label={t('credentials.binding.vault.connection')}
            value={vaultConnection}
            onChange={(e) => setVaultConnection(e.target.value)}
            error={errors.connection}
            options={[
              { value: '', label: t('credentials.binding.vault.chooseConnection') },
              ...connectionOptions.map((c) => ({ value: c.id, label: c.name })),
            ]}
          />
          {connections.isSuccess && connectionOptions.length === 0 ? (
            <p className="text-xs text-fg-muted">{t('credentials.binding.vault.noConnections')}</p>
          ) : null}
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Input
              label={t('credentials.binding.vault.path')}
              value={vaultPath}
              onChange={(e) => setVaultPath(e.target.value)}
              error={errors.path}
              autoComplete="off"
              spellCheck={false}
            />
            <Input
              label={t('discovery.credentials.form.optional', {
                field: t('credentials.binding.vault.version'),
              })}
              inputMode="numeric"
              value={vaultVersion}
              onChange={(e) => setVaultVersion(e.target.value)}
              error={errors.version}
              autoComplete="off"
            />
          </div>
          <fieldset className="space-y-3">
            <legend className={labelClasses}>{t('credentials.binding.vault.fieldMap')}</legend>
            {errors.field_map ? <p className="text-sm text-danger">{errors.field_map}</p> : null}
            {vaultFields.map((key) => (
              <Input
                key={`${kind}-map-${key}`}
                label={t('credentials.binding.vault.mapField', { field: label(key) })}
                value={fieldMap[key] ?? ''}
                onChange={(e) => setFieldMap((m) => ({ ...m, [key]: e.target.value }))}
                error={errors[`map.${key}`]}
                autoComplete="off"
                spellCheck={false}
              />
            ))}
          </fieldset>
        </fieldset>
      ) : (
        <fieldset className="space-y-4 rounded-lg border border-line p-3">
          <legend className="px-1 text-sm font-medium text-fg">
            {t('discovery.credentials.form.secretLegend')}
          </legend>
          <p className="text-xs text-fg-muted">{t('discovery.credentials.form.secretHint')}</p>
          {fields.map((field) => {
            if (field.type === 'select') {
              return (
                <Select
                  key={`${kind}-${field.key}`}
                  label={label(field.key)}
                  value={secret[field.key] ?? ''}
                  onChange={(e) => setField(field.key, e.target.value)}
                  options={field.options ?? []}
                />
              );
            }
            if (field.type === 'file') {
              const errorId = errors[field.key] ? `${fileId}-error` : undefined;
              return (
                <div key={`${kind}-${field.key}-${fileKey}`} className="space-y-1.5">
                  <label htmlFor={fileId} className={labelClasses}>
                    {label(field.key)}
                  </label>
                  <input
                    id={fileId}
                    type="file"
                    autoComplete="off"
                    aria-invalid={errors[field.key] ? true : undefined}
                    aria-describedby={errorId}
                    className="block w-full text-sm text-fg file:mr-3 file:rounded-lg file:border file:border-control file:bg-panel file:px-3 file:py-1.5 file:text-sm file:text-fg"
                    onChange={(e) => {
                      const file = e.target.files?.[0];
                      if (!file) {
                        setField(field.key, '');
                        return;
                      }
                      void readText(file).then(
                        (text) => setField(field.key, text),
                        () => setField(field.key, ''),
                      );
                    }}
                  />
                  <p className="text-xs text-fg-muted">{t('discovery.credentials.form.keyHint')}</p>
                  {errors[field.key] ? (
                    <p id={errorId} className="text-sm text-danger">
                      {errors[field.key]}
                    </p>
                  ) : null}
                </div>
              );
            }
            return (
              <Input
                key={`${kind}-${field.key}`}
                label={
                  field.required
                    ? label(field.key)
                    : t('discovery.credentials.form.optional', { field: label(field.key) })
                }
                type={field.type === 'secret' ? 'password' : 'text'}
                autoComplete="off"
                spellCheck={false}
                value={secret[field.key] ?? ''}
                onChange={(e) => setField(field.key, e.target.value)}
                error={errors[field.key]}
              />
            );
          })}
        </fieldset>
      )}
      {refusal ? (
        <div role="alert" className="space-y-1 text-sm text-danger">
          <p>{refusal}</p>
          {vault ? null : <p>{t('discovery.credentials.form.reenter')}</p>}
        </div>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={onClose}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" disabled={create.isPending}>
          {create.isPending ? t('app.loading') : t('common.save')}
        </Button>
      </div>
    </form>
  );
}

/** formField maps the field of a 422 to the input of the form that shows it. */
function formField(field: string): string {
  if (field.startsWith('vault_ref.connection')) return 'connection';
  if (field.startsWith('vault_ref.path')) return 'path';
  if (field.startsWith('vault_ref.version')) return 'version';
  if (field.startsWith('vault_ref.field_map') || field === 'field_map') return 'field_map';
  if (field.startsWith('secret.')) return field.slice('secret.'.length);
  return field;
}

/** CredentialsSection lists the credentials and offers creating and deleting them. */
export function CredentialsSection() {
  const { t, i18n } = useTranslation();
  const canRead = useHasPermission('credential:read') === true;
  const canManage = useHasPermission('credential:manage') === true;
  const credentials = useCredentials(canRead);
  const clients = useClientOptions(canRead);
  const { remove } = useCredentialMutations();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<Credential | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  if (!canRead) return null;

  const clientList = clients.data?.data ?? [];
  const clientName = (id?: string) =>
    id
      ? (clientList.find((c) => c.id === id)?.name ?? id)
      : t('discovery.credentials.organizationWide');
  const items = credentials.data?.data ?? [];

  const confirmDelete = () => {
    if (!deleting) return;
    const credential = deleting;
    setActionError(null);
    remove.mutate(credential.id, {
      onSettled: () => setDeleting(null),
      onError: (err) =>
        setActionError(
          t('discovery.credentials.errors.deleteFailed', {
            name: credential.name,
            reason: describeRefusal(err, t),
          }),
        ),
    });
  };

  return (
    <Panel
      title={t('discovery.credentials.title')}
      leading={<KeyRound className="text-accent" />}
      actions={
        canManage ? (
          <Button size="sm" variant="secondary" icon={<Plus />} onClick={() => setCreating(true)}>
            {t('discovery.credentials.add')}
          </Button>
        ) : null
      }
      padded={false}
    >
      <p className="px-4 pt-3 text-sm text-fg-muted">{t('discovery.credentials.intro')}</p>
      {actionError ? (
        <p role="alert" className="px-4 pt-2 text-sm text-danger">
          {actionError}
        </p>
      ) : null}
      {credentials.isError ? (
        <p role="alert" className="px-4 py-3 text-sm text-danger">
          {t('discovery.credentials.loadFailed')}
        </p>
      ) : items.length === 0 ? (
        <p className="px-4 py-3 text-sm text-fg-muted">
          {credentials.isPending ? t('app.loading') : t('discovery.credentials.none')}
        </p>
      ) : (
        <ScrollRegion label={t('discovery.credentials.listLabel')} className="mt-3">
          <table className={cn(tableClasses, 'min-w-[36rem]')}>
            <thead className={cn(theadClasses, 'border-t border-line')}>
              <tr>
                <th scope="col" className={cn(thClasses, 'text-fg-muted')}>
                  {t('discovery.credentials.columns.name')}
                </th>
                <th scope="col" className={cn(thClasses, 'text-fg-muted')}>
                  {t('discovery.credentials.columns.kind')}
                </th>
                <th scope="col" className={cn(thClasses, 'text-fg-muted')}>
                  {t('discovery.credentials.columns.client')}
                </th>
                <th scope="col" className={cn(thClasses, 'text-fg-muted')}>
                  {t('discovery.credentials.columns.createdAt')}
                </th>
                {canManage ? (
                  <th scope="col" className={cn(thClasses, 'text-right text-fg-muted')}>
                    {t('common.actions')}
                  </th>
                ) : null}
              </tr>
            </thead>
            <tbody>
              {items.map((c) => (
                <tr key={c.id} className={trClasses}>
                  <td className={tdPrimaryClasses}>{c.name}</td>
                  <td className={tdClasses}>
                    {t(`discovery.credentials.kinds.${c.kind}`, c.kind)}
                  </td>
                  <td className={tdClasses}>{clientName(c.client_id)}</td>
                  <td className={cn(tdClasses, 'whitespace-nowrap tabular-nums')}>
                    {formatDate(c.created_at, i18n.language)}
                  </td>
                  {canManage ? (
                    <td className={cn(tdClasses, 'text-right')}>
                      <IconButton
                        size="sm"
                        label={t('discovery.credentials.deleteNamed', { name: c.name })}
                        icon={<Trash2 />}
                        className="text-danger hover:text-danger"
                        onClick={() => setDeleting(c)}
                      />
                    </td>
                  ) : null}
                </tr>
              ))}
            </tbody>
          </table>
        </ScrollRegion>
      )}
      {canManage ? (
        <Modal
          open={creating}
          onOpenChange={(o) => {
            if (!o) setCreating(false);
          }}
          title={t('discovery.credentials.form.title')}
        >
          {creating ? (
            <CredentialForm
              clients={clientList.map((c) => ({ value: c.id, label: c.name }))}
              onClose={() => setCreating(false)}
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
          title={t('discovery.credentials.confirmDelete.title')}
          description={t('discovery.credentials.confirmDelete.text', {
            name: deleting?.name ?? '',
          })}
        >
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setDeleting(null)}>
              {t('common.cancel')}
            </Button>
            <Button variant="danger" disabled={remove.isPending} onClick={confirmDelete}>
              {t('discovery.credentials.confirmDelete.confirm')}
            </Button>
          </div>
        </Modal>
      ) : null}
    </Panel>
  );
}
