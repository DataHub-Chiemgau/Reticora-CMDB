import { describe, expect, it } from 'vitest';

// The room floor-plan positions CIs by normalized coordinates persisted on
// the room (spec §8.5). These tests pin the coordinate math used on drop.

describe('room plan coordinate math', () => {
  function dropPosition(
    clientX: number,
    clientY: number,
    rect: { left: number; top: number; width: number; height: number },
  ) {
    const x = Math.min(1, Math.max(0, (clientX - rect.left) / rect.width));
    const y = Math.min(1, Math.max(0, (clientY - rect.top) / rect.height));
    return { x, y };
  }

  it('maps the center to 0.5/0.5', () => {
    const pos = dropPosition(50, 50, { left: 0, top: 0, width: 100, height: 100 });
    expect(pos).toEqual({ x: 0.5, y: 0.5 });
  });

  it('clamps drops outside the plan to the nearest edge', () => {
    expect(dropPosition(-10, 50, { left: 0, top: 0, width: 100, height: 100 }).x).toBe(0);
    expect(dropPosition(150, 50, { left: 0, top: 0, width: 100, height: 100 }).x).toBe(1);
    expect(dropPosition(50, -10, { left: 0, top: 0, width: 100, height: 100 }).y).toBe(0);
    expect(dropPosition(50, 150, { left: 0, top: 0, width: 100, height: 100 }).y).toBe(1);
  });

  it('offsets relative to the plan origin', () => {
    const pos = dropPosition(60, 40, { left: 10, top: 10, width: 100, height: 60 });
    expect(pos.x).toBeCloseTo(0.5);
    expect(pos.y).toBeCloseTo(0.5);
  });
});
