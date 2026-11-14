/**
 * ProductMappingPanel is the view "Produktzuordnung" of the findings page
 * (SEC-02, WP-1122, E-236): the groups of CIs without product mapping and
 * the mappings of the organization. The dialog takes manufacturer, model or
 * sys_object_id from a group, suggests vendor and product from the
 * vulnerability catalog and sets version field and scheme; after saving it
 * links the version match job. Without finding:write the view is read only
 * (the server also refuses writes of principals that are not org-wide).
 */
import { useState } from 'react';
import { Link } from 'react-router';
import { useTranslation } from 'react-i18next';
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ApiError, CPE_VERSION_SCHEMES, productMappingApi } from '../../api/client';
import type {
  CPEMapping,
  CPEMappingRequest,
  PaginatedResponse,
  UnmappedCIGroup,
  VulnProduct,
} from '../../api/client';
import { useHasPermission } from '../../api/hooks';
import { useOrgWideWriter } from '../../api/ipamHooks';
import { Button } from '../ui/Button';
import { Card } from '../ui/Card';
import { EmptyState } from '../ui/EmptyState';
import { ErrorState } from '../ui/ErrorState';
import { Input } from '../ui/Input';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';
import { SkeletonList } from '../ui/Skeleton';
import { ScrollRegion } from '../ScrollRegion';
import {
  tableClasses,
  tdClasses,
  tdMonoClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../ui/table';

type MatchKind = NonNullable<CPEMappingRequest['match_kind']>;
type VersionField = NonNullable<CPEMappingRequest['version_field']>;

interface Draft {
  match_kind: MatchKind;
  manufacturer_pattern: string;
  model_pattern: string;
  sys_object_id_prefix: string;
  vendor: string;
  products: string;
  version_field: VersionField;
  version_scheme: (typeof CPE_VERSION_SCHEMES)[number];
}

/** draftFrom prefills the dialog from a group without mapping. */
export function draftFrom(group?: UnmappedCIGroup): Draft {
  const bySysObjectID = !!group && !group.manufacturer && !!group.sys_object_id;
  return {
    match_kind: bySysObjectID ? 'sys_object_id' : 'manufacturer_model',
    manufacturer_pattern: group?.manufacturer ?? '',
    model_pattern: group?.model ?? '',
    sys_object_id_prefix: bySysObjectID ? (group?.sys_object_id ?? '') : '',
    vendor: '',
    products: '',
    version_field: 'firmware_version',
    version_scheme: 'generic',
  };
}

function requestOf(d: Draft): CPEMappingRequest {
  const products = d.products
    .split(',')
    .map((p) => p.trim())
    .filter(Boolean);
  const base: CPEMappingRequest = {
    match_kind: d.match_kind,
    vendor: d.vendor.trim(),
    products,
    version_field: d.version_field,
    version_scheme: d.version_scheme,
  };
  if (d.match_kind === 'sys_object_id') {
    return { ...base, sys_object_id_prefix: d.sys_object_id_prefix.trim() };
  }
  return {
    ...base,
    manufacturer_pattern: d.manufacturer_pattern.trim(),
    model_pattern: d.model_pattern.trim(),
  };
}

function patternOf(m: CPEMapping): string {
  if (m.match_kind === 'sys_object_id') return m.sys_object_id_prefix ?? '';
  return m.model_pattern
    ? `${m.manufacturer_pattern ?? ''} / ${m.model_pattern}`
    : (m.manufacturer_pattern ?? '');
}

/** PAGE_LIMIT is the page size of both lists; further pages load on demand. */
const PAGE_LIMIT = 200;

/** nextOffset is the offset of the page after last, undefined at the end. */
function nextOffset<T>(last: PaginatedResponse<T>): number | undefined {
  return last.has_more ? last.offset + (last.data?.length ?? 0) : undefined;
}

export function ProductMappingPanel() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  // Mappings apply to every CI of the organization: only a principal with
  // finding:write and without client or site restriction writes them
  // (WP-1122 AK1; the API answers 403 otherwise).
  const mayWrite = useHasPermission('finding:write');
  const orgWide = useOrgWideWriter();
  const canWrite = mayWrite === true && orgWide === true;
  const [dialog, setDialog] = useState<Draft | null>(null);
  const [jobId, setJobId] = useState<string | null>(null);

  const unmapped = useInfiniteQuery({
    queryKey: ['security-unmapped'],
    queryFn: ({ pageParam }: { pageParam: number }) =>
      productMappingApi.unmapped({ limit: PAGE_LIMIT, offset: pageParam }),
    initialPageParam: 0,
    getNextPageParam: nextOffset<UnmappedCIGroup>,
  });
  const mappings = useInfiniteQuery({
    queryKey: ['security-cpe-mappings'],
    queryFn: ({ pageParam }: { pageParam: number }) =>
      productMappingApi.list({ limit: PAGE_LIMIT, offset: pageParam }),
    initialPageParam: 0,
    getNextPageParam: nextOffset<CPEMapping>,
  });
  const remove = useMutation({
    mutationFn: (id: string) => productMappingApi.remove(id),
    onSuccess: (deleted) => {
      setJobId(deleted?.match_job_id ?? null);
      void queryClient.invalidateQueries({ queryKey: ['security-cpe-mappings'] });
      void queryClient.invalidateQueries({ queryKey: ['security-unmapped'] });
    },
  });

  const groups = unmapped.data?.pages.flatMap((p) => p.data ?? []) ?? [];
  const rows = mappings.data?.pages.flatMap((p) => p.data ?? []) ?? [];
  const groupsTotal = unmapped.data?.pages[0]?.total ?? 0;
  const rowsTotal = mappings.data?.pages[0]?.total ?? 0;

  return (
    <div className="space-y-6">
      {jobId ? (
        <p role="status" className="rounded-lg border border-line bg-panel px-4 py-3 text-sm">
          {t('findings.mapping.saved')}{' '}
          <Link className="font-medium text-accent underline" to="/jobs">
            {t('findings.mapping.matchJob', { id: jobId })}
          </Link>
        </p>
      ) : null}

      <section aria-labelledby="mapping-unmapped-heading" className="space-y-3">
        <h2 id="mapping-unmapped-heading" className="text-base font-semibold text-fg">
          {t('findings.mapping.unmappedTitle')}
        </h2>
        {unmapped.isLoading ? <SkeletonList rows={3} label={t('app.loading')} /> : null}
        {unmapped.error ? (
          <ErrorState
            title={t('app.error')}
            retryLabel={t('common.retry')}
            onRetry={() => void unmapped.refetch()}
          />
        ) : null}
        {unmapped.data && groups.length === 0 ? (
          <EmptyState title={t('findings.mapping.unmappedEmpty')} />
        ) : null}
        {groups.length > 0 ? (
          <Card className="overflow-hidden p-0">
            <ScrollRegion label={t('findings.mapping.unmappedTitle')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr>
                    <th scope="col" className={thClasses}>
                      {t('findings.mapping.manufacturer')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('findings.mapping.model')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('findings.mapping.sysObjectId')}
                    </th>
                    <th scope="col" className={`${thClasses} text-right`}>
                      {t('findings.mapping.ciCount')}
                    </th>
                    {canWrite ? (
                      <th scope="col" className={`${thClasses} text-right`}>
                        {t('common.actions')}
                      </th>
                    ) : null}
                  </tr>
                </thead>
                <tbody>
                  {groups.map((g) => (
                    <tr
                      key={`${g.manufacturer}|${g.model}|${g.sys_object_id}`}
                      className={trClasses}
                    >
                      <td className={tdPrimaryClasses}>{g.manufacturer || '—'}</td>
                      <td className={tdClasses}>{g.model || '—'}</td>
                      <td className={tdMonoClasses}>{g.sys_object_id || '—'}</td>
                      <td className={`${tdClasses} text-right tabular-nums`}>{g.ci_count}</td>
                      {canWrite ? (
                        <td className={`${tdClasses} text-right`}>
                          <Button
                            size="sm"
                            variant="secondary"
                            aria-label={t('findings.mapping.mapGroup', {
                              name: g.manufacturer || g.sys_object_id,
                            })}
                            onClick={() => setDialog(draftFrom(g))}
                          >
                            {t('findings.mapping.map')}
                          </Button>
                        </td>
                      ) : null}
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollRegion>
          </Card>
        ) : null}
        {unmapped.hasNextPage ? (
          <div className="flex items-center justify-between gap-3 text-sm text-muted">
            <span>{t('findings.mapping.shown', { shown: groups.length, total: groupsTotal })}</span>
            <Button
              size="sm"
              variant="secondary"
              disabled={unmapped.isFetchingNextPage}
              onClick={() => void unmapped.fetchNextPage()}
            >
              {t('findings.mapping.loadMore')}
            </Button>
          </div>
        ) : null}
      </section>

      <section aria-labelledby="mapping-list-heading" className="space-y-3">
        <div className="flex items-center justify-between gap-3">
          <h2 id="mapping-list-heading" className="text-base font-semibold text-fg">
            {t('findings.mapping.listTitle')}
          </h2>
          {canWrite ? (
            <Button size="sm" onClick={() => setDialog(draftFrom())}>
              {t('findings.mapping.create')}
            </Button>
          ) : null}
        </div>
        {mappings.isLoading ? <SkeletonList rows={3} label={t('app.loading')} /> : null}
        {mappings.error ? (
          <ErrorState
            title={t('app.error')}
            retryLabel={t('common.retry')}
            onRetry={() => void mappings.refetch()}
          />
        ) : null}
        {mappings.data && rows.length === 0 ? (
          <EmptyState title={t('findings.mapping.listEmpty')} />
        ) : null}
        {rows.length > 0 ? (
          <Card className="overflow-hidden p-0">
            <ScrollRegion label={t('findings.mapping.listTitle')}>
              <table className={tableClasses}>
                <thead className={theadClasses}>
                  <tr>
                    <th scope="col" className={thClasses}>
                      {t('findings.mapping.pattern')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('findings.mapping.vendor')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('findings.mapping.products')}
                    </th>
                    <th scope="col" className={thClasses}>
                      {t('findings.mapping.versionScheme')}
                    </th>
                    <th scope="col" className={`${thClasses} text-right`}>
                      {t('findings.mapping.priority')}
                    </th>
                    {canWrite ? (
                      <th scope="col" className={`${thClasses} text-right`}>
                        {t('common.actions')}
                      </th>
                    ) : null}
                  </tr>
                </thead>
                <tbody>
                  {rows.map((m) => (
                    <tr key={m.id} className={trClasses}>
                      <td className={tdMonoClasses}>{patternOf(m)}</td>
                      <td className={tdPrimaryClasses}>{m.vendor}</td>
                      <td className={tdMonoClasses}>{m.products.join(', ')}</td>
                      <td className={tdClasses}>
                        {t(`findings.mapping.schemes.${m.version_scheme}`)} ·{' '}
                        {t(`findings.mapping.fields.${m.version_field}`)}
                      </td>
                      <td className={`${tdClasses} text-right tabular-nums`}>{m.priority}</td>
                      {canWrite ? (
                        <td className={`${tdClasses} text-right`}>
                          <Button
                            size="sm"
                            variant="secondary"
                            aria-label={t('findings.mapping.deleteMapping', { name: patternOf(m) })}
                            disabled={remove.isPending}
                            onClick={() => remove.mutate(m.id)}
                          >
                            {t('common.delete')}
                          </Button>
                        </td>
                      ) : null}
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollRegion>
          </Card>
        ) : null}
        {mappings.hasNextPage ? (
          <div className="flex items-center justify-between gap-3 text-sm text-muted">
            <span>{t('findings.mapping.shown', { shown: rows.length, total: rowsTotal })}</span>
            <Button
              size="sm"
              variant="secondary"
              disabled={mappings.isFetchingNextPage}
              onClick={() => void mappings.fetchNextPage()}
            >
              {t('findings.mapping.loadMore')}
            </Button>
          </div>
        ) : null}
      </section>

      {dialog ? (
        <MappingDialog
          initial={dialog}
          onClose={() => setDialog(null)}
          onSaved={(saved) => {
            setDialog(null);
            setJobId(saved.match_job_id ?? null);
            void queryClient.invalidateQueries({ queryKey: ['security-cpe-mappings'] });
            void queryClient.invalidateQueries({ queryKey: ['security-unmapped'] });
          }}
        />
      ) : null}
    </div>
  );
}

function MappingDialog({
  initial,
  onClose,
  onSaved,
}: {
  initial: Draft;
  onClose: () => void;
  onSaved: (saved: CPEMapping) => void;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState<Draft>(initial);
  const [query, setQuery] = useState(initial.manufacturer_pattern || '');
  const set = <K extends keyof Draft>(key: K, value: Draft[K]) =>
    setDraft((d) => ({ ...d, [key]: value }));

  const suggestions = useQuery({
    queryKey: ['security-vuln-products', query],
    queryFn: () => productMappingApi.products(query.trim()),
    enabled: query.trim().length >= 2,
  });
  const save = useMutation({
    mutationFn: () => productMappingApi.create(requestOf(draft)),
    onSuccess: onSaved,
  });
  const violations = save.error instanceof ApiError ? save.error.violations : [];
  const errorOf = (field: string) => violations.find((v) => v.field === field)?.detail;
  const generalError =
    save.error && violations.length === 0 ? t('findings.mapping.saveFailed') : undefined;

  const pick = (p: VulnProduct) => {
    setDraft((d) => {
      const products =
        d.vendor === p.vendor && d.products ? `${d.products}, ${p.product}` : p.product;
      return { ...d, vendor: p.vendor, products };
    });
  };

  return (
    <Modal
      open
      onOpenChange={(open) => (open ? undefined : onClose())}
      title={t('findings.mapping.dialogTitle')}
      description={t('findings.mapping.dialogHint')}
      size="lg"
    >
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        <Select
          label={t('findings.mapping.matchKind')}
          value={draft.match_kind}
          onChange={(e) => set('match_kind', e.target.value as MatchKind)}
          options={[
            { value: 'manufacturer_model', label: t('findings.mapping.kinds.manufacturer_model') },
            { value: 'sys_object_id', label: t('findings.mapping.kinds.sys_object_id') },
          ]}
        />
        {draft.match_kind === 'manufacturer_model' ? (
          <div className="grid gap-4 sm:grid-cols-2">
            <Input
              label={t('findings.mapping.manufacturerPattern')}
              value={draft.manufacturer_pattern}
              error={errorOf('manufacturer_pattern')}
              onChange={(e) => set('manufacturer_pattern', e.target.value)}
            />
            <Input
              label={t('findings.mapping.modelPattern')}
              value={draft.model_pattern}
              error={errorOf('model_pattern')}
              onChange={(e) => set('model_pattern', e.target.value)}
            />
          </div>
        ) : (
          <Input
            label={t('findings.mapping.sysObjectIdPrefix')}
            value={draft.sys_object_id_prefix}
            error={errorOf('sys_object_id_prefix')}
            onChange={(e) => set('sys_object_id_prefix', e.target.value)}
          />
        )}
        <div className="space-y-2">
          <Input
            label={t('findings.mapping.searchCatalog')}
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          {suggestions.data && suggestions.data.data.length > 0 ? (
            <ul aria-label={t('findings.mapping.suggestions')} className="flex flex-wrap gap-2">
              {suggestions.data.data.map((p) => (
                <li key={`${p.vendor}/${p.product}`}>
                  <Button type="button" size="sm" variant="secondary" onClick={() => pick(p)}>
                    {p.vendor} / {p.product}
                  </Button>
                </li>
              ))}
            </ul>
          ) : null}
          {suggestions.data && suggestions.data.data.length === 0 ? (
            <p className="text-sm text-fg-muted">{t('findings.mapping.noSuggestion')}</p>
          ) : null}
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Input
            label={t('findings.mapping.vendor')}
            value={draft.vendor}
            error={errorOf('vendor')}
            onChange={(e) => set('vendor', e.target.value)}
          />
          <Input
            label={t('findings.mapping.productsInput')}
            value={draft.products}
            error={errorOf('products')}
            onChange={(e) => set('products', e.target.value)}
          />
          <Select
            label={t('findings.mapping.versionField')}
            value={draft.version_field}
            onChange={(e) => set('version_field', e.target.value as VersionField)}
            options={[
              { value: 'firmware_version', label: t('findings.mapping.fields.firmware_version') },
              { value: 'os_version', label: t('findings.mapping.fields.os_version') },
            ]}
          />
          <Select
            label={t('findings.mapping.versionScheme')}
            value={draft.version_scheme}
            error={errorOf('version_scheme')}
            onChange={(e) => set('version_scheme', e.target.value as Draft['version_scheme'])}
            options={CPE_VERSION_SCHEMES.map((s) => ({
              value: s,
              label: t(`findings.mapping.schemes.${s}`),
            }))}
          />
        </div>
        {generalError ? (
          <p role="alert" className="text-sm text-red-600">
            {generalError}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" disabled={save.isPending}>
            {t('common.save')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
