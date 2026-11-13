/**
 * SubnetTree is the subnet tree of the IPAM page (IPM-19, WP-915). Each
 * level loads lazily over GET /subnets?section_id=…&parent_id=root|<id>
 * with section and VRF, at most API_MAX_LIMIT subnets per request, and
 * "Load more" follows the pagination. The server joins all filters with
 * AND, so a filter by text, client or site applied level by level would
 * hide every match below a parent that does not match (and an org-wide
 * pool with client subnets below it). While such a filter is set, from
 * the page or from the scope chosen in the layout, the tree gives way to
 * the flat list of the matches (GET /subnets without parent_id). A node shows CIDR, name, VRF,
 * the pool mark and a usage bar from GET /subnets/{id}/usage with the
 * share in percent and occupied/total as text for screen readers.
 * contains opens the chain of subnets that hold the address and marks the
 * narrowest one. The CIDR of a node links to the subnet detail (WP-916).
 */
import { ChevronDown, ChevronRight } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import type { Subnet, SubnetQuery } from '../../api/cmdb';
import { useSubnetLevel, useSubnetUsage } from '../../api/ipamHooks';
import { useScopeStore } from '../../stores/scope';
import { Badge } from '../ui/Badge';
import { Button } from '../ui/Button';
import { cn } from '../ui/utils';

export interface SubnetTreeProps {
  /** The section and the filters of the page (WP-909). */
  query: Omit<SubnetQuery, 'parent_id' | 'offset' | 'limit' | 'contains'>;
  /** Names of the VRFs by id. */
  vrfNames: Record<string, string>;
  /** Subnets opened from the start (the chain of contains). */
  expanded?: ReadonlySet<string>;
  /** The narrowest subnet of contains, marked. */
  marked?: string;
}

export function SubnetTree({ query, vrfNames, expanded, marked }: SubnetTreeProps) {
  const { t } = useTranslation();
  const scopeClient = useScopeStore((state) => state.clientId);
  const scopeSite = useScopeStore((state) => state.siteId);
  const filtered = !!(query.q || query.client_id || query.site_id || scopeClient || scopeSite);
  if (filtered) {
    return (
      <section aria-label={t('ipam.tree.label')}>
        <p className="mb-2 text-xs text-fg-muted">{t('ipam.tree.results')}</p>
        <SubnetLevel query={query} vrfNames={vrfNames} marked={marked} depth={0} />
      </section>
    );
  }
  return (
    <nav aria-label={t('ipam.tree.label')}>
      <SubnetLevel
        query={query}
        parentId="root"
        vrfNames={vrfNames}
        expanded={expanded}
        marked={marked}
        depth={0}
      />
    </nav>
  );
}

interface LevelProps extends SubnetTreeProps {
  /** root or the parent; absent for the flat list of the matches. */
  parentId?: string;
  depth: number;
}

function SubnetLevel({ query, parentId, vrfNames, expanded, marked, depth }: LevelProps) {
  const { t } = useTranslation();
  const flat = parentId === undefined;
  const level = useSubnetLevel(flat ? query : { ...query, parent_id: parentId });
  if (level.isPending) {
    return <p className="px-2 py-1 text-sm text-fg-muted">{t('ipam.tree.loading')}</p>;
  }
  if (level.isError) {
    return (
      <p role="alert" className="px-2 py-1 text-sm text-red-700 dark:text-red-300">
        {t('ipam.tree.loadFailed')}
      </p>
    );
  }
  const subnets = level.data.pages.flatMap((page) => page.data ?? []);
  if (subnets.length === 0) {
    return depth === 0 ? (
      <p className="px-2 py-4 text-sm text-fg-muted">
        {t(flat ? 'ipam.tree.noResults' : 'ipam.tree.empty')}
      </p>
    ) : null;
  }
  return (
    <ul className={cn('space-y-1', depth > 0 && 'ml-5 border-l border-line pl-2')}>
      {subnets.map((subnet) => (
        <SubnetNode
          key={subnet.id}
          subnet={subnet}
          query={query}
          vrfNames={vrfNames}
          expanded={expanded}
          marked={marked}
          depth={depth}
          flat={flat}
        />
      ))}
      {level.hasNextPage && (
        <li>
          <Button
            variant="ghost"
            size="sm"
            loading={level.isFetchingNextPage}
            onClick={() => void level.fetchNextPage()}
          >
            {t('ipam.tree.loadMore')}
          </Button>
        </li>
      )}
    </ul>
  );
}

interface NodeProps extends SubnetTreeProps {
  subnet: Subnet;
  depth: number;
  /** A match of the flat list: no children to open. */
  flat?: boolean;
}

function SubnetNode({ subnet, query, vrfNames, expanded, marked, depth, flat }: NodeProps) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(() => expanded?.has(subnet.id) ?? false);
  const hasChildren = !flat && (subnet.child_count ?? 0) > 0;
  const isMarked = marked === subnet.id;
  const label = subnet.name ? `${subnet.cidr} ${subnet.name}` : subnet.cidr;
  return (
    <li>
      <div
        className={cn(
          'flex flex-wrap items-center gap-2 rounded-lg px-2 py-1.5',
          isMarked && 'bg-accent/10 ring-1 ring-accent',
        )}
        data-testid={`subnet-node-${subnet.id}`}
        aria-current={isMarked ? 'true' : undefined}
      >
        {hasChildren ? (
          <button
            type="button"
            className="rounded p-0.5 text-fg-muted hover:bg-fg/[0.06] focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
            aria-expanded={open}
            aria-label={t(open ? 'ipam.tree.collapse' : 'ipam.tree.expand', { subnet: label })}
            onClick={() => setOpen((v) => !v)}
          >
            {open ? (
              <ChevronDown className="h-4 w-4" aria-hidden />
            ) : (
              <ChevronRight className="h-4 w-4" aria-hidden />
            )}
          </button>
        ) : (
          <span className="inline-block w-5" aria-hidden />
        )}
        <Link
          to={`/ipam/subnets/${subnet.id}`}
          className="rounded font-mono text-sm font-medium text-fg underline-offset-2 hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
        >
          {subnet.cidr}
        </Link>
        {subnet.name && <span className="text-sm text-fg">{subnet.name}</span>}
        <span className="text-xs text-fg-muted">
          {t('ipam.tree.vrf', { vrf: (subnet.vrf_id && vrfNames[subnet.vrf_id]) || subnet.vrf_id })}
        </span>
        {subnet.is_pool && <Badge>{t('ipam.pool.true')}</Badge>}
        {isMarked && <Badge>{t('ipam.tree.narrowest')}</Badge>}
        <UsageBar subnetId={subnet.id} label={label} />
      </div>
      {open && hasChildren && (
        <SubnetLevel
          query={query}
          parentId={subnet.id}
          vrfNames={vrfNames}
          expanded={expanded}
          marked={marked}
          depth={depth + 1}
        />
      )}
    </li>
  );
}

/**
 * UsageBar is the usage of a subnet: a bar with the share in percent and
 * occupied of total as text (a pool counts what its children cover).
 */
function UsageBar({ subnetId, label }: { subnetId: string; label: string }) {
  const { t } = useTranslation();
  const usage = useSubnetUsage(subnetId);
  if (!usage.data) {
    return null;
  }
  const u = usage.data;
  const used = u.is_pool ? (u.covered_text ?? String(u.covered ?? 0)) : String(u.used);
  const total = u.total_text ?? (u.total !== undefined ? String(u.total) : '');
  const percent = Math.max(0, Math.min(100, u.percent));
  const text = t('ipam.usage.text', { used, total, percent: percent.toFixed(1) });
  return (
    <span className="ml-auto flex items-center gap-2">
      <span
        role="meter"
        aria-label={t('ipam.usage.label', { subnet: label })}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent}
        aria-valuetext={text}
        className="block h-2 w-28 overflow-hidden rounded-full bg-fg/[0.08]"
      >
        <span
          className={cn(
            'block h-full rounded-full',
            percent >= 90 ? 'bg-red-600' : percent >= 70 ? 'bg-amber-500' : 'bg-accent',
          )}
          style={{ width: `${percent}%` }}
        />
      </span>
      <span className="text-xs tabular-nums text-fg-muted">{text}</span>
    </span>
  );
}
