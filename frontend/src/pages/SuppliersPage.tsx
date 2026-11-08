import { useMemo, useState } from 'react';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useQueryClient } from '@tanstack/react-query';
import { Pencil, Plus, Search, Trash2, Truck } from 'lucide-react';
import {
  ApiError,
  supplierApi,
  type FieldErrorInfo,
  type Supplier,
  type SupplierCreateRequest,
  type SupplierUpdateRequest,
} from '../api/client';
import {
  useContacts,
  useCreateSupplier,
  useDeleteSupplier,
  useHasPermission,
  useSuppliers,
  useUpdateSupplier,
} from '../api/hooks';
import { useClients } from '../api/cmdbHooks';
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { Textarea } from '../components/ui/Textarea';
import { Toolbar, ToolbarEnd } from '../components/ui/Toolbar';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { ScrollRegion } from '../components/ScrollRegion';
import { useDebouncedValue } from '../components/pickers/ObjectPicker';

/** PAGE_SIZE is the page of the supplier list (limit, API-02). */
const PAGE_SIZE = 50;

/**
 * refusal words a refused supplier write: 409 for a taken name, 422 on
 * delete for a supplier still referenced, 403 for a missing contract:write.
 */
function refusal(
  error: unknown,
  t: TFunction,
  action: 'save' | 'delete',
  fields: Record<string, FieldErrorInfo> = {},
): string {
  if (error instanceof ApiError) {
    if (error.status === 409) return t('supplier.errors.nameTaken');
    if (error.status === 403) return t('supplier.errors.forbidden');
    if (error.status === 404) return t('supplier.errors.notFound');
    if (error.status === 422 && action === 'delete') return t('supplier.errors.inUse');
    if ((error.status === 422 || error.status === 400) && Object.keys(fields).length > 0) {
      return t('supplier.errors.invalid');
    }
    return t('supplier.errors.reason', { detail: error.detail ?? error.message });
  }
  return t('supplier.errors.reason', {
    detail: error instanceof Error ? error.message : String(error),
  });
}

type DialogState = { mode: 'create' } | { mode: 'edit'; supplier: Supplier };

/**
 * SuppliersPage is the supplier administration (MGT-12, UI-14, WP-808): page
 * by page over limit and cursor, searched by filter[name][like] and sorted
 * by name, with create, edit and delete for holders of contract:write.
 * Suppliers carry no scope filter: org-wide ones serve every client.
 */
export function SuppliersPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const mayWrite = useHasPermission('contract:write') === true;
  const [search, setSearch] = useState('');
  const debounced = useDebouncedValue(search.trim());
  // The cursors of the pages shown so far; the last one is the current page.
  const [cursors, setCursors] = useState<(string | undefined)[]>([undefined]);
  const cursor = cursors[cursors.length - 1];
  const suppliers = useSuppliers(
    {
      limit: PAGE_SIZE,
      sort: 'name',
      filter: debounced ? [{ field: 'name', op: 'like', value: debounced }] : [],
      ...(cursor ? { cursor } : {}),
    },
    { keepPrevious: true },
  );
  const contacts = useContacts();
  const contactNames = useMemo(
    () => new Map((contacts.data?.data ?? []).map((c) => [c.id, c.display_name])),
    [contacts.data],
  );
  const clients = useClients();
  const clientNames = useMemo(
    () => new Map((clients.data?.data ?? []).map((c) => [c.id, c.name])),
    [clients.data],
  );
  const [dialog, setDialog] = useState<DialogState | null>(null);
  const [deleting, setDeleting] = useState<Supplier | null>(null);

  const rows = suppliers.data?.data ?? [];
  const nextCursor = suppliers.data?.next_cursor;
  const forbidden = suppliers.error instanceof ApiError && suppliers.error.status === 403;

  const reload = async (id: string) => {
    const fresh = await supplierApi.get(id);
    void queryClient.invalidateQueries({ queryKey: ['suppliers'] });
    setDialog({ mode: 'edit', supplier: fresh });
  };

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('supplier.title')}
        description={t('supplier.subtitle')}
        actions={
          mayWrite ? (
            <Button icon={<Plus />} onClick={() => setDialog({ mode: 'create' })}>
              {t('supplier.create')}
            </Button>
          ) : null
        }
      />
      {!mayWrite ? <p className="text-sm text-fg-muted">{t('supplier.readOnly')}</p> : null}

      <Toolbar>
        <Input
          type="search"
          icon={<Search />}
          placeholder={t('supplier.search')}
          aria-label={t('supplier.search')}
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
            setCursors([undefined]);
          }}
          className="w-full sm:w-64"
        />
        {suppliers.data ? (
          <ToolbarEnd>
            <span className="text-sm text-fg-muted tabular-nums">
              {suppliers.data.total} {t('common.entries')}
            </span>
          </ToolbarEnd>
        ) : null}
      </Toolbar>

      <Card className="overflow-hidden p-0">
        {suppliers.isLoading ? (
          <div className="p-5">
            <SkeletonList rows={4} label={t('app.loading')} />
          </div>
        ) : forbidden ? (
          <div className="p-5">
            <ErrorState title={t('supplier.forbiddenRead')} />
          </div>
        ) : suppliers.error ? (
          <div className="p-5">
            <ErrorState
              title={t('supplier.loadError')}
              retryLabel={t('common.retry')}
              onRetry={() => void suppliers.refetch()}
            />
          </div>
        ) : rows.length === 0 ? (
          <div className="p-5">
            <EmptyState icon={<Truck />} title={t('supplier.empty')} />
          </div>
        ) : (
          <ScrollRegion label={t('supplier.title')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('supplier.fields.name')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('supplier.fields.externalRef')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('supplier.fields.client')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('supplier.fields.contact')}
                  </th>
                  {mayWrite ? (
                    <th scope="col" className={thClasses}>
                      <span className="sr-only">{t('common.actions')}</span>
                    </th>
                  ) : null}
                </tr>
              </thead>
              <tbody>
                {rows.map((supplier) => (
                  <tr key={supplier.id} className={trClasses}>
                    <td className={tdPrimaryClasses}>
                      <span className="[overflow-wrap:anywhere]">{supplier.name}</span>
                    </td>
                    <td className={tdClasses}>{supplier.external_ref ?? '–'}</td>
                    <td className={tdClasses}>
                      {supplier.client_id
                        ? (clientNames.get(supplier.client_id) ?? supplier.client_id)
                        : t('supplier.orgWide')}
                    </td>
                    <td className={tdClasses}>
                      {supplier.contact_id ? (contactNames.get(supplier.contact_id) ?? '–') : '–'}
                    </td>
                    {mayWrite ? (
                      <td className={tdClasses}>
                        <span className="flex justify-end gap-1">
                          <IconButton
                            size="sm"
                            icon={<Pencil />}
                            label={t('supplier.editLabel', { name: supplier.name })}
                            onClick={() => setDialog({ mode: 'edit', supplier })}
                          />
                          <IconButton
                            size="sm"
                            icon={<Trash2 />}
                            label={t('supplier.deleteLabel', { name: supplier.name })}
                            onClick={() => setDeleting(supplier)}
                            className="hover:text-danger"
                          />
                        </span>
                      </td>
                    ) : null}
                  </tr>
                ))}
              </tbody>
            </table>
          </ScrollRegion>
        )}
        {cursors.length > 1 || nextCursor ? (
          <div className="flex justify-end gap-2 border-t border-line px-5 py-3">
            <Button
              variant="secondary"
              size="sm"
              disabled={cursors.length <= 1}
              onClick={() => setCursors((c) => c.slice(0, -1))}
            >
              {t('common.back')}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              disabled={!nextCursor}
              onClick={() => nextCursor && setCursors((c) => [...c, nextCursor])}
            >
              {t('common.next')}
            </Button>
          </div>
        ) : null}
      </Card>

      {dialog ? (
        <SupplierDialog
          key={dialog.mode === 'edit' ? `${dialog.supplier.id}:${dialog.supplier.version}` : 'new'}
          state={dialog}
          onClose={() => setDialog(null)}
          onReload={reload}
        />
      ) : null}
      <DeleteSupplierDialog supplier={deleting} onClose={() => setDeleting(null)} />
    </div>
  );
}

interface SupplierForm {
  name: string;
  external_ref: string;
  client_id: string;
  contact_id: string;
  notes: string;
}

function formOf(supplier?: Supplier): SupplierForm {
  return {
    name: supplier?.name ?? '',
    external_ref: supplier?.external_ref ?? '',
    client_id: supplier?.client_id ?? '',
    contact_id: supplier?.contact_id ?? '',
    notes: supplier?.notes ?? '',
  };
}

function orNull(value: string): string | null {
  const trimmed = value.trim();
  return trimmed === '' ? null : trimmed;
}

interface SupplierDialogProps {
  state: DialogState;
  onClose: () => void;
  /** onReload reads the supplier again after a 412 and reopens the dialog with it. */
  onReload: (id: string) => Promise<void>;
}

/**
 * SupplierDialog creates a supplier or changes one: a change sends the
 * changed fields with If-Match from version; a 412 offers to reload, a 409
 * marks name, a 422 the named field. The contact person is chosen from
 * GET /contacts by display_name.
 */
function SupplierDialog({ state, onClose, onReload }: SupplierDialogProps) {
  const { t } = useTranslation();
  const editing = state.mode === 'edit' ? state.supplier : undefined;
  const [form, setForm] = useState<SupplierForm>(() => formOf(editing));
  const [fieldErrors, setFieldErrors] = useState<Record<string, FieldErrorInfo>>({});
  const [notice, setNotice] = useState<string>();
  const [stale, setStale] = useState(false);
  const [reloading, setReloading] = useState(false);
  const contacts = useContacts();
  const clients = useClients();
  const create = useCreateSupplier();
  const update = useUpdateSupplier();
  const saving = create.isPending || update.isPending;
  const set = (key: keyof SupplierForm) => (value: string) =>
    setForm((current) => ({ ...current, [key]: value }));

  const fieldError = (field: string): string | undefined => {
    const info = fieldErrors[field];
    if (!info) return undefined;
    if (info.kind === 'conflict') return t('supplier.errors.nameTaken');
    if (info.kind === 'forbidden') return t('supplier.errors.forbidden');
    return info.message;
  };

  const fail = (error: unknown) => {
    if (error instanceof ApiError && error.status === 412) {
      setStale(true);
      return;
    }
    const fields = error instanceof ApiError ? error.fieldErrors() : {};
    setFieldErrors(fields);
    setNotice(refusal(error, t, 'save', fields));
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setNotice(undefined);
    setFieldErrors({});
    setStale(false);
    const values = {
      name: form.name.trim(),
      external_ref: orNull(form.external_ref),
      contact_id: orNull(form.contact_id),
      notes: orNull(form.notes),
    };
    if (editing) {
      const before = formOf(editing);
      const previous = {
        name: before.name,
        external_ref: orNull(before.external_ref),
        contact_id: orNull(before.contact_id),
        notes: orNull(before.notes),
      };
      const changed: SupplierUpdateRequest = {};
      for (const key of Object.keys(values) as (keyof typeof values)[]) {
        if (values[key] !== previous[key]) {
          (changed as Record<string, unknown>)[key] = values[key];
        }
      }
      if (Object.keys(changed).length === 0) {
        onClose();
        return;
      }
      update.mutate(
        { id: editing.id, data: changed, version: editing.version },
        { onSuccess: onClose, onError: fail },
      );
      return;
    }
    const data: SupplierCreateRequest = { name: values.name };
    if (values.external_ref) data.external_ref = values.external_ref;
    if (values.contact_id) data.contact_id = values.contact_id;
    if (values.notes) data.notes = values.notes;
    if (form.client_id) data.client_id = form.client_id;
    create.mutate(data, { onSuccess: onClose, onError: fail });
  };

  const contactRows = contacts.data?.data ?? [];
  const clientRows = clients.data?.data ?? [];

  return (
    <Modal
      open
      onOpenChange={(open) => (open ? undefined : onClose())}
      title={editing ? t('supplier.editTitle') : t('supplier.createTitle')}
    >
      <form className="space-y-4" onSubmit={submit} noValidate>
        <Input
          label={t('supplier.fields.name')}
          value={form.name}
          required
          onChange={(e) => set('name')(e.target.value)}
          error={fieldError('name')}
        />
        <Input
          label={t('supplier.fields.externalRef')}
          value={form.external_ref}
          onChange={(e) => set('external_ref')(e.target.value)}
          error={fieldError('external_ref')}
        />
        <div>
          <Select
            label={t('supplier.fields.client')}
            value={form.client_id}
            disabled={!!editing}
            onChange={(e) => set('client_id')(e.target.value)}
            error={fieldError('client_id')}
            options={[
              { value: '', label: t('supplier.orgWide') },
              ...clientRows.map((c) => ({ value: c.id, label: c.name })),
              ...(form.client_id && !clientRows.some((c) => c.id === form.client_id)
                ? [{ value: form.client_id, label: form.client_id }]
                : []),
            ]}
          />
          {editing ? (
            <p className="mt-1 text-sm text-fg-muted">{t('supplier.clientFixed')}</p>
          ) : null}
        </div>
        <Select
          label={t('supplier.fields.contact')}
          value={form.contact_id}
          onChange={(e) => set('contact_id')(e.target.value)}
          error={fieldError('contact_id')}
          options={[
            { value: '', label: t('supplier.noContact') },
            ...contactRows.map((c) => ({ value: c.id, label: c.display_name })),
            ...(form.contact_id && !contactRows.some((c) => c.id === form.contact_id)
              ? [{ value: form.contact_id, label: form.contact_id }]
              : []),
          ]}
        />
        <Textarea
          label={t('supplier.fields.notes')}
          value={form.notes}
          rows={3}
          onChange={(e) => set('notes')(e.target.value)}
          error={fieldError('notes')}
        />
        {stale && editing ? (
          <div
            role="alert"
            className="flex flex-wrap items-center gap-3 rounded-lg border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-fg dark:border-amber-400/30 dark:bg-amber-500/10"
          >
            <span className="min-w-0 flex-1">{t('supplier.errors.stale')}</span>
            <Button
              type="button"
              size="sm"
              variant="secondary"
              loading={reloading}
              onClick={() => {
                setReloading(true);
                void onReload(editing.id).catch((error: unknown) => {
                  setReloading(false);
                  setStale(false);
                  fail(error);
                });
              }}
            >
              {t('supplier.errors.reload')}
            </Button>
          </div>
        ) : null}
        {notice ? (
          <p role="alert" className="text-sm text-danger">
            {notice}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" loading={saving}>
            {editing ? t('common.save') : t('common.create')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

/** DeleteSupplierDialog confirms the deletion; a supplier still referenced is 422. */
function DeleteSupplierDialog({
  supplier,
  onClose,
}: {
  supplier: Supplier | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const remove = useDeleteSupplier();
  const close = () => {
    remove.reset();
    onClose();
  };
  return (
    <Modal
      open={!!supplier}
      onOpenChange={(open) => (open ? undefined : close())}
      title={t('supplier.deleteTitle')}
      description={supplier ? t('supplier.deleteConfirm', { name: supplier.name }) : undefined}
    >
      {remove.error ? (
        <p role="alert" className="mb-3 text-sm text-danger">
          {refusal(remove.error, t, 'delete')}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={close}>
          {t('common.cancel')}
        </Button>
        <Button
          type="button"
          variant="danger"
          loading={remove.isPending}
          onClick={() => supplier && remove.mutate(supplier.id, { onSuccess: close })}
        >
          {t('common.delete')}
        </Button>
      </div>
    </Modal>
  );
}
