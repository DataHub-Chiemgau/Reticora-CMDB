import { useRef, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { ApiError } from '../api/client';
import type { TopologyGraphData } from '../api/client';
import { useCI, useCINeighbors, useUpdateCI } from '../api/hooks';
import { localizedText } from '../api/cmdb';
import { useCIType, useCITypes } from '../api/cmdbHooks';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { SkeletonList } from '../components/ui/Skeleton';
import { CIFormModal } from './CIFormModal';
import { getStatusBadgeVariant, getStatusTranslationKey } from './ciStatus';
import {
  InstanceFieldsSection,
  ProvenanceSection,
  LifecycleSection,
  ImpactSection,
  CIMetricsSection,
  CIHistorySection,
} from '../components/cmdb/CIDetailSections';
import { CIRelationshipsCard } from '../components/cmdb/CIRelationshipsCard';
import { CIParentAssetCard } from '../components/cmdb/CIParentAssetCard';
import { ScopeBanner } from '../components/ScopeBanner';

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
        ciType: node.ci_type,
        status: node.status,
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
      <nav aria-label={t('accessibility.breadcrumb')} className="text-sm">
        <Link
          to="/cmdb"
          className="text-primary underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
        >
          ← {t('nav.cmdb')}
        </Link>
      </nav>

      {/* A CI outside the chosen client or site is shown with a notice
          (E-87 option 1); access stays with the server (row-level security). */}
      <ScopeBanner object={ci} />

      <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">{ci.name}</h2>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <Badge variant={getStatusBadgeVariant(ci.status)}>
              {t(getStatusTranslationKey(ci.status))}
            </Badge>
            <Badge variant="info">{typeLabel}</Badge>
            {ci.health ? (
              <Badge variant={healthBadgeVariant(ci.health)}>
                {t(`ci.healthState.${ci.health}`)}
              </Badge>
            ) : null}
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button variant="secondary" onClick={() => setIsEditOpen(true)}>
            {t('common.edit')}
          </Button>
          <Button variant="secondary" onClick={() => navigate(`/topology?root=${ci.id}`)}>
            {t('topology.showInTopology')}
          </Button>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Card title={t('ci.overview')}>
          <dl className="grid grid-cols-1 gap-3 text-sm sm:grid-cols-2">
            <DetailRow label="ID" value={ci.id} />
            <DetailRow label={t('form.fields.ciType')} value={typeLabel} />
            <DetailRow label={t('ci.manufacturer')} value={ci.manufacturer} />
            <DetailRow label={t('ci.model')} value={ci.model} />
            <DetailRow
              label={ci.asset ? t('ci.observedSerialNumber') : t('ci.serialNumber')}
              value={ci.serial_number}
            />
            <DetailRow label={t('ci.managementIp')} value={ci.management_ip} />
            <DetailRow label={t('ci.firmware')} value={ci.firmware_version} />
            <DetailRow label={t('ci.source')} value={ci.discovery_source} />
            <DetailRow label={t('ci.location')} value={ci.location_id} />
            <DetailRow label={t('ci.site')} value={ci.site_id} />
            <DetailRow label={t('ci.room')} value={ci.room_id} />
            <DetailRow label={t('ci.firstSeen')} value={ci.first_seen_at} />
            <DetailRow label={t('ci.lastSeen')} value={ci.last_seen_at} />
            <DetailRow label={t('ci.created')} value={ci.created_at} />
            <DetailRow label={t('ci.updated')} value={ci.updated_at} />
          </dl>
        </Card>

        <Card title={t('ci.attributes')}>
          {Object.keys(ci.attributes ?? {}).length === 0 ? (
            <EmptyState title={t('ci.noAttributes')} />
          ) : (
            <pre className="overflow-x-auto rounded-lg bg-gray-100 p-3 text-xs dark:bg-gray-950">
              {JSON.stringify(ci.attributes, null, 2)}
            </pre>
          )}
        </Card>

        {ci.asset && (
          <Card title={t('ci.assetCanonical')}>
            <p className="mb-3 text-xs text-gray-500 dark:text-gray-400">
              {t('ci.assetCanonicalHint')}
            </p>
            <dl className="grid grid-cols-1 gap-3 text-sm sm:grid-cols-2">
              <DetailRow label={t('ci.assetTag')} value={ci.asset.asset_tag} />
              <DetailRow label={t('ci.serialNumber')} value={ci.asset.serial_number} />
              <DetailRow label={t('ci.location')} value={ci.asset.location_id} />
              <DetailRow label={t('ci.physicalLifecycle')} value={ci.asset.lifecycle_state} />
              <DetailRow label={t('ci.barcode')} value={ci.asset.barcode} />
              <DetailRow label={t('ci.supplier')} value={ci.asset.supplier} />
              <DetailRow label={t('ci.warrantyEnd')} value={ci.asset.warranty_end} />
            </dl>
          </Card>
        )}

        <CIParentAssetCard ciId={ci.id} />

        <CIRelationshipsCard ciId={ci.id} />

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
            <EmptyState title={t('topology.noNeighbors')} />
          ) : (
            <ul className="space-y-2 text-sm">
              {neighbors.map((neighbor) => (
                <li
                  key={`${neighbor.id}-${neighbor.relType}-${neighbor.direction}`}
                  className="flex flex-wrap items-center justify-between gap-2 border-b border-gray-100 pb-2 last:border-0 dark:border-gray-800"
                >
                  <Link
                    to={`/cmdb/${neighbor.id}`}
                    className="font-medium text-primary underline-offset-2 hover:underline"
                  >
                    {neighbor.name}
                  </Link>
                  <span className="flex items-center gap-2">
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
      </div>

      {/* Advanced sections (progressive disclosure, spec §18): instance
          fields, provenance/overrides, lifecycle, and impact analysis live
          behind expandable sections so the simple view stays simple. */}
      <div className="space-y-3">
        <InstanceFieldsSection
          ciId={ci.id}
          attributes={ci.attributes ?? {}}
          onChanged={saveAttributes}
          serverError={attributeError}
        />
        <ProvenanceSection ciId={ci.id} />
        <LifecycleSection entityType="cis" entityId={ci.id} currentState={ci.lifecycle_state} />
        <ImpactSection ciId={ci.id} />
        <CIMetricsSection ciId={ci.id} />
        <CIHistorySection ciId={ci.id} />
      </div>

      <CIFormModal open={isEditOpen} onOpenChange={setIsEditOpen} ci={ci} />
    </div>
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

function DetailRow({ label, value }: { label: string; value?: string }) {
  return (
    <div>
      <dt className="text-gray-500 dark:text-gray-400">{label}</dt>
      <dd className="break-all font-medium text-gray-900 dark:text-gray-100">{value || '—'}</dd>
    </div>
  );
}
