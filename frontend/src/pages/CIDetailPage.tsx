import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { TopologyGraphData } from '../api/client';
import { useCI, useCINeighbors, useUpdateCI } from '../api/hooks';
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
} from '../components/cmdb/CIDetailSections';
import { CIRelationshipsCard } from '../components/cmdb/CIRelationshipsCard';

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
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { id = '' } = useParams<{ id: string }>();
  const [isEditOpen, setIsEditOpen] = useState(false);

  const ciQuery = useCI(id);
  const neighborsQuery = useCINeighbors(id);
  const updateCI = useUpdateCI();

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

      <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">{ci.name}</h2>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <Badge variant={getStatusBadgeVariant(ci.status)}>
              {t(getStatusTranslationKey(ci.status))}
            </Badge>
            <Badge variant="info">{ci.ci_type_id}</Badge>
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
            <DetailRow label={t('form.fields.ciType')} value={ci.ci_type_id} />
            <DetailRow label={t('ci.manufacturer')} value={ci.manufacturer} />
            <DetailRow label={t('ci.model')} value={ci.model} />
            <DetailRow label={t('ci.serialNumber')} value={ci.serial_number} />
            <DetailRow label={t('ci.managementIp')} value={ci.management_ip} />
            <DetailRow label={t('ci.firmware')} value={ci.firmware_version} />
            <DetailRow label={t('ci.source')} value={ci.discovery_source} />
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
          onChanged={(attrs) => updateCI.mutate({ id: ci.id, data: { attributes: attrs } })}
          serverError={updateCI.error as Error | null}
        />
        <ProvenanceSection ciId={ci.id} />
        <LifecycleSection
          entityType="cis"
          entityId={ci.id}
          currentState={(ci as { lifecycle_state?: string }).lifecycle_state}
        />
        <ImpactSection ciId={ci.id} />
      </div>

      <CIFormModal open={isEditOpen} onOpenChange={setIsEditOpen} ci={ci} />
    </div>
  );
}

function DetailRow({ label, value }: { label: string; value?: string }) {
  return (
    <div>
      <dt className="text-gray-500 dark:text-gray-400">{label}</dt>
      <dd className="break-all font-medium text-gray-900 dark:text-gray-100">{value || '—'}</dd>
    </div>
  );
}
