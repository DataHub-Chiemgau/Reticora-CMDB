/**
 * Tests for the notification center (UI-16): unread count, read and unread
 * entries, marking one or all as read.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { NotificationCenter } from './NotificationCenter';

const unread = {
  id: 'n-1',
  category: 'review',
  subject: 'Neues Review-Item',
  body: 'sw-core-01 ist mehrdeutig',
  created_at: '2026-10-14T08:00:00Z',
};
const read = {
  id: 'n-2',
  category: 'job',
  subject: 'Export fertig',
  body: '',
  created_at: '2026-10-13T08:00:00Z',
  read_at: '2026-10-13T09:00:00Z',
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function page(data: unknown[]) {
  return { data, total: data.length, limit: 50, offset: 0, has_more: false };
}

function stubServer() {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (init?.method === 'POST' && url.endsWith('/read-all')) return json({ marked: 1 });
    if (init?.method === 'POST') return new Response(null, { status: 204 });
    if (url.includes('unread=true')) return json(page([unread]));
    return json(page([unread, read]));
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('NotificationCenter', () => {
  it('shows the unread count and marks one notification as read', async () => {
    const fetchMock = stubServer();
    renderWithProviders(<NotificationCenter />);

    const bell = await screen.findByRole('button', { name: /1 ungelesen/ });
    fireEvent.click(bell);

    const panel = await screen.findByRole('dialog', { name: 'Benachrichtigungen' });
    await within(panel).findByText('Export fertig');
    const items = within(panel).getAllByRole('listitem');
    expect(items[0]).toHaveAttribute('data-unread', 'true');
    expect(items[1]).not.toHaveAttribute('data-unread');
    // Only the unread one offers "mark as read".
    expect(within(panel).getAllByRole('button', { name: 'Als gelesen markieren' })).toHaveLength(1);

    fireEvent.click(within(panel).getByRole('button', { name: 'Als gelesen markieren' }));
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(
          (call) =>
            String(call[0]) === '/api/v1/notifications/n-1/read' && call[1]?.method === 'POST',
        ),
      ).toBe(true),
    );
  });

  it('marks all notifications as read', async () => {
    const fetchMock = stubServer();
    renderWithProviders(<NotificationCenter />);

    fireEvent.click(await screen.findByRole('button', { name: /1 ungelesen/ }));
    const panel = await screen.findByRole('dialog', { name: 'Benachrichtigungen' });
    const markAll = within(panel).getByRole('button', { name: 'Alle gelesen' });
    await waitFor(() => expect(markAll).not.toBeDisabled());
    fireEvent.click(markAll);

    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(
          (call) =>
            String(call[0]) === '/api/v1/notifications/read-all' && call[1]?.method === 'POST',
        ),
      ).toBe(true),
    );
  });
});
