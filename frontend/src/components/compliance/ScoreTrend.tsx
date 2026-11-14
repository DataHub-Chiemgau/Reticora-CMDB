import { useId } from 'react';
import { useTranslation } from 'react-i18next';

/** One day of the score trend. */
export interface ScorePoint {
  day: string;
  score: number;
}

export interface ScoreTrendProps {
  points: readonly ScorePoint[];
  /** The label of the chart and its table. */
  label: string;
}

const WIDTH = 600;
const HEIGHT = 160;
const PAD = 8;

/**
 * ScoreTrend draws the daily compliance score (0–100 %) as an SVG line
 * without a chart library (SEC-03, WP-1135 AK3). Screen readers get the
 * values as a table instead of the picture (the SVG is hidden from them).
 */
export function ScoreTrend({ points, label }: ScoreTrendProps) {
  const { t, i18n } = useTranslation();
  const tableId = useId();
  const first = points[0];
  const last = points[points.length - 1];
  if (!first || !last) {
    return <p className="text-sm text-fg-muted">{t('compliance.trend.empty')}</p>;
  }
  const step = points.length > 1 ? (WIDTH - 2 * PAD) / (points.length - 1) : 0;
  const y = (score: number) =>
    PAD + (HEIGHT - 2 * PAD) * (1 - Math.min(100, Math.max(0, score)) / 100);
  const path = points
    .map((p, i) => `${(PAD + i * step).toFixed(1)},${y(p.score).toFixed(1)}`)
    .join(' ');
  const format = new Intl.NumberFormat(i18n.language, { maximumFractionDigits: 1 });
  return (
    <figure className="space-y-2">
      <svg
        viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
        className="h-40 w-full text-primary"
        aria-hidden="true"
        focusable="false"
        data-testid="score-trend-line"
      >
        <line
          x1={PAD}
          x2={WIDTH - PAD}
          y1={y(100)}
          y2={y(100)}
          className="stroke-line"
          strokeDasharray="4 4"
        />
        <line x1={PAD} x2={WIDTH - PAD} y1={y(0)} y2={y(0)} className="stroke-line" />
        {points.length > 1 ? (
          <polyline fill="none" stroke="currentColor" strokeWidth={2} points={path} />
        ) : (
          <circle cx={PAD} cy={y(first.score)} r={3} fill="currentColor" />
        )}
      </svg>
      <figcaption className="text-xs text-fg-muted">
        {t('compliance.trend.caption', {
          first: first.day,
          last: last.day,
          score: format.format(last.score),
        })}
      </figcaption>
      <table className="sr-only" id={tableId} aria-label={label}>
        <thead>
          <tr>
            <th scope="col">{t('compliance.trend.day')}</th>
            <th scope="col">{t('compliance.score')}</th>
          </tr>
        </thead>
        <tbody>
          {points.map((p) => (
            <tr key={p.day}>
              <td>{p.day}</td>
              <td>{`${format.format(p.score)} %`}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </figure>
  );
}
