/**
 * UI-18: the rack drawing follows the theme. The frame, the unit lines and
 * the unit numbers (outside the frame, on the panel background) use fill-/
 * stroke- classes with design tokens (they switch with the theme) instead of colours chosen for the dark
 * theme (#9ca3af numbers reached only 2.5:1 on the white card). Items keep
 * their data colour.
 */
import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { RackSVG } from './RackSVG';

// The colours the drawing hard-coded for the dark theme before.
const DARK_HEX = ['#1f2937', '#374151', '#9ca3af'];

function renderRack() {
  return render(
    <RackSVG
      units={3}
      items={[
        { position: 1, height: 1, label: 'pdu-01', id: 'a' },
        { position: 2, height: 1, label: 'sw-01', id: 'b', color: '#16a34a' },
      ]}
    />,
  );
}

describe('RackSVG theme', () => {
  it('draws frame, unit lines and unit numbers with theme classes', () => {
    const { container } = renderRack();
    const svg = container.querySelector('svg')!;

    const frame = svg.querySelector(':scope > rect')!;
    expect(frame).not.toHaveAttribute('fill');
    expect(frame).not.toHaveAttribute('stroke');
    expect(frame.getAttribute('class')).toBe('fill-canvas stroke-line-strong');

    const lines = [...svg.querySelectorAll('line')];
    expect(lines).toHaveLength(3);
    for (const line of lines) {
      expect(line).not.toHaveAttribute('stroke');
      expect(line.getAttribute('class')).toBe('stroke-line');
    }

    const numbers = lines.map((line) => line.nextElementSibling!);
    expect(numbers.map((n) => n.textContent)).toEqual(['3', '2', '1']);
    for (const number of numbers) {
      expect(number).not.toHaveAttribute('fill');
      expect(number.getAttribute('class')).toBe('fill-fg-muted');
    }
  });

  it('keeps no colour chosen for the dark theme', () => {
    const { container } = renderRack();
    for (const element of container.querySelectorAll('svg *')) {
      for (const attribute of ['fill', 'stroke']) {
        const value = element.getAttribute(attribute)?.toLowerCase();
        expect(DARK_HEX, `${element.tagName} ${attribute}`).not.toContain(value);
      }
    }
  });

  it('keeps the item colours', () => {
    const { container } = renderRack();
    const items = [...container.querySelectorAll('svg g > rect')];
    expect(items.map((rect) => rect.getAttribute('fill'))).toEqual(['#087680', '#16a34a']);
    const labels = [...container.querySelectorAll('svg g > rect + text')];
    expect(labels.map((label) => [label.textContent, label.getAttribute('class')])).toEqual([
      ['pdu-01', 'fill-white'],
      ['sw-01', 'fill-white'],
    ]);
  });
});
