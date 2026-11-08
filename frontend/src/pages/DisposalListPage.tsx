import { Download, Plus, Recycle, Trash2 } from 'lucide-react';
import { useState } from 'react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { ApiError } from '../api/client';
import type { CreateDisposalRecordRequest, DisposalMethod, DisposalRecord } from '../api/client';
import {
  useCreateDisposalRecord,
  useDisposalRecords,
  useDocument,
  useHasPermission,
  useSuppliers,
} from '../api/hooks';
import {
  ScanStatusBadge,
  scanNotice,
  useDocumentDownload,
} from '../components/documents/LinkedDocuments';
import { ObjectPicker, type PickedObject } from '../components/pickers/ObjectPicker';
import { FileUpload } from '../components/upload/FileUpload';
import { Badge } from '../components/ui/Badge';
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { Input, labelClasses } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';
import { PageHeader } from '../components/ui/PageHeader';
import { Textarea } from '../components/ui/Textarea';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../components/ui/table';
import { cn } from '../components/ui/utils';
import { ScrollRegion } from '../components/ScrollRegion';
import { TableEmptyRow } from './ListParts';

/** DISPOSAL_METHODS are the disposal methods (migration 000049, disposal.Methods). */
export const DISPOSAL_METHODS = [
  'reuse',
  'recycling',
  'destruction',
  'secure_erasure',
  'physical_destruction',
  'return_to_vendor',
] as const satisfies readonly DisposalMethod[];

/**
 * DISPOSAL_COMMON_REQUIRED_FIELDS are required for every method (E-55,
 * disposal.CommonRequiredFields); "a|b" is satisfied by either field.
 */
export const DISPOSAL_COMMON_REQUIRED_FIELDS = [
  'method',
  'performed_at',
  'performed_by',
  'asset_id|ci_id',
] as const;

/**
 * DISPOSAL_REQUIRED_FIELDS mirrors the table disposal.RequiredFields of the
 * handler (WP-751, x-required-fields-by-method of
 * CreateDisposalRecordRequest): the fields a method requires in addition to
 * DISPOSAL_COMMON_REQUIRED_FIELDS after DIN 66399 and the BSI
 * recommendation. "a|b" is satisfied by either field.
 */
export const DISPOSAL_REQUIRED_FIELDS: Record<DisposalMethod, readonly string[]> = {
  destruction: [
    'protection_class',
    'security_level',
    'data_carrier',
    'supplier_id|witness',
    'evidence_document_id',
  ],
  physical_destruction: [
    'protection_class',
    'security_level',
    'data_carrier',
    'supplier_id|witness',
    'evidence_document_id',
  ],
  secure_erasure: ['data_carrier', 'certificate_ref|notes', 'evidence_document_id'],
  reuse: [],
  recycling: [],
  return_to_vendor: [],
};

/** The protection classes after DIN 66399. */
export const PROTECTION_CLASSES = [1, 2, 3] as const;

/** requiredEntries are the required-field entries of a method incl. the common ones. */
export function requiredEntries(method: DisposalMethod): string[] {
  return [...DISPOSAL_COMMON_REQUIRED_FIELDS, ...(DISPOSAL_REQUIRED_FIELDS[method] ?? [])];
}

/** isRequired tells whether a field is required, alone or as one of alternatives. */
export function isRequired(method: DisposalMethod, field: string): boolean {
  return requiredEntries(method).some((entry) => entry.split('|').includes(field));
}

interface DisposalDraft {
  method: DisposalMethod;
  object: PickedObject[];
  protection_class: string;
  security_level: string;
  data_carrier: string;
  supplier_id: string;
  performed_by: string;
  witness: string;
  performed_at: string;
  certificate_ref: string;
  notes: string;
  evidence_document_id: string;
}

/** localDateTime is the value of a datetime-local input for a moment. */
function localDateTime(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function emptyDraft(): DisposalDraft {
  return {
    method: 'destruction',
    object: [],
    protection_class: '',
    security_level: '',
    data_carrier: '',
    supplier_id: '',
    performed_by: '',
    witness: '',
    performed_at: localDateTime(new Date()),
    certificate_ref: '',
    notes: '',
    evidence_document_id: '',
  };
}

function draftValues(draft: DisposalDraft): Record<string, string> {
  const object = draft.object[0];
  return {
    method: draft.method,
    performed_at: draft.performed_at,
    performed_by: draft.performed_by.trim(),
    witness: draft.witness.trim(),
    asset_id: object?.kind === 'asset' ? object.id : '',
    ci_id: object?.kind === 'ci' ? object.id : '',
    protection_class: draft.protection_class,
    security_level: draft.security_level.trim(),
    data_carrier: draft.data_carrier.trim(),
    supplier_id: draft.supplier_id,
    evidence_document_id: draft.evidence_document_id,
    certificate_ref: draft.certificate_ref.trim(),
    notes: draft.notes.trim(),
  };
}

/**
 * missingFields checks the draft against the required-field table of its
 * method and names, per unmet entry, its first field as the server does.
 */
export function missingFields(draft: DisposalDraft): string[] {
  const values = draftValues(draft);
  return requiredEntries(draft.method).flatMap((entry) => {
    const alternatives = entry.split('|');
    return alternatives.some((field) => values[field]) ? [] : [alternatives[0] ?? entry];
  });
}

function toRequest(draft: DisposalDraft): CreateDisposalRecordRequest {
  const values = draftValues(draft);
  const optional = (field: string) => (values[field] ? { [field]: values[field] } : {});
  return {
    method: draft.method,
    performed_at: new Date(draft.performed_at).toISOString(),
    performed_by: values.performed_by ?? '',
    ...optional('asset_id'),
    ...optional('ci_id'),
    ...(draft.protection_class ? { protection_class: Number(draft.protection_class) } : {}),
    ...optional('security_level'),
    ...optional('data_carrier'),
    ...optional('supplier_id'),
    ...optional('witness'),
    ...optional('evidence_document_id'),
    ...optional('certificate_ref'),
    ...optional('notes'),
  };
}

interface CreateErrors {
  fields: Record<string, string>;
  message?: string;
}

const FORM_FIELDS = [
  'method',
  'asset_id',
  'ci_id',
  'protection_class',
  'security_level',
  'data_carrier',
  'supplier_id',
  'performed_by',
  'witness',
  'performed_at',
  'certificate_ref',
  'notes',
  'evidence_document_id',
];

/**
 * createErrors places a refused disposal: a 400 at the field it names
 * (violations, else the field of the problem), else as detail text; a 422
 * (lifecycle source state, the detail names the allowed states) and a 404
 * as message.
 */
function createErrors(error: unknown, t: TFunction): CreateErrors {
  if (!(error instanceof ApiError)) {
    return { fields: {}, message: error instanceof Error ? error.message : String(error) };
  }
  const detail = error.detail ?? error.title ?? error.message;
  if (error.status === 400) {
    const fields: Record<string, string> = { ...error.violationsByField() };
    if (error.field && !(error.field in fields)) fields[error.field] = detail;
    const placed = Object.keys(fields).some((field) => FORM_FIELDS.includes(field));
    return { fields, message: placed ? undefined : detail };
  }
  if (error.status === 422)
    return { fields: {}, message: t('disposal.errors.sourceState', { detail }) };
  if (error.status === 404)
    return { fields: {}, message: t('disposal.errors.notFound', { detail }) };
  return { fields: {}, message: detail };
}

/** EvidenceStatus shows the scan status of the uploaded evidence and reports infected. */
function EvidenceStatus({ documentId, onRemove }: { documentId: string; onRemove: () => void }) {
  const { t } = useTranslation();
  const document = useDocument(documentId);
  const status = document.data?.scan_status;
  const notice = scanNotice(status, t);
  return (
    <div className="space-y-1" data-testid="evidence-status">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="truncate text-fg">{document.data?.file_name ?? documentId}</span>
        <ScanStatusBadge status={status} />
        <IconButton
          icon={<Trash2 />}
          label={t('disposal.evidenceRemove')}
          variant="ghost"
          size="sm"
          onClick={onRemove}
        />
      </div>
      {notice ? (
        <p className={cn('text-xs', status === 'infected' ? 'text-danger' : 'text-fg-muted')}>
          {status === 'infected' ? t('disposal.evidenceInfected') : notice}
        </p>
      ) : null}
    </div>
  );
}

function CreateDisposalForm({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState<DisposalDraft>(emptyDraft);
  const [errors, setErrors] = useState<CreateErrors>({ fields: {} });
  const create = useCreateDisposalRecord();
  const suppliers = useSuppliers({ limit: 200 });
  const evidence = useDocument(draft.evidence_document_id);
  const infected = evidence.data?.scan_status === 'infected';
  const object = draft.object[0];

  const label = (field: string) =>
    `${t(`disposal.fields.${field}`)}${isRequired(draft.method, field) ? ' *' : ''}`;
  const set = (patch: Partial<DisposalDraft>) => setDraft({ ...draft, ...patch });
  const fieldError = (field: string) => errors.fields[field];

  const submit = () => {
    const missing = missingFields(draft);
    if (missing.length > 0) {
      setErrors({
        fields: Object.fromEntries(missing.map((field) => [field, t('disposal.errors.required')])),
      });
      return;
    }
    setErrors({ fields: {} });
    create.mutate(toRequest(draft), {
      onSuccess: onDone,
      onError: (error) => setErrors(createErrors(error, t)),
    });
  };

  const supplierOptions = [
    { value: '', label: t('disposal.noSupplier') },
    ...(suppliers.data?.data ?? []).map((supplier) => ({
      value: supplier.id,
      label: supplier.name,
    })),
  ];

  return (
    <form
      className="space-y-4"
      noValidate
      onSubmit={(event) => {
        event.preventDefault();
        submit();
      }}
    >
      <p className="text-xs text-fg-muted">{t('disposal.requiredHint')}</p>
      <Select
        label={label('method')}
        value={draft.method}
        error={fieldError('method')}
        onChange={(event) => set({ method: event.target.value as DisposalMethod })}
        options={DISPOSAL_METHODS.map((value) => ({
          value,
          label: t(`disposal.methods.${value}`),
        }))}
      />
      <div className="space-y-1">
        <ObjectPicker
          label={`${t('disposal.fields.object')} *`}
          kinds={['asset', 'ci']}
          value={draft.object}
          required
          onChange={(value) => set({ object: value })}
        />
        {fieldError('asset_id') || fieldError('ci_id') ? (
          <p className="text-sm text-danger">{fieldError('asset_id') ?? fieldError('ci_id')}</p>
        ) : null}
      </div>
      <div className="grid gap-4 md:grid-cols-2">
        <Select
          label={label('protection_class')}
          value={draft.protection_class}
          error={fieldError('protection_class')}
          onChange={(event) => set({ protection_class: event.target.value })}
          options={[
            { value: '', label: t('disposal.noValue') },
            ...PROTECTION_CLASSES.map((value) => ({
              value: String(value),
              label: t('disposal.protectionClass', { value }),
            })),
          ]}
        />
        <Input
          label={label('security_level')}
          value={draft.security_level}
          placeholder={t('disposal.securityLevelPlaceholder')}
          error={fieldError('security_level')}
          onChange={(event) => set({ security_level: event.target.value.toUpperCase() })}
        />
        <Input
          label={label('data_carrier')}
          value={draft.data_carrier}
          error={fieldError('data_carrier')}
          onChange={(event) => set({ data_carrier: event.target.value })}
        />
        <Select
          label={label('supplier_id')}
          value={draft.supplier_id}
          error={fieldError('supplier_id')}
          disabled={suppliers.isError}
          onChange={(event) => set({ supplier_id: event.target.value })}
          options={supplierOptions}
        />
        <Input
          label={label('performed_by')}
          value={draft.performed_by}
          error={fieldError('performed_by')}
          onChange={(event) => set({ performed_by: event.target.value })}
        />
        <Input
          label={label('witness')}
          value={draft.witness}
          error={fieldError('witness')}
          onChange={(event) => set({ witness: event.target.value })}
        />
        <Input
          type="datetime-local"
          label={label('performed_at')}
          value={draft.performed_at}
          error={fieldError('performed_at')}
          onChange={(event) => set({ performed_at: event.target.value })}
        />
        <Input
          label={label('certificate_ref')}
          value={draft.certificate_ref}
          error={fieldError('certificate_ref')}
          onChange={(event) => set({ certificate_ref: event.target.value })}
        />
      </div>
      {suppliers.isError ? (
        <p className="text-xs text-fg-muted">{t('disposal.suppliersUnavailable')}</p>
      ) : null}
      {isRequired(draft.method, 'supplier_id') ? (
        <p className="text-xs text-fg-muted">{t('disposal.supplierOrWitness')}</p>
      ) : null}
      <Textarea
        label={label('notes')}
        value={draft.notes}
        rows={3}
        error={fieldError('notes')}
        onChange={(event) => set({ notes: event.target.value })}
      />

      <div className="space-y-2">
        <span className={labelClasses}>{label('evidence_document_id')}</span>
        {draft.evidence_document_id ? (
          <EvidenceStatus
            documentId={draft.evidence_document_id}
            onRemove={() => set({ evidence_document_id: '' })}
          />
        ) : (
          <FileUpload
            category="certificate"
            label={t('disposal.evidenceUpload')}
            entityType={object?.kind}
            entityId={object?.id}
            onUploaded={(result) => set({ evidence_document_id: result.documentId })}
          />
        )}
        {fieldError('evidence_document_id') ? (
          <p className="text-sm text-danger">{fieldError('evidence_document_id')}</p>
        ) : null}
      </div>

      {errors.message ? (
        <p className="text-sm text-danger" role="alert">
          {errors.message}
        </p>
      ) : null}

      <div className="flex justify-end gap-2">
        <Button variant="secondary" onClick={onCancel}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" loading={create.isPending} disabled={create.isPending || infected}>
          {t('common.create')}
        </Button>
      </div>
    </form>
  );
}

/** DetailRow is one field of the detail view. */
function DetailRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-3 gap-2 py-1.5">
      <dt className="text-sm text-fg-muted">{label}</dt>
      <dd className="col-span-2 break-words text-sm text-fg">{children}</dd>
    </div>
  );
}

/**
 * DisposalDetail shows every field of a record, its record_hash and the
 * evidence with its scan status; the download is offered only for clean
 * content (GET /documents/{id}/content). A record is append-only: there is
 * no edit or delete.
 */
function DisposalDetail({ record }: { record: DisposalRecord }) {
  const { t, i18n } = useTranslation();
  const evidence = useDocument(record.evidence_document_id ?? '');
  const suppliers = useSuppliers({ limit: 200 }, { enabled: !!record.supplier_id });
  const { download, error: downloadError } = useDocumentDownload();
  const status = evidence.data?.scan_status;
  const supplierName = record.supplier_id
    ? (suppliers.data?.data.find((supplier) => supplier.id === record.supplier_id)?.name ??
      record.supplier_id)
    : undefined;
  const dash = (value: string | number | undefined | null) =>
    value === undefined || value === null || value === '' ? '—' : String(value);

  return (
    <div className="space-y-4">
      <dl className="divide-y divide-line">
        <DetailRow label={t('disposal.fields.method')}>
          {t(`disposal.methods.${record.method}`, record.method)}
        </DetailRow>
        <DetailRow label={t('disposal.fields.asset_id')}>
          <span className="font-mono">{dash(record.asset_id)}</span>
        </DetailRow>
        <DetailRow label={t('disposal.fields.ci_id')}>
          <span className="font-mono">{dash(record.ci_id)}</span>
        </DetailRow>
        <DetailRow label={t('disposal.fields.protection_class')}>
          {dash(record.protection_class)}
        </DetailRow>
        <DetailRow label={t('disposal.fields.security_level')}>
          {dash(record.security_level)}
        </DetailRow>
        <DetailRow label={t('disposal.fields.data_carrier')}>{dash(record.data_carrier)}</DetailRow>
        <DetailRow label={t('disposal.fields.supplier_id')}>{dash(supplierName)}</DetailRow>
        <DetailRow label={t('disposal.fields.performed_by')}>{dash(record.performed_by)}</DetailRow>
        <DetailRow label={t('disposal.fields.witness')}>{dash(record.witness)}</DetailRow>
        <DetailRow label={t('disposal.fields.performed_at')}>
          <span className="tabular-nums">
            {new Date(record.performed_at).toLocaleString(i18n.language)}
          </span>
        </DetailRow>
        <DetailRow label={t('disposal.fields.certificate_ref')}>
          {dash(record.certificate_ref)}
        </DetailRow>
        <DetailRow label={t('disposal.fields.notes')}>{dash(record.notes)}</DetailRow>
        <DetailRow label={t('disposal.recordHash')}>
          <span className="font-mono text-xs">{dash(record.record_hash)}</span>
        </DetailRow>
        <DetailRow label={t('disposal.fields.evidence_document_id')}>
          {record.evidence_document_id ? (
            <span className="flex flex-wrap items-center gap-2">
              <span className="truncate">
                {evidence.data?.file_name ?? record.evidence_document_id}
              </span>
              <ScanStatusBadge status={status} />
              {status === 'clean' ? (
                <Button
                  variant="secondary"
                  size="sm"
                  icon={<Download />}
                  onClick={() => void download(record.evidence_document_id!)}
                >
                  {t('disposal.evidenceDownload')}
                </Button>
              ) : null}
            </span>
          ) : (
            '—'
          )}
        </DetailRow>
      </dl>
      {status && status !== 'clean' ? (
        <p className="text-xs text-fg-muted">{scanNotice(status, t)}</p>
      ) : null}
      {downloadError ? (
        <p className="text-sm text-danger" role="alert">
          {downloadError}
        </p>
      ) : null}
    </div>
  );
}

/**
 * DisposalListPage documents disposals after E-55 (WP-751, WP-815): the
 * form follows the required fields of the method, the evidence is uploaded
 * before submitting and goes as evidence_document_id. Records are
 * append-only; the list offers a detail view, no edit and no delete.
 */
export function DisposalListPage() {
  const { t, i18n } = useTranslation();
  const [showCreate, setShowCreate] = useState(false);
  const [selected, setSelected] = useState<DisposalRecord | undefined>(undefined);
  const canWrite = useHasPermission('disposal:write') === true;
  const { data, isLoading, error, refetch } = useDisposalRecords({ limit: 100 });

  const records = data?.data ?? [];
  const methodLabel = (method: string) => t(`disposal.methods.${method}`, method);

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('nav.disposal')}
        description={t('disposal.summary')}
        actions={
          canWrite ? (
            <Button icon={<Plus />} onClick={() => setShowCreate(true)}>
              {t('disposal.create')}
            </Button>
          ) : undefined
        }
      />

      {isLoading ? <SkeletonList rows={5} label={t('app.loading')} /> : null}
      {error ? (
        <ErrorState
          title={t('app.error')}
          description={error instanceof Error ? error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      {data ? (
        <Card className="p-0 overflow-hidden">
          <ScrollRegion label={t('nav.disposal')}>
            <table className={tableClasses}>
              <thead className={theadClasses}>
                <tr>
                  <th scope="col" className={thClasses}>
                    {t('disposal.certificate')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('disposal.method')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('disposal.performedAt')}
                  </th>
                  <th scope="col" className={thClasses}>
                    {t('disposal.fields.data_carrier')}
                  </th>
                  <th scope="col" className={thClasses}>
                    <span className="sr-only">{t('common.actions')}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {records.map((rec: DisposalRecord) => (
                  <tr key={rec.id} className={trClasses}>
                    <td className={cn(tdMonoClasses, 'font-medium')}>
                      {rec.certificate_ref || rec.id.slice(0, 8)}
                    </td>
                    <td className={tdClasses}>
                      <Badge variant="neutral">{methodLabel(rec.method)}</Badge>
                    </td>
                    <td className={tdClasses}>
                      <span className="tabular-nums">
                        {new Date(rec.performed_at).toLocaleString(i18n.language)}
                      </span>
                      {rec.performed_by ? (
                        <span
                          className="block max-w-[16ch] truncate text-xs text-fg-subtle"
                          title={rec.performed_by}
                        >
                          {rec.performed_by}
                        </span>
                      ) : null}
                    </td>
                    <td className={tdClasses}>{rec.data_carrier || '—'}</td>
                    <td className={`${tdClasses} text-right`}>
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label={t('disposal.showDetail', {
                          name: rec.certificate_ref || rec.id.slice(0, 8),
                        })}
                        onClick={() => setSelected(rec)}
                      >
                        {t('disposal.detail')}
                      </Button>
                    </td>
                  </tr>
                ))}
                {records.length === 0 ? (
                  <TableEmptyRow
                    colSpan={5}
                    icon={<Recycle />}
                    title={t('disposal.emptyTitle')}
                    description={t('disposal.emptyHint')}
                  />
                ) : null}
              </tbody>
            </table>
          </ScrollRegion>
        </Card>
      ) : null}

      <Modal open={showCreate} onOpenChange={setShowCreate} title={t('disposal.create')} size="lg">
        {showCreate ? (
          <CreateDisposalForm
            onDone={() => setShowCreate(false)}
            onCancel={() => setShowCreate(false)}
          />
        ) : null}
      </Modal>

      <Modal
        open={selected !== undefined}
        onOpenChange={(next) => {
          if (!next) setSelected(undefined);
        }}
        title={t('disposal.detailTitle')}
        size="lg"
      >
        {selected ? <DisposalDetail record={selected} /> : null}
      </Modal>
    </div>
  );
}
