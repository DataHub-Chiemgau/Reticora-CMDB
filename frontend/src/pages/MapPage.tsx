import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { siteApi } from '../api/client';
import type { Site } from '../api/client';
import { Card } from '../components/ui/Card';
import { Skeleton } from '../components/ui/Skeleton';
import { ErrorState } from '../components/ui/ErrorState';

/**
 * MapPage renders a dependency-free SVG world map (equirectangular
 * projection) with site markers. Sites without coordinates are listed
 * separately. Clicking a marker opens the site detail.
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

function project(lat: number, lon: number, width: number, height: number): { x: number; y: number } {
  const x = ((lon + 180) / 360) * width;
  // clamp latitude to the visible Web-Mercator-ish band and invert Y
  const clamped = Math.max(-80, Math.min(84, lat));
  const y = ((84 - clamped) / 168) * height;
  return { x, y };
}

function SiteMarker({ site, onOpen }: { site: Site; onOpen: (id: string) => void }) {
  if (site.geo_lat == null || site.geo_lon == null) return null;
  const { x, y } = project(site.geo_lat, site.geo_lon, 100, 50);
  return (
    <g
      role="button"
      tabIndex={0}
      aria-label={site.name}
      onClick={() => onOpen(site.id)}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') onOpen(site.id);
      }}
      className="cursor-pointer focus:outline-none"
    >
      <circle cx={x} cy={y} r={1.6} className="fill-primary opacity-90" />
      <circle cx={x} cy={y} r={3.2} className="fill-primary opacity-20" />
      <title>{site.name}</title>
    </g>
  );
}

export function MapPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [selected, setSelected] = useState<Site | null>(null);

  const { data, isLoading, error } = useQuery({
    queryKey: ['sites', 'map'],
    queryFn: () => siteApi.list({ limit: 500 }),
  });

  const sites = useMemo(() => data?.data ?? [], [data]);
  const withGeo = sites.filter((s) => s.geo_lat != null && s.geo_lon != null);
  const withoutGeo = sites.filter((s) => s.geo_lat == null || s.geo_lon == null);

  const openSite = (id: string) => {
    const site = sites.find((s) => s.id === id);
    if (site) setSelected(site);
  };

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h2 className="text-2xl font-bold text-gray-900 dark:text-gray-100">
            {t('nav.map', 'Karte')}
          </h2>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">
            {t('map.summary', 'Standorte auf der Karte. Marker öffnen den Standort.')}
          </p>
        </div>
        <span className="text-sm text-gray-500 dark:text-gray-400">
          {withGeo.length} {t('map.located', 'lokalisiert')} · {withoutGeo.length}{' '}
          {t('map.unlocated', 'ohne Koordinaten')}
        </span>
      </div>

      {isLoading ? (
        <Skeleton className="h-96 w-full" />
      ) : error ? (
        <ErrorState title={t('app.error')} />
      ) : (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
          <Card className="lg:col-span-2">
            <svg
              viewBox="0 0 100 50"
              role="img"
              aria-label={t('map.worldAria', 'Weltkarte mit Standortmarkern')}
              className="h-auto w-full rounded-lg bg-blue-50 dark:bg-gray-900"
            >
              {LANDMASSES.map((d, i) => (
                <path key={i} d={d} className="fill-gray-300 dark:fill-gray-700" />
              ))}
              {withGeo.map((site) => (
                <SiteMarker key={site.id} site={site} onOpen={openSite} />
              ))}
            </svg>
          </Card>

          <div className="space-y-3">
            {selected ? (
              <Card>
                <h3 className="text-lg font-semibold text-gray-900 dark:text-gray-100">
                  {selected.name}
                </h3>
                {selected.address ? (
                  <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{selected.address}</p>
                ) : null}
                <p className="mt-1 text-xs text-gray-500 dark:text-gray-400">
                  {selected.geo_lat?.toFixed(4)}, {selected.geo_lon?.toFixed(4)}
                </p>
                <button
                  type="button"
                  onClick={() => navigate(`/cmdb?site_id=${selected.id}`)}
                  className="mt-3 text-sm font-medium text-primary hover:underline"
                >
                  {t('map.openSite', 'Standort-CIs anzeigen')} →
                </button>
              </Card>
            ) : (
              <Card>
                <p className="text-sm text-gray-500 dark:text-gray-400">
                  {t('map.hint', 'Wählen Sie einen Marker, um Details zu sehen.')}
                </p>
              </Card>
            )}

            {withoutGeo.length > 0 ? (
              <Card>
                <h4 className="text-sm font-semibold text-gray-700 dark:text-gray-300">
                  {t('map.noCoords', 'Ohne Koordinaten')}
                </h4>
                <ul className="mt-2 space-y-1 text-sm text-gray-600 dark:text-gray-300">
                  {withoutGeo.map((s) => (
                    <li key={s.id}>{s.name}</li>
                  ))}
                </ul>
              </Card>
            ) : null}
          </div>
        </div>
      )}
    </div>
  );
}
