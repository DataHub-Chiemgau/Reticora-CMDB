/**
 * AddressMap is the address map of the subnet detail (IPM-19, WP-916): for
 * an IPv4 subnet up to /22 one cell per address with colour and text per
 * kind (free, occupied, reserved, gateway, network and broadcast address).
 * /31 has two usable hosts and /32 one, without network and broadcast
 * address (RFC 3021). A larger subnet and IPv6 show only the list; the page
 * says so (mapSupported).
 *
 * Keyboard: the cells are one tab stop (roving tabindex); the arrow keys
 * move by one cell or one row, Home and End jump to the first and last
 * cell, Enter or Space selects the cell (onSelect).
 */
import { useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { IPAddress } from '../../api/cmdb';
import { cn } from '../ui/utils';

/** MAP_MIN_PREFIX: the largest subnet the map shows is a /22 (1024 cells). */
export const MAP_MIN_PREFIX = 22;
/** The cells per row of the map. */
export const MAP_COLUMNS = 16;

/** CELL_KINDS are the kinds of a cell, labels ipam.map.kinds.*. */
export const CELL_KINDS = ['free', 'used', 'reserved', 'gateway', 'network', 'broadcast'] as const;
export type CellKind = (typeof CELL_KINDS)[number];

export interface MapCell {
  ip: string;
  kind: CellKind;
  /** The stored address of the cell, when one exists. */
  address?: IPAddress;
}

const KIND_CLASSES: Record<CellKind, string> = {
  free: 'border-line-strong bg-panel text-fg',
  used: 'border-accent bg-accent text-accent-fg',
  reserved: 'border-amber-600 bg-amber-200 text-amber-950 dark:bg-amber-700 dark:text-amber-50',
  gateway: 'border-violet-700 bg-violet-700 text-white',
  network: 'border-line bg-fg/[0.12] text-fg line-through',
  broadcast: 'border-line bg-fg/[0.12] text-fg line-through',
};

/** parseIPv4 returns the address as an unsigned 32-bit number, or undefined. */
export function parseIPv4(text: string): number | undefined {
  const parts = text.split('.');
  if (parts.length !== 4) return undefined;
  let value = 0;
  for (const part of parts) {
    if (!/^\d{1,3}$/.test(part)) return undefined;
    const n = Number(part);
    if (n > 255) return undefined;
    value = value * 256 + n;
  }
  return value;
}

export function formatIPv4(value: number): string {
  return [24, 16, 8, 0].map((shift) => Math.floor(value / 2 ** shift) % 256).join('.');
}

/** parseIPv4CIDR returns network and prefix of an IPv4 CIDR, or undefined (also for IPv6). */
export function parseIPv4CIDR(cidr: string): { network: number; prefix: number } | undefined {
  const [addr, len] = cidr.split('/');
  const ip = parseIPv4(addr ?? '');
  const prefix = Number(len);
  if (ip === undefined || !Number.isInteger(prefix) || prefix < 0 || prefix > 32) return undefined;
  const size = 2 ** (32 - prefix);
  return { network: ip - (ip % size), prefix };
}

/** mapSupported: the map is shown for IPv4 subnets from /22 to /32. */
export function mapSupported(cidr: string): boolean {
  const parsed = parseIPv4CIDR(cidr);
  return parsed !== undefined && parsed.prefix >= MAP_MIN_PREFIX;
}

/** mapCells computes the cells of an IPv4 subnet up to /22. */
export function mapCells(cidr: string, gateway: string | undefined, addresses: IPAddress[]) {
  const parsed = parseIPv4CIDR(cidr);
  if (!parsed || parsed.prefix < MAP_MIN_PREFIX) return [];
  const size = 2 ** (32 - parsed.prefix);
  const byIP = new Map(addresses.map((a) => [a.address.split('/')[0] ?? a.address, a]));
  const cells: MapCell[] = [];
  for (let i = 0; i < size; i++) {
    const ip = formatIPv4(parsed.network + i);
    const address = byIP.get(ip);
    let kind: CellKind = 'free';
    // RFC 3021: /31 and /32 have no network and broadcast address.
    if (size > 2 && i === 0) kind = 'network';
    else if (size > 2 && i === size - 1) kind = 'broadcast';
    else if (address?.is_gateway || ip === gateway) kind = 'gateway';
    else if (address?.type === 'reserved') kind = 'reserved';
    else if (address) kind = 'used';
    cells.push({ ip, kind, address });
  }
  return cells;
}

export interface AddressMapProps {
  cidr: string;
  gateway?: string;
  addresses: IPAddress[];
  /** Called with the cell chosen by click, Enter or Space. */
  onSelect?: (cell: MapCell) => void;
  /** The ip of the chosen cell, marked. */
  selected?: string;
}

export function AddressMap({ cidr, gateway, addresses, onSelect, selected }: AddressMapProps) {
  const { t } = useTranslation();
  const cells = useMemo(() => mapCells(cidr, gateway, addresses), [cidr, gateway, addresses]);
  const [focus, setFocus] = useState(0);
  const refs = useRef<(HTMLButtonElement | null)[]>([]);

  const move = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const last = cells.length - 1;
    const target: Record<string, number> = {
      ArrowRight: Math.min(last, index + 1),
      ArrowLeft: Math.max(0, index - 1),
      ArrowDown: Math.min(last, index + MAP_COLUMNS),
      ArrowUp: Math.max(0, index - MAP_COLUMNS),
      Home: 0,
      End: last,
    };
    const next = target[event.key];
    if (next === undefined) return;
    event.preventDefault();
    setFocus(next);
    refs.current[next]?.focus();
  };

  const label = (cell: MapCell) => {
    const kind = t(`ipam.map.kinds.${cell.kind}`);
    const state = cell.address?.state ? t(`ipam.states.${cell.address.state}`) : undefined;
    const name = cell.address?.dns_name;
    return [cell.ip, kind, state, name].filter(Boolean).join(' – ');
  };

  if (cells.length === 0) return null;
  return (
    <div className="space-y-3">
      <ul className="flex flex-wrap gap-3 text-xs text-fg" aria-label={t('ipam.map.legend')}>
        {CELL_KINDS.map((kind) => (
          <li key={kind} className="flex items-center gap-1.5">
            <span
              className={cn('inline-block h-3 w-3 rounded-sm border', KIND_CLASSES[kind])}
              aria-hidden
            />
            {t(`ipam.map.kinds.${kind}`)}
          </li>
        ))}
      </ul>
      <div
        role="group"
        aria-label={t('ipam.map.label', { cidr })}
        className="grid gap-1"
        style={{ gridTemplateColumns: `repeat(${MAP_COLUMNS}, minmax(0, 1fr))` }}
      >
        {cells.map((cell, index) => (
          <button
            key={cell.ip}
            ref={(node) => {
              refs.current[index] = node;
            }}
            type="button"
            data-kind={cell.kind}
            tabIndex={index === focus ? 0 : -1}
            aria-label={label(cell)}
            aria-pressed={selected === cell.ip}
            title={label(cell)}
            onFocus={() => setFocus(index)}
            onKeyDown={(event) => move(event, index)}
            onClick={() => onSelect?.(cell)}
            className={cn(
              'h-7 min-w-0 rounded border text-[10px] tabular-nums focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent',
              KIND_CLASSES[cell.kind],
              selected === cell.ip && 'ring-2 ring-fg',
            )}
          >
            {cell.ip.split('.')[3]}
          </button>
        ))}
      </div>
    </div>
  );
}
