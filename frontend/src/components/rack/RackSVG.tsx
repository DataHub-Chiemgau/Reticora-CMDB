import { FC } from 'react';

export interface RackUnit {
  position: number;
  height: number;
  label: string;
  color?: string;
  id?: string;
}

export interface RackSVGProps {
  units: number;
  items: RackUnit[];
  width?: number;
  className?: string;
  onItemClick?: (item: RackUnit) => void;
}

const UNIT_HEIGHT = 20;
const LABEL_WIDTH = 30;

/**
 * RackSVG renders a server rack diagram as an SVG element.
 * Each unit is 1U (20px), items can span multiple units.
 */
export const RackSVG: FC<RackSVGProps> = ({
  units,
  items,
  width = 300,
  className,
  onItemClick,
}) => {
  const totalHeight = units * UNIT_HEIGHT;
  const rackWidth = width - LABEL_WIDTH;

  return (
    <svg
      className={className}
      width={width}
      height={totalHeight + 2}
      viewBox={`0 0 ${width} ${totalHeight + 2}`}
      xmlns="http://www.w3.org/2000/svg"
    >
      {/* Rack frame */}
      <rect x={LABEL_WIDTH} y={1} width={rackWidth} height={totalHeight} fill="#1f2937" stroke="#374151" strokeWidth={1} />

      {/* Unit lines and labels */}
      {Array.from({ length: units }, (_, i) => {
        const y = i * UNIT_HEIGHT + 1;
        return (
          <g key={`unit-${i}`}>
            <line x1={LABEL_WIDTH} y1={y} x2={width} y2={y} stroke="#374151" strokeWidth={0.5} />
            <text x={LABEL_WIDTH - 4} y={y + UNIT_HEIGHT / 2 + 4} textAnchor="end" fontSize={9} fill="#9ca3af">
              {units - i}
            </text>
          </g>
        );
      })}

      {/* Rack items */}
      {items.map((item, idx) => {
        const y = (units - item.position - item.height + 1) * UNIT_HEIGHT + 1;
        const itemHeight = item.height * UNIT_HEIGHT - 2;
        return (
          <g
            key={item.id ?? idx}
            onClick={() => onItemClick?.(item)}
            style={{ cursor: onItemClick ? 'pointer' : 'default' }}
          >
            <rect
              x={LABEL_WIDTH + 2}
              y={y + 1}
              width={rackWidth - 4}
              height={itemHeight}
              rx={2}
              fill={item.color ?? '#4f46e5'}
              stroke="#6366f1"
              strokeWidth={0.5}
            />
            <text
              x={LABEL_WIDTH + rackWidth / 2}
              y={y + itemHeight / 2 + 4}
              textAnchor="middle"
              fontSize={10}
              fill="#ffffff"
            >
              {item.label}
            </text>
          </g>
        );
      })}
    </svg>
  );
};
