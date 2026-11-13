/**
 * SubnetFormModal creates, edits and deletes a subnet (IPM-01 to IPM-07,
 * IPM-19, WP-916): section, CIDR, VRF, name, VLAN, gateway, nameserver set,
 * pool, client, site and description. Creating sends the filled fields
 * (an empty section or VRF means the organization's default); editing
 * sends only the changed fields (an empty VLAN or nameserver set clears
 * it). A 422 or 409 of the server names its field and appears at that
 * input; deleting a subnet with addresses or children says so.
 */
import { Trash2 } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError, type FieldErrorInfo } from '../../api/client';
import type { Subnet, SubnetCreate, SubnetUpdate } from '../../api/cmdb';
import { useClients } from '../../api/cmdbHooks';
import { useSites, useVrfs } from '../../api/hooks';
import {
  useDeleteSubnet,
  useIPAMSections,
  useNameserverSetOptions,
  useSaveSubnet,
  useVLANOptions,
} from '../../api/ipamHooks';
import { Button } from '../ui/Button';
import { Input } from '../ui/Input';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';
import { Textarea } from '../ui/Textarea';

export interface SubnetFormModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The subnet to edit; absent to create one. */
  subnet?: Subnet;
  /** The section a new subnet starts in. */
  sectionId?: string;
  /** Called with the saved subnet. */
  onSaved?: (subnet: Subnet) => void;
  /** Called after the subnet was deleted. */
  onDeleted?: () => void;
}

const FIELDS = [
  'section_id',
  'cidr',
  'vrf_id',
  'name',
  'vlan_id',
  'gateway',
  'nameserver_set_id',
  'client_id',
  'site_id',
  'description',
] as const;
type Field = (typeof FIELDS)[number];
type Values = Record<Field, string> & { is_pool: boolean };

function initial(subnet: Subnet | undefined, sectionId: string | undefined): Values {
  return {
    section_id: subnet?.section_id ?? sectionId ?? '',
    cidr: subnet?.cidr ?? '',
    vrf_id: subnet?.vrf_id ?? '',
    name: subnet?.name ?? '',
    vlan_id: subnet?.vlan_id ?? '',
    gateway: subnet?.gateway ?? '',
    nameserver_set_id: subnet?.nameserver_set_id ?? '',
    client_id: subnet?.client_id ?? '',
    site_id: subnet?.site_id ?? '',
    description: subnet?.description ?? '',
    is_pool: subnet?.is_pool ?? false,
  };
}

export function SubnetFormModal({
  open,
  onOpenChange,
  subnet,
  sectionId,
  onSaved,
  onDeleted,
}: SubnetFormModalProps) {
  const { t } = useTranslation();
  const [start] = useState(() => initial(subnet, sectionId));
  const [values, setValues] = useState<Values>(start);
  const [errors, setErrors] = useState<Record<string, FieldErrorInfo>>({});
  const [message, setMessage] = useState<string>();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const sections = useIPAMSections();
  const vrfs = useVrfs();
  const clients = useClients();
  const sites = useSites(values.client_id || undefined);
  const vlans = useVLANOptions(values.site_id || undefined);
  const nameservers = useNameserverSetOptions();
  const save = useSaveSubnet();
  const remove = useDeleteSubnet();

  const set = (field: Field, value: string) => setValues((v) => ({ ...v, [field]: value }));

  const failed = (err: unknown, fallback: string) => {
    if (err instanceof ApiError) {
      const fields = err.fieldErrors();
      const known = Object.keys(fields).filter((f) => (FIELDS as readonly string[]).includes(f));
      if (known.length > 0 || 'is_pool' in fields) {
        setErrors(fields);
        return;
      }
      setMessage(err.status === 403 ? t('ipam.subnetForm.forbidden') : (err.detail ?? fallback));
      return;
    }
    setMessage(fallback);
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setErrors({});
    setMessage(undefined);
    if (!values.cidr.trim()) {
      setErrors({ cidr: { kind: 'invalid', message: t('ipam.subnetForm.cidrRequired') } });
      return;
    }
    let data: SubnetCreate | SubnetUpdate;
    if (subnet) {
      const changed: Record<string, string | boolean> = {};
      for (const field of FIELDS) {
        if (values[field].trim() !== start[field]) changed[field] = values[field].trim();
      }
      if (values.is_pool !== start.is_pool) changed.is_pool = values.is_pool;
      data = changed as SubnetUpdate;
    } else {
      const created: Record<string, string | boolean> = { is_pool: values.is_pool };
      for (const field of FIELDS) {
        const value = values[field].trim();
        if (value !== '') created[field] = value;
      }
      data = { ...created, cidr: values.cidr.trim() } as SubnetCreate;
    }
    save.mutate(
      { id: subnet?.id, data },
      {
        onSuccess: (saved) => {
          onSaved?.(saved);
          onOpenChange(false);
        },
        onError: (err) => failed(err, t('ipam.subnetForm.saveFailed')),
      },
    );
  };

  const doDelete = () => {
    if (!subnet) return;
    setMessage(undefined);
    remove.mutate(subnet.id, {
      onSuccess: () => {
        onDeleted?.();
        onOpenChange(false);
      },
      onError: (err) => {
        setConfirmDelete(false);
        if (err instanceof ApiError && err.status === 409) {
          setMessage(err.detail ?? t('ipam.subnetForm.deleteInUse'));
          return;
        }
        setMessage(t('ipam.subnetForm.deleteFailed'));
      },
    });
  };

  const error = (field: string) => errors[field]?.message;
  const none = { value: '', label: t('ipam.subnetForm.none') };

  return (
    <Modal
      open={open}
      onOpenChange={onOpenChange}
      size="lg"
      title={
        subnet
          ? t('ipam.subnetForm.editTitle', { cidr: subnet.cidr })
          : t('ipam.subnetForm.createTitle')
      }
    >
      <form onSubmit={submit} className="space-y-4" noValidate>
        <div className="grid gap-3 sm:grid-cols-2">
          <Select
            label={t('ipam.subnetForm.section')}
            value={values.section_id}
            onChange={(event) => set('section_id', event.target.value)}
            error={error('section_id')}
            options={[
              { value: '', label: t('ipam.subnetForm.defaultSection') },
              ...(sections.data?.data ?? []).map((s) => ({ value: s.id, label: s.name })),
            ]}
          />
          <Input
            label={t('ipam.subnetForm.cidr')}
            value={values.cidr}
            onChange={(event) => set('cidr', event.target.value)}
            error={error('cidr')}
            placeholder="10.0.0.0/24"
            required
          />
          <Select
            label={t('ipam.subnetForm.vrf')}
            value={values.vrf_id}
            onChange={(event) => set('vrf_id', event.target.value)}
            error={error('vrf_id')}
            options={[
              { value: '', label: t('ipam.subnetForm.defaultVrf') },
              ...(vrfs.data?.data ?? [])
                .filter((v) => !v.is_default)
                .map((v) => ({ value: v.id, label: v.name })),
            ]}
          />
          <Input
            label={t('ipam.subnetForm.name')}
            value={values.name}
            onChange={(event) => set('name', event.target.value)}
            error={error('name')}
          />
          <Select
            label={t('ipam.subnetForm.client')}
            value={values.client_id}
            onChange={(event) => {
              set('client_id', event.target.value);
              set('site_id', '');
            }}
            error={error('client_id')}
            options={[
              { value: '', label: t('ipam.subnetForm.orgWide') },
              ...(clients.data?.data ?? []).map((c) => ({ value: c.id, label: c.name })),
            ]}
          />
          <Select
            label={t('ipam.subnetForm.site')}
            value={values.site_id}
            onChange={(event) => set('site_id', event.target.value)}
            error={error('site_id')}
            options={[
              none,
              ...(sites.data?.data ?? []).map((s) => ({ value: s.id, label: s.name })),
            ]}
          />
          <Select
            label={t('ipam.subnetForm.vlan')}
            value={values.vlan_id}
            onChange={(event) => set('vlan_id', event.target.value)}
            error={error('vlan_id')}
            options={[
              none,
              ...(vlans.data?.data ?? []).map((v) => ({
                value: v.id,
                label: `${v.vlan_id} ${v.name}`,
              })),
            ]}
          />
          <Input
            label={t('ipam.subnetForm.gateway')}
            value={values.gateway}
            onChange={(event) => set('gateway', event.target.value)}
            error={error('gateway')}
          />
          <Select
            label={t('ipam.subnetForm.nameserverSet')}
            value={values.nameserver_set_id}
            onChange={(event) => set('nameserver_set_id', event.target.value)}
            error={error('nameserver_set_id')}
            options={[
              none,
              ...(nameservers.data?.data ?? []).map((n) => ({ value: n.id, label: n.name })),
            ]}
          />
          <div className="flex items-end">
            <label className="flex items-center gap-2 text-sm text-fg">
              <input
                type="checkbox"
                checked={values.is_pool}
                onChange={(event) => setValues((v) => ({ ...v, is_pool: event.target.checked }))}
                aria-invalid={error('is_pool') ? true : undefined}
                aria-describedby={error('is_pool') ? 'subnet-form-is-pool-error' : undefined}
              />
              {t('ipam.subnetForm.isPool')}
            </label>
          </div>
        </div>
        {error('is_pool') && (
          <p id="subnet-form-is-pool-error" className="text-sm text-red-700 dark:text-red-300">
            {error('is_pool')}
          </p>
        )}
        <Textarea
          label={t('ipam.subnetForm.description')}
          value={values.description}
          onChange={(event) => set('description', event.target.value)}
          error={error('description')}
          rows={2}
        />
        {message && (
          <p role="alert" className="text-sm text-red-700 dark:text-red-300">
            {message}
          </p>
        )}
        <div className="flex flex-wrap items-center justify-between gap-2">
          {subnet ? (
            confirmDelete ? (
              <span className="flex items-center gap-2 text-sm text-fg">
                {t('ipam.subnetForm.deleteConfirm', { cidr: subnet.cidr })}
                <Button
                  type="button"
                  variant="danger"
                  size="sm"
                  loading={remove.isPending}
                  onClick={doDelete}
                >
                  {t('ipam.subnetForm.delete')}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => setConfirmDelete(false)}
                >
                  {t('ipam.subnetForm.cancel')}
                </Button>
              </span>
            ) : (
              <Button
                type="button"
                variant="ghost"
                icon={<Trash2 />}
                onClick={() => setConfirmDelete(true)}
              >
                {t('ipam.subnetForm.delete')}
              </Button>
            )
          ) : (
            <span />
          )}
          <Button type="submit" loading={save.isPending}>
            {subnet ? t('ipam.subnetForm.save') : t('ipam.subnetForm.create')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
