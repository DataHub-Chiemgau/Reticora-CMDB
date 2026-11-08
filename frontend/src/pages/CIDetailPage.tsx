import { Braces, Network, Pencil, Tags } from 'lucide-react';
import { useMemo, useRef, useState } from 'react';
import type { TFunction } from 'i18next';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { ApiError } from '../api/client';
import type { CI, CIAssetFacts, TopologyGraphData } from '../api/client';
import {
  useCI,
  useCINeighbors,
  useHasPermission,
  useSupplier,
  useTeam,
  useUpdateCI,
  useUser,
} from '../api/hooks';
import { localizedText, tagLabel } from '../api/cmdb';
import { useCIType, useCITypes, useSetCITags, useTags } from '../api/cmdbHooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { PageHeader } from '../components/ui/PageHeader';
import { SkeletonList } from '../components/ui/Skeleton';
import { ScrollRegion } from '../components/ScrollRegion';
import { CIFormModal } from './CIFormModal';
import { getStatusBadgeVariant, getStatusTranslationKey } from './ciStatus';
import {
  InstanceFieldsSection,
  ProvenanceSection,
  LifecycleSection,
  ImpactSection,
  CIMetricsSection,
  CIHistorySection,
  DetailRow,
  formatDate,
  formatDateTime,
  useLocationNames,
} from '../components/cmdb/CIDetailSections';
import { CIRelationshipsCard } from '../components/cmdb/CIRelationshipsCard';
import { ScopeBanner } from '../components/ScopeBanner';
import { CITemplatesSection } from '../components/metamodel/CITemplatesSection';

export interface NeighborEntry {
  id: string;
  name: string;
  ciType: string;
  status: string;
  relType: string;
  direction: 'outgoing' | 'incoming';
}

/**
 * Flattens the depth-1 neighbor subgraph of a CI into a directed list that can
 * be rendered as a table. Edges pointing at CIs that are missing from the node
 * set are skipped so a partial response cannot produce empty rows.
 */
export function buildNeighborList(graph: TopologyGraphData, ciId: string): NeighborEntry[] {
  const nodesById = new Map(graph.nodes.map((node) => [node.id, node]));

  return graph.edges.flatMap((edge) => {
    const isOutgoing = edge.source_ci_id === ciId;
    const otherId = isOutgoing ? edge.target_ci_id : edge.source_ci_id;

    if (edge.source_ci_id !== ciId && edge.target_ci_id !== ciId) {
      return [];
    }

    const node = nodesById.get(otherId);
    if (!node) {
      return [];
    }

    return [
      {
        id: node.id,
        name: node.name,
        // The neighbors of a CI are CIs, which always carry both (K27).
        ciType: node.ci_type ?? '',
        status: node.status ?? '',
        relType: edge.rel_type,
        direction: isOutgoing ? ('outgoing' as const) : ('incoming' as const),
      },
    ];
  });
}

export function CIDetailPage() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const { id = '' } = useParams<{ id: string }>();
  const [isEditOpen, setIsEditOpen] = useState(false);

  const ciQuery = useCI(id);
  // The type is shown by its label in the UI language (GLO-10, UI-18); the
  // list includes deactivated types, which a CI can still carry. A type
  // beyond the first page of the list is read on its own.
  const ciTypes = useCITypes({ include_inactive: true, limit: 200 });
  const typeId = ciQuery.data?.ci_type_id;
  const listedType = ciTypes.data?.data.find((type) => type.id === typeId);
  const singleType = useCIType(typeId, {
    enabled: !!ciTypes.data && !listedType && !!ciTypes.data.has_more,
  });
  const neighborsQuery = useCINeighbors(id);
  // Location, site and room are shown by name from the location tree (CI-06,
  // DB-05, F-237); the tree is read only for a CI that has a location.
  const locations = useLocationNames(
    !!(
      ciQuery.data?.location_id ||
      ciQuery.data?.site_id ||
      ciQuery.data?.room_id ||
      ciQuery.data?.asset?.location_id
    ),
  );
  const updateCI = useUpdateCI();
  // Instance attribute saves (API-07, F-082): every save carries If-Match with
  // the CI version the page shows. The fields save on every valid change, so
  // the saves run one after another and a save waiting behind another uses
  // the version that save returned; only the latest waiting values are sent,
  // always to the CI they were typed on.
  const attributeSaves = useRef<{
    running: boolean;
    pending: { id: string; version?: number; attributes: Record<string, unknown> } | null;
    saved?: { id: string; version: number };
  }>({ running: false, pending: null });

  if (ciQuery.isLoading) {
    return <SkeletonList rows={5} label={t('app.loading')} />;
  }

  if (ciQuery.error || !ciQuery.data) {
    return (
      <ErrorState
        title={t('ci.detailLoadError')}
        description={ciQuery.error instanceof Error ? ciQuery.error.message : undefined}
        retryLabel={t('common.retry')}
        onRetry={() => void ciQuery.refetch()}
      />
    );
  }

  const ci = ciQuery.data;
  const neighbors = neighborsQuery.data ? buildNeighborList(neighborsQuery.data, ci.id) : [];
  // The type id is shown only while the type is unknown (not loaded, not
  // visible to the user or deleted).
  const ciType =
    listedType ?? (singleType.data?.id === ci.ci_type_id ? singleType.data : undefined);
  const typeLabel = ciType
    ? localizedText(ciType.display_name, i18n.language) || ciType.name
    : ci.ci_type_id;

  function saveAttributes(attrs: Record<string, unknown>) {
    const queue = attributeSaves.current;
    queue.pending = { id: ci.id, version: ci.version, attributes: attrs };
    if (queue.running) return;
    queue.running = true;
    void (async () => {
      try {
        while (queue.pending) {
          const next = queue.pending;
          queue.pending = null;
          const saved = queue.saved?.id === next.id ? queue.saved.version : undefined;
          const version =
            saved !== undefined && (next.version === undefined || saved > next.version)
              ? saved
              : next.version;
          const result = await updateCI.mutateAsync({
            id: next.id,
            data: { attributes: next.attributes },
            version,
          });
          if (result.version !== undefined) {
            queue.saved = { id: result.id, version: result.version };
          }
        }
      } catch {
        // The refusal is shown through the mutation state; the values typed
        // after it are not sent on the stale version.
        queue.pending = null;
      } finally {
        queue.running = false;
      }
    })();
  }

  // 409 names the fields changed since the shown version; 412 means the
  // version is unknown or missing. Both are resolved by reloading the CI.
  const attributeError = (() => {
    const err = updateCI.error;
    if (err instanceof ApiError && err.status === 409) {
      return new Error(
        t('ci.instanceFieldsConflict', {
          fields: err.conflictFields.length > 0 ? err.conflictFields.join(', ') : 'attributes',
        }),
      );
    }
    if (err instanceof ApiError && err.status === 412) {
      return new Error(t('ci.instanceFieldsPrecondition'));
    }
    return err as Error | null;
  })();

  return (
    <div className="space-y-4">
      <PageHeader
        title={ci.name}
        breadcrumbs={[{ label: t('nav.cmdb'), to: '/cmdb' }, { label: ci.name }]}
        meta={
          <span className="flex flex-wrap items-center gap-1.5">
            <Badge variant={getStatusBadgeVariant(ci.status)}>
              {t(getStatusTranslationKey(ci.status))}
            </Badge>
            <Badge variant="info">{typeLabel}</Badge>
            {ci.health ? (
              <Badge variant={healthBadgeVariant(ci.health)}>
                {t(`ci.healthState.${ci.health}`)}
              </Badge>
            ) : null}
          </span>
        }
        actions={
          <>
            <Button
              variant="secondary"
              icon={<Network />}
              onClick={() => navigate(`/topology?root=${ci.id}`)}
            >
              {t('topology.showInTopology')}
            </Button>
            <Button icon={<Pencil />} onClick={() => setIsEditOpen(true)}>
              {t('common.edit')}
            </Button>
          </>
        }
      />

      {/* A CI outside the chosen client or site is shown with a notice
          (E-87 option 1); access stays with the server (row-level security). */}
      <ScopeBanner object={ci} />

      {/* Facts about the device first, then how it connects; the quieter
          material (provenance, history, metrics) follows below. */}
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1fr)_22rem]">
        <div className="min-w-0 space-y-4">
          <Card title={t('ci.overview')}>
            <div className="divide-y divide-line [&>dl]:py-3 [&>dl:first-child]:pt-0 [&>dl:last-child]:pb-0">
              <dl>
                <DetailRow label={t('form.fields.ciType')} value={typeLabel} />
                <DetailRow label={t('ci.manufacturer')} value={ci.manufacturer} />
                <DetailRow label={t('ci.model')} value={ci.model} />
                <DetailRow label={t('ci.managementIp')} value={ci.management_ip} mono />
                <DetailRow
                  label={ci.asset ? t('ci.observedSerialNumber') : t('ci.serialNumber')}
                  value={ci.serial_number}
                  mono
                />
                <DetailRow label={t('ci.firmware')} value={ci.firmware_version} />
              </dl>
              <dl>
                <DetailRow label={t('ci.location')} value={locations.path(ci.location_id)} />
                <DetailRow label={t('ci.site')} value={locations.name(ci.site_id)} />
                <DetailRow label={t('ci.room')} value={locations.name(ci.room_id)} />
              </dl>
              <dl>
                <CIOwnerRow ci={ci} />
              </dl>
              <dl>
                <DetailRow label="ID" value={ci.id} mono />
                <DetailRow label={t('ci.source')} value={ci.discovery_source} />
              </dl>
            </div>
          </Card>

          {/* The one asset display of the CI (UI-14, WP-810): its own asset or,
              for a configuration child, the asset of the CI it derives it from. */}
          {ci.asset && <CIAssetCard ci={ci} asset={ci.asset} locationPath={locations.path} />}

          <CIRelationshipsCard ciId={ci.id} />
        </div>

        <div className="min-w-0 space-y-4">
          <CITagsCard ci={ci} />

          <Card title={t('ci.attributes')}>
            {Object.keys(ci.attributes ?? {}).length === 0 ? (
              <EmptyState title={t('ci.noAttributes')} icon={<Braces />} className="py-8" />
            ) : (
              <ScrollRegion label={t('ci.attributes')} className="rounded-lg">
                <pre className="rounded-lg border border-line bg-canvas p-3 font-mono text-xs text-fg">
                  {JSON.stringify(ci.attributes, null, 2)}
                </pre>
              </ScrollRegion>
            )}
          </Card>

          <Card title={t('topology.neighbors')}>
            {neighborsQuery.isLoading ? (
              <SkeletonList rows={3} label={t('app.loading')} />
            ) : neighborsQuery.error ? (
              <ErrorState
                title={t('topology.neighborsLoadError')}
                retryLabel={t('common.retry')}
                onRetry={() => void neighborsQuery.refetch()}
              />
            ) : neighbors.length === 0 ? (
              <EmptyState title={t('topology.noNeighbors')} icon={<Network />} className="py-8" />
            ) : (
              <ul className="-my-2 divide-y divide-line text-sm">
                {neighbors.map((neighbor) => (
                  <li
                    key={`${neighbor.id}-${neighbor.relType}-${neighbor.direction}`}
                    className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1.5 py-2.5"
                  >
                    <Link
                      to={`/cmdb/${neighbor.id}`}
                      className="min-w-0 rounded font-medium text-accent underline-offset-2 [overflow-wrap:anywhere] hover:underline"
                    >
                      {neighbor.name}
                    </Link>
                    <span className="flex flex-wrap items-center gap-1.5">
                      <Badge variant="neutral">{neighbor.relType}</Badge>
                      <Badge variant={getStatusBadgeVariant(neighbor.status)}>
                        {t(getStatusTranslationKey(neighbor.status))}
                      </Badge>
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <Card title={t('ci.timeline')}>
            <dl>
              <DetailRow
                compact
                label={t('ci.firstSeen')}
                value={formatDateTime(ci.first_seen_at, i18n.language)}
              />
              <DetailRow
                compact
                label={t('ci.lastSeen')}
                value={formatDateTime(ci.last_seen_at, i18n.language)}
              />
              <DetailRow
                compact
                label={t('ci.created')}
                value={formatDateTime(ci.created_at, i18n.language)}
              />
              <DetailRow
                compact
                label={t('ci.updated')}
                value={formatDateTime(ci.updated_at, i18n.language)}
              />
            </dl>
          </Card>
        </div>
      </div>

      {/* Advanced sections (progressive disclosure, spec §18): instance
          fields, lifecycle, impact analysis, then provenance, metrics and
          history live behind expandable sections so the simple view stays
          simple. */}
      <div className="space-y-2">
        <InstanceFieldsSection
          ciId={ci.id}
          attributes={ci.attributes ?? {}}
          onChanged={saveAttributes}
          serverError={attributeError}
        />
        {/* Templates of the CI (WP-800): only with citype:manage and the
            module metamodel; otherwise neither shown nor read (UI-12). */}
        <CITemplatesSection ciId={ci.id} ciTypeId={ci.ci_type_id} version={ci.version} />
        <LifecycleSection entityType="cis" entityId={ci.id} currentState={ci.lifecycle_state} />
        <ImpactSection ciId={ci.id} />
        <ProvenanceSection ciId={ci.id} />
        <CIMetricsSection ciId={ci.id} />
        <CIHistorySection ciId={ci.id} />
      </div>

      <CIFormModal open={isEditOpen} onOpenChange={setIsEditOpen} ci={ci} />
    </div>
  );
}

/**
 * CIAssetCard shows the asset data of a CI read-only (DB-05, AST-04, F-238)
 * by its child contract (AST-05, WP-273, WP-810): a configuration child shows
 * the asset of the CI it derives it from (asset_source_ci_id), marked as
 * such; a standalone child or a CI without parent its own asset, a child with
 * a link to its parent CI. The supplier is named from GET /suppliers/{id}; a
 * refused or unknown supplier (403, 404, e.g. module inventory off) shows a
 * dash. The deprecated fields asset_tag, warranty_end and supplier are not read.
 */
function CIAssetCard({
  ci,
  asset,
  locationPath,
}: {
  ci: CI;
  asset: CIAssetFacts;
  locationPath: (id?: string) => string | undefined;
}) {
  const { t, i18n } = useTranslation();
  const supplier = useSupplier(asset.supplier_id);
  const supplierName =
    asset.supplier_id && supplier.data?.id === asset.supplier_id ? supplier.data.name : undefined;
  const inherited = ci.child_mode === 'configuration';
  const linkClass =
    'rounded font-medium text-accent underline-offset-2 [overflow-wrap:anywhere] hover:underline';
  return (
    <Card
      title={t('ci.assetCanonical')}
      actions={
        <Link to={`/assets/${asset.id}`} className={`text-sm ${linkClass}`}>
          {t('ci.openAsset')}
        </Link>
      }
    >
      <p className="mb-3 max-w-[68ch] text-sm text-fg-muted">{t('ci.assetCanonicalHint')}</p>
      {inherited && ci.asset_source_ci_id ? (
        <p className="mb-3 flex flex-wrap items-center gap-2 text-sm">
          <Link to={`/cmdb/${ci.asset_source_ci_id}`} className={linkClass}>
            {t('ci.assetFromParent')}
          </Link>
          <Badge variant="neutral">{t('ci.assetReadOnly')}</Badge>
        </p>
      ) : ci.parent_ci_id ? (
        <p className="mb-3 text-sm">
          <Link to={`/cmdb/${ci.parent_ci_id}`} className={linkClass}>
            {t('ci.parentCi')}
          </Link>
        </p>
      ) : null}
      <dl>
        <DetailRow label={t('ci.assetTag')} value={asset.inventory_no} mono />
        <DetailRow label={t('ci.serialNumber')} value={asset.serial_number} mono />
        <DetailRow label={t('ci.location')} value={locationPath(asset.location_id)} />
        <DetailRow label={t('ci.physicalLifecycle')} value={asset.lifecycle_state} />
        <DetailRow label={t('ci.rfidTag')} value={asset.rfid_tag} mono />
        <DetailRow label={t('ci.barcode')} value={asset.barcode} mono />
        <DetailRow
          label={t('ci.purchaseDate')}
          value={formatDate(asset.purchase_date, i18n.language)}
        />
        <DetailRow
          label={t('ci.warrantyEnd')}
          value={formatDate(asset.warranty_until, i18n.language)}
        />
        <DetailRow label={t('ci.supplier')} value={supplierName} />
        <DetailRow label={t('ci.invoiceNumber')} value={asset.invoice_number} mono />
      </dl>
    </Card>
  );
}

/**
 * CIOwnerRow shows the owner of the CI (CI-07, WP-805): the responsible
 * user and team by name (GET /users/{id}, GET /teams/{id}); without
 * user:read only their kind. A CI with an asset has the asset's owner
 * (DB-05), marked as such. Not the contact role owner of ci_contact.
 */
function CIOwnerRow({ ci }: { ci: CI }) {
  const { t } = useTranslation();
  const mayRead = useHasPermission('user:read') === true;
  const user = useUser(ci.owner_user_id, { enabled: mayRead });
  const team = useTeam(ci.owner_team_id, { enabled: mayRead });
  const parts: string[] = [];
  const named = (kind: string, name?: string) =>
    name ? t('ci.owner.named', { kind, name }) : kind;
  if (ci.owner_user_id) {
    const name = user.data?.id === ci.owner_user_id ? user.data.display_name : undefined;
    parts.push(named(t('ci.owner.user'), mayRead ? name : undefined));
  }
  if (ci.owner_team_id) {
    const name = team.data?.id === ci.owner_team_id ? team.data.name : undefined;
    parts.push(named(t('ci.owner.team'), mayRead ? name : undefined));
  }
  let value = parts.join(' · ');
  if (value && ci.asset_id) value = t('ci.owner.fromAssetMark', { value });
  return <DetailRow label={t('ci.owner.title')} value={value || undefined} />;
}

/** tagRefusal words a refused PUT /cis/{id}/tags like the refusal of a PATCH. */
function tagRefusal(error: unknown, t: TFunction): string {
  if (error instanceof ApiError) {
    if (error.status === 409) {
      return t('ci.tags.conflict', {
        fields: error.conflictFields.length > 0 ? error.conflictFields.join(', ') : 'tags',
      });
    }
    if (error.status === 412) return t('ci.tags.precondition');
    if (error.status === 403) return t('ci.tags.forbidden');
    if (error.status === 422) {
      const fields = error.violations.map((v) => v.field);
      return t('ci.tags.invalid', {
        detail: fields.length > 0 ? fields.join(', ') : (error.detail ?? error.message),
      });
    }
    return t('ci.tags.reason', { detail: error.detail ?? error.message });
  }
  return t('ci.tags.reason', { detail: error instanceof Error ? error.message : String(error) });
}

/**
 * CITagsCard shows the catalog tags of a CI as colored chips and changes
 * them with a selection from the catalog (CI-08, UI-14, WP-817): PUT
 * /cis/{id}/tags with If-Match of the shown version; 412 and 409 appear as
 * with the PATCH. Only catalog tags are offered, nothing is created. The
 * catalog is read only for a CI with tags (for names and colors) and while
 * the selection is open; the change needs ci:write.
 */
function CITagsCard({ ci }: { ci: CI }) {
  const { t, i18n } = useTranslation();
  const keys = useMemo(() => ci.tags ?? [], [ci.tags]);
  const mayWrite = useHasPermission('ci:write') === true;
  const [selection, setSelection] = useState<string[] | null>(null);
  const editing = selection !== null;
  const catalog = useTags(undefined, { enabled: editing || keys.length > 0 });
  const setTags = useSetCITags();
  const byKey = useMemo(
    () => new Map((catalog.data?.data ?? []).map((tag) => [tag.key, tag])),
    [catalog.data],
  );
  // Keys of the CI the catalog does not know stay removable in the selection.
  const offered = useMemo(() => {
    const catalogKeys = (catalog.data?.data ?? []).map((tag) => tag.key);
    const unknown = (selection ?? []).filter((key) => !byKey.has(key));
    return [...catalogKeys, ...unknown.filter((key, i, all) => all.indexOf(key) === i)];
  }, [catalog.data, selection, byKey]);

  const open = () => {
    setTags.reset();
    setSelection([...keys]);
  };
  const toggle = (key: string) =>
    setSelection((current) =>
      (current ?? []).includes(key)
        ? (current ?? []).filter((k) => k !== key)
        : [...(current ?? []), key],
    );
  const save = () => {
    if (!selection) return;
    setTags.mutate(
      { id: ci.id, tags: [...selection].sort(), version: ci.version },
      { onSuccess: () => setSelection(null) },
    );
  };

  return (
    <Card
      title={t('ci.tags.title')}
      actions={
        mayWrite && !editing ? (
          <Button size="sm" variant="secondary" icon={<Pencil />} onClick={open}>
            {t('ci.tags.edit')}
          </Button>
        ) : undefined
      }
    >
      {keys.length === 0 ? (
        <p className="text-sm text-fg-muted">{t('ci.tags.none')}</p>
      ) : (
        <ul aria-label={t('ci.tags.title')} className="flex flex-wrap gap-1.5">
          {keys.map((key) => {
            const tag = byKey.get(key);
            return (
              <li
                key={key}
                className="inline-flex items-center gap-1.5 rounded-full border border-line bg-panel px-2.5 py-0.5 text-xs font-medium text-fg"
                style={tag?.color ? { borderColor: tag.color } : undefined}
              >
                <span
                  aria-hidden="true"
                  className="h-2 w-2 shrink-0 rounded-full bg-fg-subtle"
                  style={tag?.color ? { backgroundColor: tag.color } : undefined}
                />
                <span className="[overflow-wrap:anywhere]">
                  {tag ? tagLabel(tag, i18n.language) : key}
                </span>
              </li>
            );
          })}
        </ul>
      )}
      {editing ? (
        <div className="mt-4 space-y-3 border-t border-line pt-4">
          {catalog.isLoading ? (
            <SkeletonList rows={2} label={t('app.loading')} />
          ) : catalog.error ? (
            <p role="alert" className="text-sm text-danger">
              {t('ci.tags.catalogError')}
            </p>
          ) : offered.length === 0 ? (
            <p className="text-sm text-fg-muted">{t('ci.tags.catalogEmpty')}</p>
          ) : (
            <fieldset className="min-w-0">
              <legend className="text-sm font-medium text-fg">{t('ci.tags.select')}</legend>
              <div className="mt-2 grid grid-cols-1 gap-2 sm:grid-cols-2">
                {offered.map((key) => {
                  const tag = byKey.get(key);
                  return (
                    <label key={key} className="flex items-center gap-2 text-sm text-fg">
                      <input
                        type="checkbox"
                        checked={selection.includes(key)}
                        onChange={() => toggle(key)}
                      />
                      <span className="[overflow-wrap:anywhere]">
                        {tag ? tagLabel(tag, i18n.language) : `${key} (${t('ci.tags.unknown')})`}
                      </span>
                    </label>
                  );
                })}
              </div>
            </fieldset>
          )}
          {setTags.error ? (
            <p role="alert" className="text-sm text-danger">
              {tagRefusal(setTags.error, t)}
            </p>
          ) : null}
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              icon={<Tags />}
              onClick={save}
              loading={setTags.isPending}
              disabled={catalog.isLoading}
            >
              {t('common.save')}
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setSelection(null)}>
              {t('common.cancel')}
            </Button>
          </div>
        </div>
      ) : null}
    </Card>
  );
}

function healthBadgeVariant(health: string) {
  switch (health) {
    case 'critical':
      return 'danger' as const;
    case 'warning':
      return 'warning' as const;
    case 'ok':
      return 'success' as const;
    default:
      return 'neutral' as const;
  }
}
