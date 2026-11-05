import { ArrowRight, MapPin } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router-dom';
import { siteApi } from '../api/client';
import type { Site } from '../api/client';
import { Panel } from '../components/spatial/Panel';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { PageHeader } from '../components/ui/PageHeader';
import { Skeleton } from '../components/ui/Skeleton';

/**
 * MapPage renders a dependency-free SVG world map (equirectangular
 * projection) with site markers. Sites without coordinates are listed
 * separately. Clicking a marker opens the site detail.
 *
 * The map is a labelled group, not an image: the markers inside it are
 * buttons, which an element with role="img" must not contain (axe
 * nested-interactive, F-356). Each marker takes the focus, shows a focus
 * ring, and opens the site with Enter or Space (WP-631).
 */

// Rough continent outlines as normalized [0..1] polygons for orientation.
// Simplified, low-detail shapes — enough to read geography at a glance.
const LANDMASSES: string[] = [
  // North America
  'M5,18 L28,10 L38,16 L40,28 L30,40 L18,44 L8,36 Z',
  // South America
  'M24,50 L34,48 L38,60 L34,78 L27,80 L22,64 Z',
  // Europe
  'M46,12 L58,10 L60,20 L52,26 L46,22 Z',
  // Africa
  'M46,30 L60,28 L66,44 L60,66 L50,68 L44,50 Z',
  // Asia
  'M60,8 L92,10 L96,26 L84,40 L68,38 L60,24 Z',
  // Australia
  'M80,60 L94,58 L96,70 L86,74 L78,68 Z',
];

function project(
  lat: number,
  lon: number,
  width: number,
  height: number,
): { x: number; y: number } {
  const x = ((lon + 180) / 360) * width;
  // clamp latitude to the visible Web-Mercator-ish band and invert Y
  const clamped = Math.max(-80, Math.min(84, lat));
  const y = ((84 - clamped) / 168) * height;
  return { x, y };
}

// A graticule every 10 % of the width/height: structure that makes the
// position of a marker readable without a real basemap.
const GRATICULE_X = [10, 20, 30, 40, 50, 60, 70, 80, 90];
const GRATICULE_Y = [10, 20, 30, 40];

function SiteMarker({
  site,
  selected,
  onOpen,
}: {
  site: Site;
  selected: boolean;
  onOpen: (id: string) => void;
}) {
  if (site.geo_lat == null || site.geo_lon == null) return null;
  const { x, y } = project(site.geo_lat, site.geo_lon, 100, 50);
  return (
    <g
      role="button"
      tabIndex={0}
      aria-label={site.name}
      aria-pressed={selected}
      onClick={() => onOpen(site.id)}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          // Space would scroll the page otherwise.
          e.preventDefault();
          onOpen(site.id);
        }
      }}
      className="group cursor-pointer focus:outline-none"
    >
      <circle
        cx={x}
        cy={y}
        r={3.2}
        className={
          selected
            ? 'fill-accent opacity-30'
            : 'fill-accent opacity-15 transition-opacity group-hover:opacity-30'
        }
      />
      <circle cx={x} cy={y} r={1.4} strokeWidth={0.5} className="fill-accent stroke-panel" />
      <circle
        cx={x}
        cy={y}
        r={4.2}
        strokeWidth={0.5}
        className="fill-none stroke-fg opacity-0 group-focus-visible:opacity-100"
      />
      <title>{site.name}</title>
    </g>
  );
}

export function MapPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [selected, setSelected] = useState<Site | null>(null);

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['sites', 'map'],
    // Every site, page by page: the API answers at most 200 per page (F-362).
    queryFn: () => siteApi.listAll(),
  });

  const sites = useMemo(() => data ?? [], [data]);
  const withGeo = sites.filter((s) => s.geo_lat != null && s.geo_lon != null);
  const withoutGeo = sites.filter((s) => s.geo_lat == null || s.geo_lon == null);

  const openSite = (id: string) => {
    const site = sites.find((s) => s.id === id);
    if (site) setSelected(site);
  };

  return (
    <div className="space-y-4">
      <PageHeader
        title={t('nav.map', 'Karte')}
        description={t('map.summary', 'Standorte auf der Karte. Marker öffnen den Standort.')}
        meta={
          data ? (
            <span className="flex flex-wrap items-center gap-1.5">
              <Badge variant="neutral">
                {withGeo.length} {t('map.located', 'lokalisiert')}
              </Badge>
              <Badge variant="neutral">
                {withoutGeo.length} {t('map.unlocated', 'ohne Koordinaten')}
              </Badge>
            </span>
          ) : null
        }
      />

      {isLoading ? (
        <Skeleton className="h-96 w-full" />
      ) : error ? (
        <ErrorState
          title={t('app.error')}
          retryLabel={t('common.retry')}
          onRetry={() => void refetch()}
        />
      ) : sites.length === 0 ? (
        <EmptyState
          icon={<MapPin />}
          title={t('map.empty')}
          description={t('map.emptyHint')}
          action={
            <Button asChild variant="secondary">
              <Link to="/locations">{t('nav.locations')}</Link>
            </Button>
          }
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_20rem]">
          <div className="min-w-0 overflow-hidden rounded-xl border border-line bg-panel">
            <svg
              viewBox="0 0 100 50"
              role="group"
              aria-label={t('map.worldAria', 'Weltkarte mit Standortmarkern')}
              className="block h-auto w-full"
            >
              {GRATICULE_X.map((x) => (
                <line
                  key={`x${x}`}
                  x1={x}
                  y1={0}
                  x2={x}
                  y2={50}
                  strokeWidth={0.1}
                  className="stroke-line"
                />
              ))}
              {GRATICULE_Y.map((y) => (
                <line
                  key={`y${y}`}
                  x1={0}
                  y1={y}
                  x2={100}
                  y2={y}
                  strokeWidth={0.1}
                  className="stroke-line"
                />
              ))}
              {/* The outlines are drawn on a 100 x 100 grid: squeeze them into the 2:1 map. */}
              <g transform="scale(1 0.5)">
                {LANDMASSES.map((d, i) => (
                  <path
                    key={i}
                    d={d}
                    strokeWidth={1}
                    strokeLinejoin="round"
                    className="fill-fg/10 stroke-line-strong [vector-effect:non-scaling-stroke]"
                  />
                ))}
              </g>
              {withGeo.map((site) => (
                <SiteMarker
                  key={site.id}
                  site={site}
                  selected={selected?.id === site.id}
                  onOpen={openSite}
                />
              ))}
            </svg>
          </div>

          <div className="min-w-0 space-y-4">
            {selected ? (
              <Panel
                title={selected.name}
                leading={<MapPin className="text-accent" />}
                onClose={() => setSelected(null)}
                closeLabel={t('accessibility.closePanel')}
              >
                {selected.address ? (
                  <p className="text-sm text-fg-muted">{selected.address}</p>
                ) : null}
                <p className="mt-1 font-mono text-xs text-fg-muted">
                  {selected.geo_lat?.toFixed(4)}, {selected.geo_lon?.toFixed(4)}
                </p>
                <Button
                  variant="secondary"
                  size="sm"
                  className="mt-3"
                  iconAfter={<ArrowRight />}
                  onClick={() => navigate(`/cmdb?site_id=${selected.id}`)}
                >
                  {t('map.openSite', 'Standort-CIs anzeigen')}
                </Button>
              </Panel>
            ) : (
              <p className="rounded-xl border border-dashed border-line-strong px-4 py-6 text-center text-sm text-fg-muted">
                {t('map.hint', 'Wählen Sie einen Marker, um Details zu sehen.')}
              </p>
            )}

            {withoutGeo.length > 0 ? (
              <Panel
                title={t('map.noCoords', 'Ohne Koordinaten')}
                actions={<Badge variant="neutral">{withoutGeo.length}</Badge>}
                padded={false}
              >
                <ul className="max-h-72 divide-y divide-line overflow-y-auto text-sm text-fg">
                  {withoutGeo.map((s) => (
                    <li key={s.id} className="px-4 py-2">
                      {s.name}
                    </li>
                  ))}
                </ul>
              </Panel>
            ) : null}
          </div>
        </div>
      )}
    </div>
  );
}
