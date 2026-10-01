import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { RoomPlanPage, dropPosition, keyboardMove } from './RoomPlanPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

// The room floor-plan positions CIs by normalized coordinates persisted on
// the room (spec §8.5). Besides drag & drop, every object can be positioned
// with the keyboard and through a form (WCAG 2.1 AA, UI-18/NFR-07, WP-193).

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('room plan coordinate math', () => {
  const rect = { left: 0, top: 0, width: 100, height: 100 };

  it('maps the center to 0.5/0.5', () => {
    expect(dropPosition(50, 50, rect)).toEqual({ x: 0.5, y: 0.5 });
  });

  it('clamps drops outside the plan to the nearest edge', () => {
    expect(dropPosition(-10, 50, rect).x).toBe(0);
    expect(dropPosition(150, 50, rect).x).toBe(1);
    expect(dropPosition(50, -10, rect).y).toBe(0);
    expect(dropPosition(50, 150, rect).y).toBe(1);
  });

  it('offsets relative to the plan origin', () => {
    const pos = dropPosition(60, 40, { left: 10, top: 10, width: 100, height: 60 });
    expect(pos.x).toBeCloseTo(0.5);
    expect(pos.y).toBeCloseTo(0.5);
  });

  it('moves by 1 % per arrow key, 10 % with Shift, clamped to the plan', () => {
    const start = { x: 0.5, y: 0.5 };
    expect(keyboardMove(start, 'ArrowRight', false)).toEqual({ x: 0.51, y: 0.5 });
    expect(keyboardMove(start, 'ArrowLeft', true)).toEqual({ x: 0.4, y: 0.5 });
    expect(keyboardMove(start, 'ArrowUp', false)).toEqual({ x: 0.5, y: 0.49 });
    expect(keyboardMove(start, 'ArrowDown', true)).toEqual({ x: 0.5, y: 0.6 });
    expect(keyboardMove({ x: 0.97, y: 0 }, 'ArrowRight', true)).toEqual({ x: 1, y: 0 });
    expect(keyboardMove({ x: 0.2, y: 0.02 }, 'ArrowUp', true)).toEqual({ x: 0.2, y: 0 });
    expect(keyboardMove(start, 'a', false)).toBeUndefined();
  });
});

const room = { id: 'room-1', name: 'Serverraum', layout: { 'ci-1': { x: 0.5, y: 0.5 } } };

function renderPlan() {
  const fetchMock = stubFetchRoutes({
    '/buildings': {
      data: [{ id: 'b-1', name: 'Haus A' }],
      total: 1,
      limit: 100,
      offset: 0,
      has_more: false,
    },
    '/rooms/room-1': room,
    '/rooms': { data: [room], total: 1, limit: 100, offset: 0, has_more: false },
    '/cis': {
      data: [{ id: 'ci-1', name: 'Switch 1', status: 'active' }],
      total: 1,
      limit: 200,
      offset: 0,
      has_more: false,
    },
  });
  renderWithProviders(<RoomPlanPage />);
  return fetchMock;
}

async function openRoom() {
  const building = await screen.findByRole('combobox', { name: /Gebäude|Building/ });
  await screen.findByRole('option', { name: 'Haus A' });
  fireEvent.change(building, { target: { value: 'b-1' } });
  const roomSelect = screen.getByRole('combobox', { name: /^(Raum|Room)$/ });
  await screen.findByRole('option', { name: 'Serverraum' });
  fireEvent.change(roomSelect, { target: { value: 'room-1' } });
  return screen.findByRole('button', { name: /Switch 1/ });
}

function lastLayoutUpdate(fetchMock: ReturnType<typeof vi.fn>) {
  const patch = fetchMock.mock.calls.filter(
    ([, init]) => (init as RequestInit | undefined)?.method === 'PATCH',
  );
  const body = (patch[patch.length - 1]?.[1] as RequestInit | undefined)?.body;
  return body
    ? (JSON.parse(String(body)) as { layout: Record<string, unknown> }).layout
    : undefined;
}

describe('RoomPlanPage without drag and drop', () => {
  it('moves a focused object with the arrow keys', async () => {
    const fetchMock = renderPlan();
    const marker = await openRoom();
    expect(marker).toHaveAttribute('tabindex', '0');
    expect(marker).toHaveAccessibleName(/50 %.*50 %/);
    marker.focus();
    fireEvent.keyDown(marker, { key: 'ArrowRight', shiftKey: true });
    await waitFor(() =>
      expect(lastLayoutUpdate(fetchMock)).toEqual({ 'ci-1': { x: 0.6, y: 0.5 } }),
    );
  });

  it('positions an object through the coordinate form', async () => {
    const fetchMock = renderPlan();
    await openRoom();
    const form = screen.getByRole('form', { name: /Switch 1/ });
    fireEvent.change(within(form).getByRole('spinbutton', { name: /X/ }), {
      target: { value: '25' },
    });
    fireEvent.change(within(form).getByRole('spinbutton', { name: /Y/ }), {
      target: { value: '75' },
    });
    fireEvent.submit(form);
    await waitFor(() =>
      expect(lastLayoutUpdate(fetchMock)).toEqual({ 'ci-1': { x: 0.25, y: 0.75 } }),
    );
  });
});
