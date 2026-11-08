/**
 * BulkBar — selection and bulk operations of the CI and the asset list
 * (UI-14, BLK-02, WP-816): the operations of POST /cis/bulk (WP-304, WP-780)
 * and POST /assets/bulk (WP-781), the selection of all matches of the list
 * filters (WP-520) or of a filter of the click builder (WP-315), resolved
 * over POST /search/query up to 500 ids, the partial failure report per id,
 * the job a bulk from 100 ids on runs as with its report, and "Rückgängig"
 * over POST /jobs/{id}/revert (WP-305, WP-784).
 */
import { Filter } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  ApiError,
  assetApi,
  ciApi,
  type BulkRequest,
  type CIBulkItemResult,
  type CIBulkJob,
  type CIBulkResult,
} from '../../api/client';
import { BULK_MAX_IDS, localizedText, savedViewApi, tagLabel } from '../../api/cmdb';
import {
  useLifecycleDefinitions,
  useRelationshipTypes,
  useSearchableAttributeDefinitions,
  useTags,
  useTemplates,
  useTemplateVersions,
} from '../../api/cmdbHooks';
import { isActive, useJob, useJobResult, useRevertJob } from '../../hooks/useJobs';
import { getStatusTranslationKey } from '../../pages/ciStatus';
import { fieldCatalog, type FilterCheck } from '../../rules/builderModes';
import { useToastStore } from '../../stores/toast';
import { LocationPicker, type PickedLocation } from '../pickers/LocationPicker';
import { ObjectPicker, type PickedObject } from '../pickers/ObjectPicker';
import { UserTeamPicker, type PickedHolder } from '../pickers/UserTeamPicker';
import { RuleBuilder } from '../rules/RuleBuilder';
import { Badge } from '../ui/Badge';
import { Button } from '../ui/Button';
import { Card } from '../ui/Card';
import { Input } from '../ui/Input';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';

export type BulkEntity = 'ci' | 'asset';

/** BULK_JOB_FROM_IDS: from this many ids on a bulk runs as a job (API-08). */
export const BULK_JOB_FROM_IDS = 100;

/** The page size of the resolution of a filter over POST /search/query. */
export const FILTER_PAGE_SIZE = 200;

/**
 * BULK_OPERATIONS are the operations of the bulk bar per list (BLK-02):
 * assign is set_owner on CIs and assign on assets; template and
 * relationship operations exist for CIs only.
 */
export const BULK_OPERATIONS = {
  ci: [
    'set_status',
    'set_fields',
    'set_location',
    'add_tags',
    'remove_tags',
    'change_template',
    'add_relationship',
    'remove_relationship',
    'assign',
    'lifecycle_transition',
    'archive',
    'export',
  ],
  asset: [
    'set_status',
    'set_fields',
    'set_location',
    'add_tags',
    'remove_tags',
    'assign',
    'lifecycle_transition',
    'archive',
    'export',
  ],
} as const;

export type BulkOperation = (typeof BULK_OPERATIONS)['ci'][number];

/** The status values of set_status (CIBulkSetStatus, AssetBulkRequest.status). */
export const BULK_STATUSES = {
  ci: ['active', 'inactive', 'maintenance', 'decommissioned'],
  asset: ['in_stock', 'assigned', 'maintenance', 'retired', 'disposed', 'lost'],
} as const;

/** The fields set_fields offers (merge patch of UpdateCIRequest or UpdateAssetRequest). */
export const BULK_FIELDS = {
  ci: [
    'name',
    'manufacturer',
    'model',
    'serial_number',
    'hostname',
    'fqdn',
    'management_ip',
    'os_name',
    'os_version',
    'firmware_version',
  ],
  asset: [
    'name',
    'manufacturer',
    'model',
    'serial_number',
    'cost_center',
    'currency',
    'purchase_date',
    'warranty_until',
  ],
} as const;

export const BULK_DIRECTIONS = ['outgoing', 'incoming'] as const;
export const BULK_EXPORT_FORMATS = ['csv', 'json'] as const;

/** The parameters the bar collects for an operation. */
export interface BulkParams {
  status: string;
  field: string;
  value: string;
  location: PickedLocation | null;
  tags: string[];
  templateId: string;
  toVersion: string;
  relType: string;
  target: PickedObject[];
  direction: (typeof BULK_DIRECTIONS)[number];
  holder: PickedHolder | null;
  toState: string;
  reason: string;
  format: (typeof BULK_EXPORT_FORMATS)[number];
}

export const EMPTY_BULK_PARAMS: BulkParams = {
  status: '',
  field: '',
  value: '',
  location: null,
  tags: [],
  templateId: '',
  toVersion: '',
  relType: '',
  target: [],
  direction: 'outgoing',
  holder: null,
  toState: '',
  reason: '',
  format: 'csv',
};

/** bulkParamsReady reports whether the operation has every parameter it needs. */
export function bulkParamsReady(op: BulkOperation, p: BulkParams): boolean {
  switch (op) {
    case 'set_status':
      return p.status !== '';
    case 'set_fields':
      return p.field !== '' && (p.field !== 'name' || p.value.trim() !== '');
    case 'set_location':
      return p.location !== null;
    case 'add_tags':
    case 'remove_tags':
      return p.tags.length > 0;
    case 'change_template':
      return p.templateId !== '';
    case 'add_relationship':
    case 'remove_relationship':
      return p.relType !== '' && p.target.length > 0;
    case 'assign':
      return p.holder !== null;
    case 'lifecycle_transition':
      return p.toState !== '';
    case 'archive':
    case 'export':
      return true;
  }
}

/**
 * buildBulkRequest renders an operation as the body of POST /cis/bulk
 * (CIBulkRequest) or POST /assets/bulk (AssetBulkRequest, AssetBulkExport).
 */
export function buildBulkRequest(
  entity: BulkEntity,
  op: BulkOperation,
  ids: string[],
  p: BulkParams,
): BulkRequest {
  switch (op) {
    case 'set_status':
      return { action: 'set_status', ids, status: p.status };
    case 'set_fields': {
      const value = p.value.trim();
      return { action: op, ids, params: { fields: { [p.field]: value === '' ? null : value } } };
    }
    case 'set_location':
      return {
        action: op,
        ids,
        params:
          entity === 'ci'
            ? { location_id: p.location?.id ?? null }
            : { to_location_id: p.location?.id },
      };
    case 'add_tags':
    case 'remove_tags':
      return { action: op, ids, params: { tags: p.tags } };
    case 'change_template':
      return {
        action: op,
        ids,
        params: {
          template_id: p.templateId,
          ...(p.toVersion ? { to_version: Number(p.toVersion) } : {}),
        },
      };
    case 'add_relationship':
    case 'remove_relationship':
      return {
        action: op,
        ids,
        params: { rel_type: p.relType, target_ci_id: p.target[0]?.id, direction: p.direction },
      };
    case 'assign': {
      const user = p.holder?.kind === 'user';
      if (entity === 'ci') {
        return {
          action: 'set_owner',
          ids,
          params: user ? { owner_user_id: p.holder?.id } : { owner_team_id: p.holder?.id },
        };
      }
      return {
        action: 'assign',
        ids,
        params: user ? { assigned_user_id: p.holder?.id } : { assigned_team_id: p.holder?.id },
      };
    }
    case 'lifecycle_transition':
      return {
        action: op,
        ids,
        params: { to_state: p.toState, ...(p.reason.trim() ? { reason: p.reason.trim() } : {}) },
      };
    case 'archive':
      return { action: op, ids };
    case 'export':
      return { action: op, ids, params: { format: p.format } };
  }
}

/** sendBulk posts a bulk request to the endpoint of the list. */
export function sendBulk(entity: BulkEntity, request: BulkRequest) {
  return entity === 'ci' ? ciApi.bulk(request) : assetApi.bulk(request);
}

/** A normalized report of a bulk job, its revert job (bulk.revert) or a synchronous bulk. */
export interface BulkReport {
  total: number;
  succeeded: number;
  failed: number;
  skipped: number;
  cancelled: boolean;
  items: CIBulkItemResult[];
}

/**
 * bulkReportOf reads the result of ci.bulk, asset.bulk (BulkResult) or
 * bulk.revert (reverted instead of succeeded); undefined for another shape.
 */
export function bulkReportOf(result: unknown): BulkReport | undefined {
  if (typeof result !== 'object' || result === null) return undefined;
  const r = result as Record<string, unknown>;
  const succeeded = typeof r.succeeded === 'number' ? r.succeeded : r.reverted;
  if (typeof r.total !== 'number' || typeof succeeded !== 'number') return undefined;
  return {
    total: r.total,
    succeeded,
    failed: typeof r.failed === 'number' ? r.failed : 0,
    skipped: typeof r.skipped === 'number' ? r.skipped : 0,
    cancelled: r.cancelled === true,
    items: Array.isArray(r.items) ? (r.items as CIBulkItemResult[]) : [],
  };
}

function nsOf(entity: BulkEntity) {
  return entity === 'ci' ? 'ci.bulk' : 'asset.bulk';
}

function resultVariant(result: string) {
  if (result === 'ok' || result === 'reverted') return 'success' as const;
  if (result === 'error') return 'danger' as const;
  return 'warning' as const;
}

/**
 * BulkFailures lists the ids a bulk failed for with their error (API-08
 * partial failure report), named as the list showed them.
 */
export function BulkFailures({
  entity,
  items,
  names,
}: {
  entity: BulkEntity;
  items: CIBulkItemResult[];
  names: Map<string, string>;
}) {
  const { t } = useTranslation();
  return (
    <ul
      className="space-y-1 text-sm text-red-700 dark:text-red-300"
      aria-label={t(`${nsOf(entity)}.failures`)}
    >
      {items.map((failure) => (
        <li key={failure.id}>
          <span className="font-medium">{names.get(failure.id) ?? failure.id}</span>
          {': '}
          {failure.error}
        </li>
      ))}
    </ul>
  );
}

/** BulkReportTable is the report per id of a bulk or its revert (also in the job center). */
export function BulkReportTable({
  entity,
  report,
  names,
}: {
  entity: BulkEntity;
  report: BulkReport;
  names: Map<string, string>;
}) {
  const { t } = useTranslation();
  const ns = nsOf(entity);
  return (
    <div className="max-h-96 overflow-auto rounded border border-gray-200 dark:border-gray-800">
      <table className="w-full text-left text-xs" aria-label={t(`${ns}.report`)}>
        <thead className="sticky top-0 bg-gray-50 dark:bg-gray-950">
          <tr>
            <th scope="col" className="px-3 py-2">
              {t(`${ns}.reportCI`)}
            </th>
            <th scope="col" className="px-3 py-2">
              {t(`${ns}.reportResult`)}
            </th>
            <th scope="col" className="px-3 py-2">
              {t(`${ns}.reportCode`)}
            </th>
            <th scope="col" className="px-3 py-2">
              {t(`${ns}.reportError`)}
            </th>
          </tr>
        </thead>
        <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
          {report.items.map((item) => (
            <tr key={item.id}>
              <td className="px-3 py-1.5">{names.get(item.id) ?? item.id}</td>
              <td className="px-3 py-1.5">
                <Badge variant={resultVariant(item.result)}>
                  {t(`ci.bulk.results.${item.result}`, item.result)}
                </Badge>
              </td>
              <td className="px-3 py-1.5 font-mono">{item.code || '—'}</td>
              <td className="px-3 py-1.5">{item.error ?? ''}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** The job the bar follows: a bulk from 100 ids on or the revert job of a bulk. */
interface FollowedJob {
  id: string;
  count: number;
  op: BulkOperation;
  revert: boolean;
}

/**
 * BulkJobReport shows a followed job: status and progress while it runs
 * (GET /jobs/{id} while isActive), then its report per id from the job or
 * GET /jobs/{id}/result. The report holds only the ids the list collected
 * for this user, so it names no object the user cannot see.
 */
function BulkJobReport({
  entity,
  followed,
  names,
  onClose,
  onFinished,
}: {
  entity: BulkEntity;
  followed: FollowedJob;
  names: Map<string, string>;
  onClose: () => void;
  onFinished: (succeeded: boolean) => void;
}) {
  const { t } = useTranslation();
  const ns = nsOf(entity);
  const read = useJob(followed.id);
  const job = read.data;
  const finished = job !== undefined && !isActive(job);
  // GET /jobs/{id} carries the result; the result endpoint is the fallback.
  const fallback = useJobResult(
    finished && job.status !== 'failed' && job.result === undefined ? followed.id : null,
  );
  const report = bulkReportOf(finished ? (job.result ?? fallback.data) : undefined);

  const notified = useRef(false);
  useEffect(() => {
    if (finished && !notified.current) {
      notified.current = true;
      onFinished(job.status === 'succeeded');
    }
  }, [finished, job, onFinished]);

  const failures = report?.items.filter((item) => item.result === 'error') ?? [];
  const progress = job?.progress ?? 0;
  const queuedKey = followed.revert
    ? 'bulk.revertQueued'
    : followed.op === 'set_status'
      ? `${ns}.queued`
      : `${ns}.queuedOther`;

  return (
    <Card title={t(followed.revert ? 'bulk.revertTitle' : `${ns}.jobTitle`)}>
      <div className="space-y-3 text-sm">
        {!finished ? (
          <>
            <p role="status" className="text-gray-700 dark:text-gray-300">
              <span>{t(queuedKey, { count: followed.count })}</span>
              {job ? (
                <span className="ml-1">
                  {t(`${ns}.jobState`, {
                    status: t(`jobs.statuses.${job.status}`, job.status),
                    progress,
                  })}
                </span>
              ) : null}
            </p>
            <div
              role="progressbar"
              aria-label={t(`${ns}.jobProgress`)}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={progress}
              className="h-2 w-full max-w-md overflow-hidden rounded bg-gray-200 dark:bg-gray-700"
            >
              <div className="h-full bg-primary" style={{ width: `${progress}%` }} />
            </div>
          </>
        ) : null}
        {read.error && !job ? (
          <p role="alert" className="text-red-600 dark:text-red-400">
            {t(`${ns}.jobLoadError`)}
          </p>
        ) : null}
        {job?.status === 'failed' ? (
          <p role="alert" className="text-red-600 dark:text-red-400">
            {t(`${ns}.jobFailed`, { error: job.error ?? '' })}
          </p>
        ) : null}
        {finished && job.status !== 'failed' && fallback.error ? (
          <p role="alert" className="text-red-600 dark:text-red-400">
            {t(`${ns}.jobLoadError`)}
          </p>
        ) : null}
        {report ? (
          <>
            <p role="status" className="font-medium text-gray-900 dark:text-gray-100">
              {t(
                followed.revert
                  ? 'bulk.revertDone'
                  : report.cancelled
                    ? `${ns}.jobCancelled`
                    : `${ns}.jobDone`,
                {
                  succeeded: report.succeeded,
                  total: report.total,
                  failed: report.failed,
                  skipped: report.skipped,
                },
              )}
            </p>
            {failures.length > 0 ? (
              <BulkFailures entity={entity} items={failures} names={names} />
            ) : null}
            <BulkReportTable entity={entity} report={report} names={names} />
          </>
        ) : null}
        {finished ? (
          <Button variant="ghost" size="sm" onClick={onClose}>
            {t(`${ns}.closeReport`)}
          </Button>
        ) : null}
      </div>
    </Card>
  );
}

/** The ids and names a selection over the list or a filter collected. */
export interface CollectedMatches {
  items: { id: string; name: string }[];
  /** All matches, also those beyond the 500 collected. */
  total: number;
}

/**
 * resolveFilter collects the ids of the matches of a filter AST over POST
 * /search/query page by page (limit 200) up to `max` (MaxBulkIDs).
 */
export async function resolveFilter(
  entity: BulkEntity,
  where: unknown,
  max = BULK_MAX_IDS,
): Promise<CollectedMatches> {
  const seen = new Set<string>();
  const items: CollectedMatches['items'] = [];
  let total = 0;
  for (let offset = 0; items.length < max; offset += FILTER_PAGE_SIZE) {
    const page = await savedViewApi.query(
      { entity_kind: entity, where },
      { limit: FILTER_PAGE_SIZE, offset },
    );
    const rows = page.data ?? [];
    total = typeof page.total === 'number' ? page.total : offset + rows.length;
    for (const row of rows) {
      if (items.length < max && !seen.has(row.id)) {
        seen.add(row.id);
        items.push({ id: row.id, name: row.name });
      }
    }
    if (!page.has_more || rows.length === 0) break;
  }
  return { items, total: Math.max(total, items.length) };
}

/** FilterSelection is the dialog of the selection by a filter of the click builder. */
function FilterSelection({
  entity,
  onResolved,
  onClose,
}: {
  entity: BulkEntity;
  onResolved: (matches: CollectedMatches) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const definitions = useSearchableAttributeDefinitions();
  const relTypes = useRelationshipTypes(entity === 'ci');
  const [check, setCheck] = useState<FilterCheck>({ ok: true, where: undefined });
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState('');
  const fields = useMemo(
    () => ({ [entity]: fieldCatalog(entity, { definitions: definitions.data ?? [] }) }),
    [entity, definitions.data],
  );

  async function resolve() {
    if (!check.ok || !check.where) return;
    setBusy(true);
    setFailure('');
    try {
      onResolved(await resolveFilter(entity, check.where));
    } catch (error) {
      setFailure(error instanceof ApiError && error.detail ? error.detail : t('bulk.filterError'));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-4">
      <p className="text-sm text-fg-muted">{t('bulk.filterHint', { max: BULK_MAX_IDS })}</p>
      <RuleBuilder
        entityKind={entity}
        fields={fields}
        relationshipTypes={(relTypes.data?.data ?? []).map((r) => ({
          key: r.key,
          label: r.display_name || r.key,
        }))}
        onChange={(next: FilterCheck) => {
          setCheck(next);
          setFailure('');
        }}
      />
      {failure ? (
        <p role="alert" className="text-sm text-danger">
          {failure}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>
          {t('common.cancel')}
        </Button>
        <Button loading={busy} disabled={!check.ok || !check.where} onClick={() => void resolve()}>
          {t('bulk.filterApply')}
        </Button>
      </div>
    </div>
  );
}

/** The controls of the parameters of an operation. */
function BulkParamsEditor({
  entity,
  op,
  params,
  onChange,
}: {
  entity: BulkEntity;
  op: BulkOperation;
  params: BulkParams;
  onChange: (next: Partial<BulkParams>) => void;
}) {
  const { t } = useTranslation();
  const ns = nsOf(entity);
  const choose = { value: '', label: t('bulk.choose') };
  switch (op) {
    case 'set_status':
      return (
        <Select
          value={params.status}
          onChange={(event) => onChange({ status: event.target.value })}
          aria-label={t(`${ns}.setStatus`)}
          className="max-w-xs"
          options={[
            { value: '', label: t(`${ns}.setStatus`) },
            ...BULK_STATUSES[entity].map((value) => ({
              value,
              label:
                entity === 'ci'
                  ? t(getStatusTranslationKey(value))
                  : t(`asset.bulk.statuses.${value}`, value),
            })),
          ]}
        />
      );
    case 'set_fields':
      return (
        <>
          <Select
            value={params.field}
            onChange={(event) => onChange({ field: event.target.value })}
            aria-label={t('bulk.field')}
            className="max-w-xs"
            options={[
              choose,
              ...BULK_FIELDS[entity].map((field) => ({
                value: field,
                label: t(entity === 'ci' ? `bulk.ciFields.${field}` : `asset.columns.${field}`),
              })),
            ]}
          />
          <Input
            value={params.value}
            onChange={(event) => onChange({ value: event.target.value })}
            aria-label={t('bulk.value')}
            placeholder={t('bulk.valueHint')}
            className="w-56"
          />
        </>
      );
    case 'set_location':
      return (
        <div className="w-full max-w-md">
          <LocationPicker
            label={t('bulk.location')}
            value={params.location?.id ?? null}
            onChange={(location) => onChange({ location })}
          />
        </div>
      );
    case 'add_tags':
    case 'remove_tags':
      return <TagChoice selected={params.tags} onChange={(tags) => onChange({ tags })} />;
    case 'change_template':
      return <TemplateChoice params={params} onChange={onChange} />;
    case 'add_relationship':
    case 'remove_relationship':
      return <RelationshipChoice params={params} onChange={onChange} />;
    case 'assign':
      return (
        <div className="w-full max-w-md">
          <UserTeamPicker
            label={t(entity === 'ci' ? 'bulk.owner' : 'bulk.holder')}
            value={params.holder}
            onChange={(holder) => onChange({ holder })}
          />
        </div>
      );
    case 'lifecycle_transition':
      return <LifecycleChoice entity={entity} params={params} onChange={onChange} />;
    case 'archive':
      return <span className="text-xs text-fg-muted">{t('bulk.archiveHint')}</span>;
    case 'export':
      return (
        <Select
          value={params.format}
          onChange={(event) => onChange({ format: event.target.value as BulkParams['format'] })}
          aria-label={t('bulk.format')}
          className="max-w-[10rem]"
          options={BULK_EXPORT_FORMATS.map((value) => ({
            value,
            label: t(`bulk.formats.${value}`),
          }))}
        />
      );
  }
}

function TagChoice({
  selected,
  onChange,
}: {
  selected: string[];
  onChange: (tags: string[]) => void;
}) {
  const { t, i18n } = useTranslation();
  const tags = useTags();
  const catalog = tags.data?.data ?? [];
  return (
    <fieldset className="flex flex-wrap items-center gap-2">
      <legend className="sr-only">{t('bulk.tags')}</legend>
      {tags.isLoading ? <span className="text-xs text-fg-muted">{t('app.loading')}</span> : null}
      {tags.data && catalog.length === 0 ? (
        <span className="text-xs text-fg-muted">{t('bulk.noTags')}</span>
      ) : null}
      {catalog.map((tag) => (
        <label key={tag.key} className="flex items-center gap-1 text-sm">
          <input
            type="checkbox"
            checked={selected.includes(tag.key)}
            onChange={() =>
              onChange(
                selected.includes(tag.key)
                  ? selected.filter((key) => key !== tag.key)
                  : [...selected, tag.key],
              )
            }
          />
          {tagLabel(tag, i18n.language)}
        </label>
      ))}
    </fieldset>
  );
}

function TemplateChoice({
  params,
  onChange,
}: {
  params: BulkParams;
  onChange: (next: Partial<BulkParams>) => void;
}) {
  const { t, i18n } = useTranslation();
  const templates = useTemplates();
  const versions = useTemplateVersions(params.templateId || undefined);
  const published = (templates.data?.data ?? []).filter(
    (template) => template.current_version !== null && template.is_active,
  );
  return (
    <>
      <Select
        value={params.templateId}
        onChange={(event) => onChange({ templateId: event.target.value, toVersion: '' })}
        aria-label={t('bulk.template')}
        className="max-w-xs"
        options={[
          { value: '', label: t('bulk.choose') },
          ...published.map((template) => ({
            value: template.id,
            label: localizedText(template.display_name, i18n.language) || template.key,
          })),
        ]}
      />
      <Select
        value={params.toVersion}
        onChange={(event) => onChange({ toVersion: event.target.value })}
        aria-label={t('bulk.templateVersion')}
        disabled={!params.templateId}
        className="max-w-[12rem]"
        options={[
          { value: '', label: t('bulk.currentVersion') },
          ...(versions.data?.data ?? [])
            .filter((version) => version.status === 'published')
            .map((version) => ({
              value: String(version.version),
              label: t('bulk.version', { version: version.version }),
            })),
        ]}
      />
    </>
  );
}

function RelationshipChoice({
  params,
  onChange,
}: {
  params: BulkParams;
  onChange: (next: Partial<BulkParams>) => void;
}) {
  const { t } = useTranslation();
  const types = useRelationshipTypes();
  return (
    <>
      <Select
        value={params.relType}
        onChange={(event) => onChange({ relType: event.target.value })}
        aria-label={t('bulk.relType')}
        className="max-w-xs"
        options={[
          { value: '', label: t('bulk.choose') },
          ...(types.data?.data ?? []).map((type) => ({
            value: type.key,
            label: type.display_name || type.key,
          })),
        ]}
      />
      <Select
        value={params.direction}
        onChange={(event) => onChange({ direction: event.target.value as BulkParams['direction'] })}
        aria-label={t('bulk.direction')}
        className="max-w-[14rem]"
        options={BULK_DIRECTIONS.map((value) => ({
          value,
          label: t(`bulk.directions.${value}`),
        }))}
      />
      <div className="w-full max-w-md">
        <ObjectPicker
          label={t('bulk.target')}
          kinds={['ci']}
          value={params.target}
          onChange={(target) => onChange({ target })}
        />
      </div>
    </>
  );
}

function LifecycleChoice({
  entity,
  params,
  onChange,
}: {
  entity: BulkEntity;
  params: BulkParams;
  onChange: (next: Partial<BulkParams>) => void;
}) {
  const { t } = useTranslation();
  const models = useLifecycleDefinitions();
  const states = useMemo(() => {
    const labels = new Map<string, string>();
    for (const model of models.data?.data ?? []) {
      if (model.applies_to !== entity) continue;
      for (const state of model.states ?? []) {
        if (!labels.has(state.key)) labels.set(state.key, state.label);
      }
    }
    return [...labels.entries()];
  }, [models.data, entity]);
  return (
    <>
      <Select
        value={params.toState}
        onChange={(event) => onChange({ toState: event.target.value })}
        aria-label={t('bulk.toState')}
        className="max-w-xs"
        options={[
          { value: '', label: t('bulk.choose') },
          ...states.map(([key, label]) => ({
            value: key,
            label: t(`lifecycle.states.${key}`, label),
          })),
        ]}
      />
      <Input
        value={params.reason}
        onChange={(event) => onChange({ reason: event.target.value })}
        aria-label={t('bulk.reason')}
        placeholder={t('bulk.reason')}
        className="w-56"
      />
    </>
  );
}

export interface BulkBarProps {
  entity: BulkEntity;
  /** The selected ids; the selection survives paging, the page owns it. */
  selectedIds: string[];
  onSelectionChange: (ids: string[]) => void;
  /** The number of matches of the list filters; undefined while the list loads. */
  total?: number;
  /** collectMatches gathers the matches of the list filters up to `max` (WP-520). */
  collectMatches: (max: number) => Promise<CollectedMatches>;
  /** Changes when the list filters or the scope change; it drops the collected state. */
  filterKey: string;
  /** Names of the objects the list showed or collected, for the reports. */
  names: Map<string, string>;
  /** Called after a change, so the list reads its rows again. */
  onChanged: () => void;
}

type Capped = { source: 'list' | 'filter'; total: number } | null;

/**
 * BulkBar is the toolbar of the selected rows with the operations of BLK-02
 * and the reports and the undo of a bulk.
 */
export function BulkBar({
  entity,
  selectedIds,
  onSelectionChange,
  total,
  collectMatches,
  filterKey,
  names,
  onChanged,
}: BulkBarProps) {
  const { t } = useTranslation();
  const ns = nsOf(entity);
  const pushToast = useToastStore((state) => state.push);
  const revert = useRevertJob();
  const [op, setOp] = useState<BulkOperation>('set_status');
  const [params, setParams] = useState<BulkParams>(EMPTY_BULK_PARAMS);
  const [busy, setBusy] = useState(false);
  const [failures, setFailures] = useState<CIBulkItemResult[]>([]);
  const [followed, setFollowed] = useState<FollowedJob | null>(null);
  const [collecting, setCollecting] = useState(false);
  const [capped, setCapped] = useState<Capped>(null);
  const [filtering, setFiltering] = useState(false);
  const operations: readonly BulkOperation[] = BULK_OPERATIONS[entity];

  // Set on unmount so a running collection does not update a gone page.
  const unmounted = useRef(false);
  useEffect(() => {
    unmounted.current = false;
    return () => {
      unmounted.current = true;
    };
  }, []);
  const currentFilterKey = useRef(filterKey);
  currentFilterKey.current = filterKey;
  useEffect(() => {
    setCapped(null);
  }, [filterKey]);

  function clearSelection() {
    onSelectionChange([]);
    setCapped(null);
  }

  function select(matches: CollectedMatches, source: 'list' | 'filter') {
    for (const item of matches.items) {
      names.set(item.id, item.name);
    }
    onSelectionChange(matches.items.map((item) => item.id));
    setCapped(matches.total > matches.items.length ? { source, total: matches.total } : null);
  }

  /** selectAllMatches selects every row of the filtered list up to 500 (API-08, F-328). */
  async function selectAllMatches() {
    const key = filterKey;
    setCollecting(true);
    try {
      const collected = await collectMatches(BULK_MAX_IDS);
      // A filter changed meanwhile: the matches belong to the old list.
      if (unmounted.current || currentFilterKey.current !== key) return;
      select(collected, 'list');
    } catch {
      pushToast({ message: t(`${ns}.collectError`), variant: 'error' });
    } finally {
      if (!unmounted.current) setCollecting(false);
    }
  }

  /** undo reverts a bulk job (POST /jobs/{id}/revert): 200 done, 202 a revert job to follow. */
  function undo(jobId: string, count: number) {
    revert.mutate(jobId, {
      onSuccess: (outcome) => {
        if (outcome.kind === 'job') {
          setFollowed({ id: outcome.job.id, count, op, revert: true });
          pushToast({ message: t('bulk.undoQueued'), variant: 'info' });
        } else {
          pushToast({ message: t('bulk.undoDone'), variant: 'success' });
        }
        onChanged();
      },
      onError: (error) => {
        const status = error instanceof ApiError ? error.status : undefined;
        const detail = error instanceof ApiError ? error.detail || error.title : '';
        pushToast({
          message:
            status === 409
              ? t('bulk.undoConflict', { detail })
              : status === 403
                ? t('bulk.undoForbidden')
                : t('bulk.undoFailed'),
          variant: 'error',
        });
      },
    });
  }

  function undoAction(jobId: string | undefined, count: number) {
    return jobId
      ? { actionLabel: t('bulk.undo'), onAction: () => undo(jobId, count), durationMs: 15_000 }
      : {};
  }

  async function apply() {
    if (selectedIds.length === 0 || selectedIds.length > BULK_MAX_IDS) return;
    if (!bulkParamsReady(op, params)) return;
    const ids = selectedIds;
    setBusy(true);
    setFailures([]);
    try {
      // One request for all ids; the server reports every id (API-08).
      const result: CIBulkResult | CIBulkJob = await sendBulk(
        entity,
        buildBulkRequest(entity, op, ids, params),
      );
      if ('job_id' in result) {
        if (result.kind === 'export') {
          pushToast({ message: t('bulk.exportQueued', { count: ids.length }), variant: 'success' });
        } else {
          // From 100 ids on: the bar follows the job to its report.
          setFollowed({ id: result.job_id, count: ids.length, op, revert: false });
        }
      } else if (result.failed === 0) {
        pushToast({
          message:
            op === 'set_status'
              ? t(`${ns}.success`, { count: result.succeeded })
              : t(`${ns}.successOther`, { count: result.succeeded }),
          variant: 'success',
          ...undoAction(result.bulk_job_id, ids.length),
        });
      } else {
        setFailures(result.items.filter((item) => item.result === 'error'));
        pushToast({
          message: t(`${ns}.partial`, { succeeded: result.succeeded, failed: result.failed }),
          variant: 'error',
          ...(result.succeeded > 0 ? undoAction(result.bulk_job_id, ids.length) : {}),
        });
      }
      onSelectionChange([]);
      setCapped(null);
      setParams(EMPTY_BULK_PARAMS);
      onChanged();
    } catch (error) {
      pushToast({
        message:
          error instanceof ApiError && error.status === 422 && error.detail
            ? t('bulk.invalid', { detail: error.detail })
            : error instanceof ApiError && error.status === 403
              ? t('bulk.forbidden')
              : t(`${ns}.error`),
        variant: 'error',
      });
    } finally {
      setBusy(false);
    }
  }

  const count = selectedIds.length;
  const job = followed;

  return (
    <>
      {failures.length > 0 ? (
        <Card title={t(`${ns}.failures`)}>
          <BulkFailures entity={entity} items={failures} names={names} />
        </Card>
      ) : null}

      {job ? (
        <BulkJobReport
          key={job.id}
          entity={entity}
          followed={job}
          names={names}
          onClose={() => setFollowed(null)}
          onFinished={(succeeded) => {
            onChanged();
            if (succeeded && !job.revert) {
              pushToast({
                message: t('bulk.jobFinished'),
                variant: 'success',
                ...undoAction(job.id, job.count),
              });
            }
          }}
        />
      ) : null}

      {count === 0 && total !== undefined && total > 0 ? (
        <div className="flex justify-end">
          <Button variant="ghost" size="sm" icon={<Filter />} onClick={() => setFiltering(true)}>
            {t('bulk.selectByFilter')}
          </Button>
        </div>
      ) : null}

      {count > 0 ? (
        <div
          className="flex flex-wrap items-center gap-3 rounded-lg border border-line bg-accent-soft px-4 py-2.5"
          role="toolbar"
          aria-label={t(`${ns}.toolbarLabel`)}
        >
          <span className="text-sm font-medium text-gray-700 dark:text-gray-200">
            {t(`${ns}.selected`, { count })}
          </span>
          <Select
            value={op}
            onChange={(event) => {
              setOp(event.target.value as BulkOperation);
              setParams(EMPTY_BULK_PARAMS);
            }}
            aria-label={t('bulk.operation')}
            className="max-w-xs"
            options={operations.map((value) => ({
              value,
              label: t(`bulk.operations.${value}`),
            }))}
          />
          <BulkParamsEditor
            entity={entity}
            op={op}
            params={params}
            onChange={(next) => setParams((current) => ({ ...current, ...next }))}
          />
          <Button
            size="sm"
            disabled={!bulkParamsReady(op, params) || count > BULK_MAX_IDS}
            loading={busy}
            onClick={() => void apply()}
          >
            {t(`${ns}.apply`)}
          </Button>
          {total !== undefined && total > count && count < BULK_MAX_IDS ? (
            <Button
              variant="secondary"
              size="sm"
              loading={collecting}
              onClick={() => void selectAllMatches()}
            >
              {total > BULK_MAX_IDS
                ? t(`${ns}.selectFirstMatches`, { count: BULK_MAX_IDS })
                : t(`${ns}.selectAllMatches`, { count: total })}
            </Button>
          ) : null}
          <Button
            variant="secondary"
            size="sm"
            icon={<Filter />}
            onClick={() => setFiltering(true)}
          >
            {t('bulk.selectByFilter')}
          </Button>
          <Button variant="ghost" size="sm" onClick={clearSelection}>
            {t(`${ns}.clearSelection`)}
          </Button>
          {capped !== null ? (
            <p role="status" className="w-full text-xs text-gray-700 dark:text-gray-300">
              {capped.source === 'list'
                ? t(`${ns}.capped`, { max: BULK_MAX_IDS, total: capped.total })
                : t('bulk.filterCapped', { max: BULK_MAX_IDS, total: capped.total })}
            </p>
          ) : null}
          {count > BULK_MAX_IDS ? (
            <p role="alert" className="w-full text-xs text-red-600 dark:text-red-400">
              {t(`${ns}.tooMany`, { max: BULK_MAX_IDS, count })}
            </p>
          ) : count >= BULK_JOB_FROM_IDS ? (
            <p className="w-full text-xs text-gray-700 dark:text-gray-300">
              {t(`${ns}.jobHint`, { count: BULK_JOB_FROM_IDS })}
            </p>
          ) : null}
        </div>
      ) : null}

      <Modal open={filtering} onOpenChange={setFiltering} title={t('bulk.filterTitle')} size="lg">
        {filtering ? (
          <FilterSelection
            entity={entity}
            onClose={() => setFiltering(false)}
            onResolved={(matches) => {
              setFiltering(false);
              if (matches.items.length === 0) {
                pushToast({ message: t('bulk.filterEmpty'), variant: 'info' });
                return;
              }
              select(matches, 'filter');
            }}
          />
        ) : null}
      </Modal>
    </>
  );
}
