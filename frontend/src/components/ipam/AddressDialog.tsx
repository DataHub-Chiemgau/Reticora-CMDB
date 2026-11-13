/**
 * AddressDialog creates and edits an IP address of a subnet (IPM-13,
 * IPM-14, IPM-19, WP-917). The fields are the address (only when
 * creating), type, state (read only when a scan sets it, origin scan),
 * role, DNS name, description, the device chosen with the CI picker
 * (ObjectPicker, WP-859), the interface of that device (GET
 * /cis/{id}/interfaces; a new device clears it) and the pin of the
 * binding. The origin is shown, never edited. Creating sends POST /ips and,
 * when the pin is set, PATCH /ips/{id} with pinned (POST takes no pin);
 * editing sends only the changed fields. A 422 of the server (ci_id, role,
 * vrf_id, …) appears at its field. The tab History lists GET
 * /ips/{id}/history (HistoryList). An open duplicate_ip or
 * ip_binding_conflict item of the address is pointed out with a link into
 * the review inbox. Without ipam:write the dialog is read only.
 */
import { useId, useRef, useState, type FormEvent, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { ApiError, type FieldErrorInfo } from '../../api/client';
import {
  IP_ADDRESS_ROLES,
  IP_ADDRESS_STATES,
  IP_ADDRESS_TYPES,
  type IPAddress,
  type IPAddressCreate,
  type IPAddressRole,
  type IPAddressState,
  type IPAddressType,
  type IPAddressUpdate,
  type Subnet,
} from '../../api/cmdb';
import { useCI, useHasPermission } from '../../api/hooks';
import {
  IPAddressPinFailed,
  useAddressReviews,
  useCIInterfaces,
  useSaveIPAddress,
  useVRF,
} from '../../api/ipamHooks';
import { ObjectPicker, type PickedObject } from '../pickers/ObjectPicker';
import { Badge } from '../ui/Badge';
import { Button } from '../ui/Button';
import { Input } from '../ui/Input';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';
import { Textarea } from '../ui/Textarea';
import { cn } from '../ui/utils';
import { HistoryList } from './HistoryList';

export interface AddressDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The subnet of the address; a new address is created in it. */
  subnet: Pick<Subnet, 'id' | 'vrf_id' | 'cidr'>;
  /** The address to edit; absent to create one. */
  address?: IPAddress;
  /** Called with the saved address. */
  onSaved?: (address: IPAddress) => void;
}

interface Values {
  address: string;
  type: IPAddressType;
  state: IPAddressState | '';
  role: IPAddressRole;
  dns_name: string;
  description: string;
  ci_id: string;
  interface_id: string;
  pinned: boolean;
}

function initial(address: IPAddress | undefined): Values {
  return {
    address: address?.address ?? '',
    type: address?.type ?? 'static',
    state: address?.state ?? '',
    role: address?.role ?? 'primary',
    dns_name: address?.dns_name ?? '',
    description: address?.description ?? '',
    ci_id: address?.ci_id ?? '',
    interface_id: address?.interface_id ?? '',
    pinned: address?.pinned ?? false,
  };
}

/** The fields a field error of the server may name; others go to the message. */
const FIELDS = [
  'address',
  'type',
  'state',
  'role',
  'dns_name',
  'description',
  'ci_id',
  'interface_id',
  'vrf_id',
  'subnet_id',
  'pinned',
] as const;

type Tab = 'properties' | 'history';
const TABS: Tab[] = ['properties', 'history'];

export function AddressDialog({
  open,
  onOpenChange,
  subnet,
  address,
  onSaved,
}: AddressDialogProps) {
  const { t } = useTranslation();
  return (
    <Modal
      open={open}
      onOpenChange={onOpenChange}
      size="lg"
      title={
        address
          ? t('ipam.addressDialog.editTitle', { address: address.address })
          : t('ipam.addressDialog.createTitle')
      }
    >
      {/* The body mounts per address: the values of one never reach another. */}
      <AddressDialogBody
        key={address?.id ?? 'new'}
        subnet={subnet}
        address={address}
        onClose={() => onOpenChange(false)}
        onSaved={onSaved}
      />
    </Modal>
  );
}

function AddressDialogBody({
  subnet,
  address,
  onClose,
  onSaved,
}: {
  subnet: AddressDialogProps['subnet'];
  address?: IPAddress;
  onClose: () => void;
  onSaved?: (address: IPAddress) => void;
}) {
  const { t } = useTranslation();
  const tabsId = useId();
  const writeRight = useHasPermission('ipam:write');
  const canWrite = writeRight === true;
  const canReadReviews = useHasPermission('discovery:read') === true;
  const [tab, setTab] = useState<Tab>('properties');
  const refs = useRef<Partial<Record<Tab, HTMLButtonElement | null>>>({});

  const onTabKey = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const step = event.key === 'ArrowRight' ? 1 : event.key === 'ArrowLeft' ? -1 : 0;
    if (step === 0) return;
    event.preventDefault();
    const next = TABS[(index + step + TABS.length) % TABS.length];
    if (!next) return;
    setTab(next);
    refs.current[next]?.focus();
  };

  // The form waits for the rights: it must not flash read only (or writable).
  if (writeRight === undefined) {
    return <p className="text-sm text-fg-muted">{t('ipam.detail.loading')}</p>;
  }
  return (
    <div className="space-y-4">
      {address && canReadReviews && <ConflictHints address={address} />}
      {address && (
        <div
          role="tablist"
          aria-label={t('ipam.addressDialog.tabsLabel')}
          className="flex gap-1 border-b border-line"
        >
          {TABS.map((id, index) => (
            <button
              key={id}
              ref={(node) => {
                refs.current[id] = node;
              }}
              type="button"
              role="tab"
              id={`${tabsId}-tab-${id}`}
              aria-selected={tab === id}
              aria-controls={`${tabsId}-panel-${id}`}
              tabIndex={tab === id ? 0 : -1}
              onClick={() => setTab(id)}
              onKeyDown={(event) => onTabKey(event, index)}
              className={cn(
                '-mb-px border-b-2 px-3 py-2 text-sm font-medium focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent',
                tab === id
                  ? 'border-accent text-fg'
                  : 'border-transparent text-fg-muted hover:text-fg',
              )}
            >
              {id === 'properties'
                ? t('ipam.addressDialog.tabProperties')
                : t('ipam.addressDialog.tabHistory')}
            </button>
          ))}
        </div>
      )}
      <div
        role={address ? 'tabpanel' : undefined}
        id={`${tabsId}-panel-properties`}
        aria-labelledby={address ? `${tabsId}-tab-properties` : undefined}
        hidden={tab !== 'properties'}
      >
        <AddressForm
          subnet={subnet}
          address={address}
          canWrite={canWrite}
          onClose={onClose}
          onSaved={onSaved}
        />
      </div>
      {address && (
        <div
          role="tabpanel"
          id={`${tabsId}-panel-history`}
          aria-labelledby={`${tabsId}-tab-history`}
          hidden={tab !== 'history'}
        >
          <HistoryList kind="ip" id={address.id} enabled={tab === 'history'} />
        </div>
      )}
    </div>
  );
}

/** ConflictHints points out open duplicate_ip and ip_binding_conflict items of the address. */
function ConflictHints({ address }: { address: IPAddress }) {
  const { t } = useTranslation();
  const reviews = useAddressReviews(address);
  const items = reviews.data ?? [];
  if (items.length === 0) return null;
  const kinds = [...new Set(items.map((item) => item.kind))];
  return (
    <div
      role="note"
      aria-label={t('ipam.addressDialog.conflictTitle')}
      className="space-y-1 rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-sm text-fg"
    >
      {kinds.map((kind) => (
        <p key={kind}>
          {t(`ipam.addressDialog.conflict_${kind}`)}{' '}
          <Link to={`/reviews?kind=${kind}`} className="text-accent underline underline-offset-2">
            {t('ipam.addressDialog.openInbox')}
          </Link>
        </p>
      ))}
    </div>
  );
}

function AddressForm({
  subnet,
  address: given,
  canWrite,
  onClose,
  onSaved,
}: {
  subnet: AddressDialogProps['subnet'];
  address?: IPAddress;
  canWrite: boolean;
  onClose: () => void;
  onSaved?: (address: IPAddress) => void;
}) {
  const { t } = useTranslation();
  const ciErrorId = useId();
  // current is the edited address; after a create whose pin failed it is
  // the created one, so the form switches to editing.
  const [current, setCurrent] = useState(given);
  const address = current;
  const [start, setStart] = useState(() => initial(given));
  const [values, setValues] = useState<Values>(start);
  // The name of the chosen device; for a bound address it follows the CI.
  const [ciName, setCIName] = useState('');
  const [errors, setErrors] = useState<Record<string, FieldErrorInfo>>({});
  const [message, setMessage] = useState<string>();
  const bound = useCI(values.ci_id && !ciName ? values.ci_id : '');
  const interfaces = useCIInterfaces(values.ci_id || undefined);
  const vrf = useVRF(subnet.vrf_id);
  const save = useSaveIPAddress();
  // A scan sets the state of a scanned row; it is shown, not edited.
  const stateFromScan = address?.origin === 'scan';
  const readOnly = !canWrite;

  const set = <K extends keyof Values>(field: K, value: Values[K]) =>
    setValues((v) => ({ ...v, [field]: value }));

  const picked: PickedObject[] = values.ci_id
    ? [{ kind: 'ci', id: values.ci_id, name: ciName || bound.data?.name || values.ci_id }]
    : [];

  const onPick = (next: PickedObject[]) => {
    const chosen = next[next.length - 1];
    setCIName(chosen?.name ?? '');
    // A new device clears the interface: it belongs to the old one.
    setValues((v) => ({ ...v, ci_id: chosen?.id ?? '', interface_id: '' }));
  };

  const failed = (err: unknown) => {
    if (err instanceof ApiError) {
      const fields = err.fieldErrors();
      const known = Object.keys(fields).filter((f) => (FIELDS as readonly string[]).includes(f));
      if (known.length > 0) {
        setErrors(fields);
        return;
      }
      setMessage(
        err.status === 403
          ? t('ipam.addressDialog.forbidden')
          : (err.detail ?? t('ipam.addressDialog.saveFailed')),
      );
      return;
    }
    setMessage(t('ipam.addressDialog.saveFailed'));
  };

  const done = (saved: IPAddress) => {
    onSaved?.(saved);
    onClose();
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (readOnly) return;
    setErrors({});
    setMessage(undefined);
    if (!address) {
      if (!values.address.trim()) {
        setErrors({
          address: { kind: 'invalid', message: t('ipam.addressDialog.addressRequired') },
        });
        return;
      }
      const data: IPAddressCreate = {
        subnet_id: subnet.id,
        address: values.address.trim(),
        type: values.type,
        role: values.role,
      };
      if (values.dns_name.trim()) data.dns_name = values.dns_name.trim();
      if (values.description.trim()) data.description = values.description.trim();
      if (values.ci_id) data.ci_id = values.ci_id;
      if (values.interface_id) data.interface_id = values.interface_id;
      save.mutate(
        { data, pin: values.pinned },
        {
          onSuccess: done,
          onError: (err) => {
            if (err instanceof IPAddressPinFailed) {
              // The address exists: the dialog edits it from now on, so a
              // retry sends PATCH pinned instead of a second POST.
              onSaved?.(err.created);
              setCurrent(err.created);
              setStart(initial(err.created));
              setValues({ ...initial(err.created), pinned: true });
              setMessage(t('ipam.addressDialog.pinFailed'));
              return;
            }
            failed(err);
          },
        },
      );
      return;
    }
    const data: IPAddressUpdate = {};
    if (values.type !== start.type) data.type = values.type;
    if (!stateFromScan && values.state !== start.state && values.state !== '') {
      data.state = values.state;
    }
    if (values.role !== start.role) data.role = values.role;
    if (values.dns_name.trim() !== start.dns_name) data.dns_name = values.dns_name.trim();
    if (values.description.trim() !== start.description) {
      data.description = values.description.trim();
    }
    if (values.interface_id !== start.interface_id) data.interface_id = values.interface_id;
    if (values.ci_id !== start.ci_id) data.ci_id = values.ci_id;
    if (values.pinned !== start.pinned) data.pinned = values.pinned;
    if (Object.keys(data).length === 0) {
      onClose();
      return;
    }
    save.mutate({ id: address.id, data }, { onSuccess: done, onError: failed });
  };

  const error = (field: string) => errors[field]?.message;
  const interfaceOptions = [
    {
      value: '',
      label: !values.ci_id
        ? t('ipam.addressDialog.chooseDeviceFirst')
        : interfaces.isPending
          ? t('ipam.addressDialog.interfacesLoading')
          : t('ipam.addressDialog.noInterface'),
    },
    ...(interfaces.data?.data ?? []).map((nic) => ({
      value: nic.id,
      label: nic.mac ? `${nic.name} (${nic.mac})` : nic.name,
    })),
  ];
  // A bound interface outside the loaded page still shows by its id.
  if (values.interface_id && !interfaceOptions.some((o) => o.value === values.interface_id)) {
    interfaceOptions.push({ value: values.interface_id, label: values.interface_id });
  }

  return (
    <form onSubmit={submit} className="space-y-4" noValidate>
      {readOnly && <p className="text-sm text-fg-muted">{t('ipam.addressDialog.readOnly')}</p>}
      <div className="grid gap-3 sm:grid-cols-2">
        <Input
          label={t('ipam.addressDialog.address')}
          value={values.address}
          onChange={(event) => set('address', event.target.value)}
          error={error('address')}
          placeholder={subnet.cidr.split('/')[0]}
          readOnly={!!address || readOnly}
          required={!address}
        />
        <Input
          label={t('ipam.detail.vrf')}
          value={vrf.data?.name ?? subnet.vrf_id ?? ''}
          error={error('vrf_id')}
          readOnly
        />
        <Select
          label={t('ipam.addressDialog.type')}
          value={values.type}
          onChange={(event) => set('type', event.target.value as IPAddressType)}
          error={error('type')}
          disabled={readOnly}
          options={IP_ADDRESS_TYPES.map((type) => ({
            value: type,
            label: t(`ipam.types.${type}`),
          }))}
        />
        {address ? (
          <div className="space-y-1">
            <Select
              label={t('ipam.addressDialog.state')}
              value={values.state}
              onChange={(event) => set('state', event.target.value as IPAddressState)}
              error={error('state')}
              disabled={readOnly || stateFromScan}
              aria-describedby={stateFromScan ? `${ciErrorId}-state` : undefined}
              options={[
                ...(values.state === ''
                  ? [{ value: '', label: t('ipam.addressDialog.stateNone') }]
                  : []),
                ...IP_ADDRESS_STATES.map((state) => ({
                  value: state,
                  label: t(`ipam.states.${state}`),
                })),
              ]}
            />
            {stateFromScan && (
              <p id={`${ciErrorId}-state`} className="text-xs text-fg-muted">
                {t('ipam.addressDialog.stateFromScan')}
              </p>
            )}
          </div>
        ) : null}
        <Select
          label={t('ipam.addressDialog.role')}
          value={values.role}
          onChange={(event) => set('role', event.target.value as IPAddressRole)}
          error={error('role')}
          disabled={readOnly}
          options={IP_ADDRESS_ROLES.map((role) => ({
            value: role,
            label: t(`ipam.roles.${role}`),
          }))}
        />
        <Input
          label={t('ipam.addressDialog.dnsName')}
          value={values.dns_name}
          onChange={(event) => set('dns_name', event.target.value)}
          error={error('dns_name')}
          readOnly={readOnly}
        />
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-1">
          {readOnly ? (
            <div className="space-y-1.5 text-sm">
              <span className="block font-medium text-fg">{t('ipam.addressDialog.device')}</span>
              {values.ci_id ? (
                <Link
                  to={`/cmdb/${values.ci_id}`}
                  className="text-accent underline underline-offset-2"
                >
                  {picked[0]?.name}
                </Link>
              ) : (
                <span className="text-fg-muted">{t('ipam.detail.none')}</span>
              )}
            </div>
          ) : (
            <ObjectPicker
              label={t('ipam.addressDialog.device')}
              kinds={['ci']}
              value={picked}
              onChange={onPick}
            />
          )}
          {error('ci_id') && (
            <p id={ciErrorId} role="alert" className="text-sm text-danger">
              {error('ci_id')}
            </p>
          )}
        </div>
        <Select
          label={t('ipam.addressDialog.interface')}
          value={values.interface_id}
          onChange={(event) => set('interface_id', event.target.value)}
          error={
            error('interface_id') ??
            (interfaces.isError ? t('ipam.addressDialog.interfacesFailed') : undefined)
          }
          disabled={readOnly || !values.ci_id}
          options={interfaceOptions}
        />
      </div>
      <Textarea
        label={t('ipam.addressDialog.description')}
        value={values.description}
        onChange={(event) => set('description', event.target.value)}
        error={error('description')}
        readOnly={readOnly}
        rows={2}
      />
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="space-y-1">
          <label className="flex items-center gap-2 text-sm text-fg">
            <input
              type="checkbox"
              checked={values.pinned}
              onChange={(event) => set('pinned', event.target.checked)}
              disabled={readOnly}
              aria-describedby={`${ciErrorId}-pinned`}
            />
            {t('ipam.addressDialog.pinned')}
          </label>
          <p id={`${ciErrorId}-pinned`} className="text-xs text-fg-muted">
            {error('pinned') ?? t('ipam.addressDialog.pinnedHint')}
          </p>
        </div>
        {address && (
          <p className="flex items-center gap-2 text-sm text-fg-muted" data-testid="address-origin">
            {t('ipam.addressDialog.origin')}:<Badge>{t(`ipam.origins.${address.origin}`)}</Badge>
          </p>
        )}
      </div>
      {message && (
        <p role="alert" className="text-sm text-red-700 dark:text-red-300">
          {message}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={onClose}>
          {readOnly ? t('ipam.addressDialog.close') : t('ipam.addressDialog.cancel')}
        </Button>
        {!readOnly && (
          <Button type="submit" loading={save.isPending}>
            {t('ipam.addressDialog.save')}
          </Button>
        )}
      </div>
    </form>
  );
}
