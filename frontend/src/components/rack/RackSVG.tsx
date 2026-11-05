import { FC } from 'react';

export interface RackUnit {
  position: number;
  height: number;
  label: string;
  color?: string;
  id?: string;
  /** title is the accessible name of the item (SVG title), e.g. its faces. */
  title?: string;
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

// The default colour of an item without its own colour (indigo-600): the
// white label reaches 6.3:1 on it, in both themes alike.
const DEFAULT_ITEM_COLOR = '#087680';

/**
 * RackSVG renders a server rack diagram as an SVG element.
 * Each unit is 1U (20px), items can span multiple units. The frame, the unit
 * lines and the unit numbers follow the theme through fill-/stroke- classes
 * from the design tokens (as the map in MapPage); the unit numbers sit outside
 * the frame on the panel background and keep 4.5:1 there in both themes.
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
      <rect
        x={LABEL_WIDTH}
        y={1}
        width={rackWidth}
        height={totalHeight}
        strokeWidth={1}
        className="fill-canvas stroke-line-strong"
      />

      {/* Unit lines and labels */}
      {Array.from({ length: units }, (_, i) => {
        const y = i * UNIT_HEIGHT + 1;
        return (
          <g key={`unit-${i}`}>
            <line
              x1={LABEL_WIDTH}
              y1={y}
              x2={width}
              y2={y}
              strokeWidth={0.5}
              className="stroke-line"
            />
            <text
              x={LABEL_WIDTH - 4}
              y={y + UNIT_HEIGHT / 2 + 4}
              textAnchor="end"
              fontSize={9}
              className="fill-fg-muted"
            >
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
            {item.title ? <title>{item.title}</title> : null}
            <rect
              x={LABEL_WIDTH + 2}
              y={y + 1}
              width={rackWidth - 4}
              height={itemHeight}
              rx={2}
              fill={item.color ?? DEFAULT_ITEM_COLOR}
              strokeWidth={0.5}
              className="stroke-black/20"
            />
            <text
              x={LABEL_WIDTH + rackWidth / 2}
              y={y + itemHeight / 2 + 4}
              textAnchor="middle"
              fontSize={10}
              className="fill-white"
            >
              {item.label}
            </text>
          </g>
        );
      })}
    </svg>
  );
};
