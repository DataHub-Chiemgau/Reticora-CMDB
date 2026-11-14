/**
 * NameserverTab is the tab "Nameserver sets" of the IPAM page (IPM-05,
 * IPM-19, WP-918): the sets with name, servers and description. With
 * ipam:write and without client and site restriction (WP-905) sets are
 * created, changed and deleted; the servers are a list of IP addresses
 * the form checks before sending (one to eight). Deleting a set subnets
 * use (409) shows the hint of the answer.
 */
import { Pencil, Plus, Trash2 } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { ApiError, type FieldErrorInfo } from '../../api/client';
import type { NameserverSet } from '../../api/cmdb';
import { useNameserverSetMutations, useNameserverSets } from '../../api/ipamHooks';
import { ScrollRegion } from '../ScrollRegion';
import { Button, IconButton } from '../ui/Button';
import { Card } from '../ui/Card';
import { Input } from '../ui/Input';
import { Modal } from '../ui/Modal';
import { Textarea } from '../ui/Textarea';
import { tableClasses, tdClasses, thClasses, theadClasses, trClasses } from '../ui/table';

export interface NameserverTabProps {
  /** The caller holds ipam:write and has no client or site restriction. */
  canWrite: boolean;
}

const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;

/** isIPAddress checks an IPv4 or IPv6 address (without prefix length). */
export function isIPAddress(text: string): boolean {
  if (IPV4.test(text)) return true;
  if (!text.includes(':') || /[^0-9a-fA-F:.]/.test(text)) return false;
  try {
    // The URL parser normalizes valid IPv6 hosts and refuses others.
    return new URL(`http://[${text}]/`).hostname.length > 2;
  } catch {
    return false;
  }
}

/** NAMESERVER_MAX is the most servers a set holds. */
export const NAMESERVER_MAX = 8;

export function NameserverTab({ canWrite }: NameserverTabProps) {
  const { t } = useTranslation();
  const sets = useNameserverSets();
  const { remove } = useNameserverSetMutations();
  const [editing, setEditing] = useState<NameserverSet | 'new'>();
  const [message, setMessage] = useState<string>();
  const rows = sets.data?.pages.flatMap((p) => p.data ?? []) ?? [];

  const onDelete = (set: NameserverSet) => {
    setMessage(undefined);
    remove.mutate(set.id, {
      onError: (err) =>
        setMessage(
          err instanceof ApiError && err.detail ? err.detail : t('ipam.nameservers.deleteFailed'),
        ),
    });
  };

  return (
    <Card className="space-y-3 p-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <h2 className="text-base font-semibold text-fg">{t('ipam.nameservers.title')}</h2>
        {canWrite && (
          <Button icon={<Plus />} onClick={() => setEditing('new')}>
            {t('ipam.nameservers.new')}
          </Button>
        )}
      </div>
      {message && (
        <p role="alert" className="text-sm text-red-700 dark:text-red-300">
          {message}
        </p>
      )}
      {sets.isError ? (
        <p role="alert" className="text-sm text-red-700 dark:text-red-300">
          {t('ipam.nameservers.loadFailed')}
        </p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-fg-muted">
          {sets.isPending ? t('ipam.detail.loading') : t('ipam.nameservers.empty')}
        </p>
      ) : (
        <ScrollRegion label={t('ipam.nameservers.title')}>
          <table className={tableClasses}>
            <thead className={theadClasses}>
              <tr>
                <th scope="col" className={thClasses}>
                  {t('ipam.nameservers.name')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('ipam.nameservers.servers')}
                </th>
                <th scope="col" className={thClasses}>
                  {t('ipam.nameservers.description')}
                </th>
                {canWrite && (
                  <th scope="col" className={thClasses}>
                    <span className="sr-only">{t('ipam.nameservers.actions')}</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {rows.map((set) => (
                <tr key={set.id} className={trClasses} data-testid={`nameserver-row-${set.id}`}>
                  <td className={tdClasses}>
                    <span className="font-medium text-fg">{set.name}</span>
                  </td>
                  <td className={tdClasses}>{set.servers.join(', ')}</td>
                  <td className={tdClasses}>{set.description}</td>
                  {canWrite && (
                    <td className={tdClasses}>
                      <span className="flex gap-1">
                        <IconButton
                          icon={<Pencil />}
                          label={t('ipam.nameservers.edit', { name: set.name })}
                          onClick={() => setEditing(set)}
                        />
                        <IconButton
                          icon={<Trash2 />}
                          label={t('ipam.nameservers.delete', { name: set.name })}
                          onClick={() => onDelete(set)}
                        />
                      </span>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </ScrollRegion>
      )}
      {sets.hasNextPage && (
        <Button variant="ghost" size="sm" onClick={() => void sets.fetchNextPage()}>
          {t('ipam.tree.loadMore')}
        </Button>
      )}
      {editing && (
        <NameserverForm
          set={editing === 'new' ? undefined : editing}
          onClose={() => setEditing(undefined)}
        />
      )}
    </Card>
  );
}

function NameserverForm({ set, onClose }: { set?: NameserverSet; onClose: () => void }) {
  const { t } = useTranslation();
  const [name, setName] = useState(set?.name ?? '');
  const [servers, setServers] = useState((set?.servers ?? []).join('\n'));
  const [description, setDescription] = useState(set?.description ?? '');
  const [errors, setErrors] = useState<Record<string, FieldErrorInfo>>({});
  const [message, setMessage] = useState<string>();
  const { create, update } = useNameserverSetMutations();

  const failed = (err: unknown) => {
    if (err instanceof ApiError) {
      const fields: Record<string, FieldErrorInfo> = {};
      for (const [field, info] of Object.entries(err.fieldErrors())) {
        fields[field.replace(/\[\d+\]$/, '')] ??= info;
      }
      if (Object.keys(fields).length > 0) {
        setErrors(fields);
        return;
      }
      if (err.detail) {
        setMessage(err.detail);
        return;
      }
    }
    setMessage(t('ipam.nameservers.saveFailed'));
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setErrors({});
    setMessage(undefined);
    const list = servers
      .split(/[\s,]+/)
      .map((s) => s.trim())
      .filter(Boolean);
    const next: Record<string, FieldErrorInfo> = {};
    if (!name.trim()) next.name = { kind: 'invalid', message: t('ipam.nameservers.nameRequired') };
    const invalid = list.filter((s) => !isIPAddress(s));
    if (list.length === 0 || list.length > NAMESERVER_MAX) {
      next.servers = { kind: 'invalid', message: t('ipam.nameservers.serversCount') };
    } else if (invalid.length > 0) {
      next.servers = {
        kind: 'invalid',
        message: t('ipam.nameservers.serversInvalid', { servers: invalid.join(', ') }),
      };
    }
    if (Object.keys(next).length > 0) {
      setErrors(next);
      return;
    }
    const data = { name: name.trim(), servers: list, description: description.trim() };
    const done = { onSuccess: onClose, onError: failed };
    if (set) update.mutate({ id: set.id, data }, done);
    else create.mutate(data, done);
  };

  return (
    <Modal
      open
      onOpenChange={(open) => !open && onClose()}
      title={set ? t('ipam.nameservers.editTitle', { name: set.name }) : t('ipam.nameservers.new')}
    >
      <form onSubmit={submit} className="space-y-3" noValidate>
        <Input
          label={t('ipam.nameservers.name')}
          value={name}
          onChange={(event) => setName(event.target.value)}
          error={errors.name?.message}
        />
        <Textarea
          label={t('ipam.nameservers.serversInput')}
          value={servers}
          onChange={(event) => setServers(event.target.value)}
          error={errors.servers?.message}
          rows={3}
          placeholder={'10.0.0.53\n2001:db8::53'}
        />
        <Input
          label={t('ipam.nameservers.description')}
          value={description}
          onChange={(event) => setDescription(event.target.value)}
          error={errors.description?.message}
        />
        {message && (
          <p role="alert" className="text-sm text-red-700 dark:text-red-300">
            {message}
          </p>
        )}
        <div className="flex justify-end">
          <Button type="submit" loading={create.isPending || update.isPending}>
            {t('ipam.nameservers.save')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
