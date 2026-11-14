/**
 * WP-916 (IPM-19): the address map of the subnet detail. Each cell carries
 * colour and text of its kind (free, occupied, reserved, gateway, network
 * and broadcast address); /31 has no network and broadcast address (RFC
 * 3021); the map covers IPv4 up to /22 (from /21 on and for IPv6 only the
 * list); the cells are one tab stop and follow the arrow keys.
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/ipam/AddressMap.test.tsx
 */
import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, within } from '@testing-library/react';
import type { IPAddress } from '../../api/cmdb';
import { renderWithProviders } from '../../test/utils';
import { AddressMap, mapCells, mapSupported } from './AddressMap';

const AT = '2026-01-01T00:00:00Z';

function ip(address: string, extra: Partial<IPAddress> = {}): IPAddress {
  return {
    id: `ip-${address}`,
    organization_id: 'org',
    address,
    vrf_id: 'vrf',
    type: 'static',
    role: 'primary',
    origin: 'manual',
    pinned: false,
    created_at: AT,
    updated_at: AT,
    ...extra,
  };
}

describe('AddressMap', () => {
  it('gives each cell the colour and the text of its kind', () => {
    renderWithProviders(
      <AddressMap
        cidr="192.168.1.0/29"
        gateway="192.168.1.1"
        addresses={[
          ip('192.168.1.2', { state: 'active', dns_name: 'srv' }),
          ip('192.168.1.3', { type: 'reserved' }),
        ]}
      />,
    );
    const map = screen.getByRole('group', { name: 'Adresskarte von 192.168.1.0/29' });
    const cells = within(map).getAllByRole('button');
    expect(cells).toHaveLength(8);
    const kinds = cells.map((cell) => cell.getAttribute('data-kind'));
    expect(kinds).toEqual([
      'network',
      'gateway',
      'used',
      'reserved',
      'free',
      'free',
      'free',
      'broadcast',
    ]);
    // The text of the kind, not the colour alone.
    expect(cells[0]).toHaveAccessibleName('192.168.1.0 – Netzadresse');
    expect(cells[1]).toHaveAccessibleName('192.168.1.1 – Gateway');
    expect(cells[2]).toHaveAccessibleName('192.168.1.2 – Belegt – Aktiv – srv');
    expect(cells[3]).toHaveAccessibleName('192.168.1.3 – Reserviert');
    expect(cells[4]).toHaveAccessibleName('192.168.1.4 – Frei');
    expect(cells[7]).toHaveAccessibleName('192.168.1.7 – Broadcastadresse');
    // The legend names every kind in text.
    const legend = screen.getByRole('list', { name: 'Legende der Adresskarte' });
    for (const text of ['Frei', 'Belegt', 'Reserviert', 'Gateway', 'Netzadresse']) {
      expect(within(legend).getByText(text)).toBeInTheDocument();
    }
  });

  it('has no network and broadcast address in a /31 and one host in a /32', () => {
    expect(mapCells('10.0.0.0/31', undefined, []).map((c) => [c.ip, c.kind])).toEqual([
      ['10.0.0.0', 'free'],
      ['10.0.0.1', 'free'],
    ]);
    expect(mapCells('10.0.0.7/32', undefined, []).map((c) => [c.ip, c.kind])).toEqual([
      ['10.0.0.7', 'free'],
    ]);
    // A /30 keeps them.
    expect(mapCells('10.0.0.0/30', undefined, []).map((c) => c.kind)).toEqual([
      'network',
      'free',
      'free',
      'broadcast',
    ]);
  });

  it('covers IPv4 up to /22; from /21 on and IPv6 only the list', () => {
    expect(mapSupported('10.0.0.0/22')).toBe(true);
    expect(mapCells('10.0.0.0/22', undefined, [])).toHaveLength(1024);
    expect(mapSupported('10.0.0.0/21')).toBe(false);
    expect(mapCells('10.0.0.0/21', undefined, [])).toHaveLength(0);
    expect(mapSupported('2001:db8::/120')).toBe(false);
    expect(mapCells('2001:db8::/120', undefined, [])).toHaveLength(0);
    const { container } = renderWithProviders(<AddressMap cidr="2001:db8::/126" addresses={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('is one tab stop that follows the arrow keys, Home and End', () => {
    const onSelect = vi.fn();
    renderWithProviders(<AddressMap cidr="10.0.0.0/24" addresses={[]} onSelect={onSelect} />);
    const cells = within(screen.getByRole('group')).getAllByRole('button');
    expect(cells.filter((cell) => cell.tabIndex === 0)).toHaveLength(1);
    expect(cells[0]).toHaveAttribute('tabindex', '0');
    cells[0]?.focus();
    fireEvent.keyDown(cells[0] as HTMLElement, { key: 'ArrowRight' });
    expect(cells[1]).toHaveFocus();
    fireEvent.keyDown(cells[1] as HTMLElement, { key: 'ArrowDown' });
    expect(cells[17]).toHaveFocus();
    expect(cells[17]).toHaveAttribute('tabindex', '0');
    expect(cells[0]).toHaveAttribute('tabindex', '-1');
    fireEvent.keyDown(cells[17] as HTMLElement, { key: 'ArrowUp' });
    expect(cells[1]).toHaveFocus();
    fireEvent.keyDown(cells[1] as HTMLElement, { key: 'End' });
    expect(cells[255]).toHaveFocus();
    fireEvent.keyDown(cells[255] as HTMLElement, { key: 'Home' });
    expect(cells[0]).toHaveFocus();
    // A button: Enter and Space choose the cell like a click.
    fireEvent.click(cells[5] as HTMLElement);
    expect(onSelect).toHaveBeenCalledWith(
      expect.objectContaining({ ip: '10.0.0.5', kind: 'free' }),
    );
  });
});
