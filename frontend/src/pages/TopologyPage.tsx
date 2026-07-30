import { useMemo } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { TopologyGraphData } from '../api/client';
import { useTopology } from '../api/hooks';
import { TopologyGraph } from '../components/graph/TopologyGraph';
import type { GraphEdge, GraphNode } from '../components/graph/TopologyGraph';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { getStatusBadgeVariant, getStatusTranslationKey } from './ciStatus';

const statusColors: Record<string, string> = {
  active: '#16a34a',
  inactive: '#6b7280',
  maintenance: '#d97706',
  decommissioned: '#dc2626',
};

export interface GraphModel {
  nodes: GraphNode[];
  edges: GraphEdge[];
}

/**
 * Maps the API topology graph onto the renderer model. Edges whose endpoints
 * are not part of the returned node set are dropped, because the renderer
 * cannot place an edge without both of its nodes.
 */
export function buildGraphModel(graph: TopologyGraphData): GraphModel {
  const nodeIds = new Set(graph.nodes.map((node) => node.id));

  return {
    nodes: graph.nodes.map((node) => ({
      id: node.id,
      label: node.name,
      color: statusColors[node.status] ?? '#4f46e5',
    })),
    edges: graph.edges
      .filter((edge) => nodeIds.has(edge.source_ci_id) && nodeIds.has(edge.target_ci_id))
      .map((edge) => ({
        source: edge.source_ci_id,
        target: edge.target_ci_id,
        label: edge.rel_type,
      })),
  };
}

const ciTypeValues = ['server', 'switch', 'router', 'firewall', 'pdu', 'ups', 'nas', 'client'];
const depthValues = ['1', '2', '3', '4', '5'];

export function TopologyPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();

  const rootCiId = searchParams.get('root') ?? '';
  const ciType = searchParams.get('type') ?? '';
  const depth = searchParams.get('depth') ?? '2';

  const { data, isLoading, error, refetch } = useTopology({
    root_ci_id: rootCiId || undefined,
    ci_type: ciType || undefined,
    depth: rootCiId ? Number(depth) : undefined,
  });

  const model = useMemo(() => (data ? buildGraphModel(data) : { nodes: [], edges: [] }), [data]);

  function updateParam(key: string, value: string) {
    const next = new URLSearchParams(searchParams);
    if (value) {
      next.set(key, value);
    } else {
      next.delete(key);
    }
    setSearchParams(next, { replace: true });
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
            {t('nav.topology')}
          </h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('topology.subtitle')}</p>
        </div>
        {rootCiId ? (
          <Button variant="secondary" onClick={() => updateParam('root', '')}>
            {t('topology.clearRoot')}
          </Button>
        ) : null}
      </div>

      <Card>
        <div className="flex flex-wrap gap-3">
          <Select
            value={ciType}
            onChange={(event) => updateParam('type', event.target.value)}
            aria-label={t('topology.filterType')}
            className="max-w-xs"
            options={[
              { value: '', label: t('topology.allTypes') },
              ...ciTypeValues.map((value) => ({
                value,
                label: value.charAt(0).toUpperCase() + value.slice(1),
              })),
            ]}
          />
          <Select
            value={depth}
            onChange={(event) => updateParam('depth', event.target.value)}
            aria-label={t('topology.filterDepth')}
            className="max-w-xs"
            disabled={!rootCiId}
            options={depthValues.map((value) => ({
              value,
              label: `${t('topology.depth')} ${value}`,
            }))}
          />
        </div>
      </Card>

      {isLoading ? <SkeletonList rows={4} label={t('app.loading')} /> : null}

      {error ? (
        <ErrorState
          title={t('topology.loadError')}
          description={error instanceof Error ? error.message : undefined}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : null}

      {data && model.nodes.length === 0 ? (
        <EmptyState title={t('topology.empty')} description={t('topology.emptyHint')} />
      ) : null}

      {data && model.nodes.length > 0 ? (
        <>
          <Card className="p-0">
            <div className="h-[28rem] w-full overflow-hidden rounded-2xl">
              <TopologyGraph nodes={model.nodes} edges={model.edges} />
            </div>
          </Card>

          <Card title={t('topology.nodeList')}>
            <p className="mb-3 text-sm text-gray-600 dark:text-gray-300">
              {t('topology.nodeListHint')}
            </p>
            <ul className="space-y-2 text-sm">
              {data.nodes.map((node) => (
                <li
                  key={node.id}
                  className="flex flex-wrap items-center justify-between gap-2 border-b border-gray-100 pb-2 last:border-0 dark:border-gray-800"
                >
                  <button
                    type="button"
                    onClick={() => navigate(`/cmdb/${node.id}`)}
                    className="font-medium text-primary underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
                  >
                    {node.name}
                  </button>
                  <span className="flex items-center gap-2">
                    <Badge variant="info">{node.ci_type}</Badge>
                    <Badge variant={getStatusBadgeVariant(node.status)}>
                      {t(getStatusTranslationKey(node.status))}
                    </Badge>
                    <Button variant="ghost" size="sm" onClick={() => updateParam('root', node.id)}>
                      {t('topology.focus')}
                    </Button>
                  </span>
                </li>
              ))}
            </ul>
          </Card>
        </>
      ) : null}
    </div>
  );
}
