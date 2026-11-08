// INV-03 and UI-14 (WP-813): the label dialog. The label of one asset is a
// PDF loaded with the bearer token (fetchBlob) and opened as object URL; a
// batch of the inventory area runs as job labels.batch, its progress comes
// from GET /jobs?kind=labels.batch and its PDF from the signed download_url
// of GET /labels/batch/{job_id}/download, loaded without token and opened
// as application/pdf object URL (the object store answers it only as
// attachment, WP-829); without it the dialog offers the signed URL as
// download link. 409 and 503 show the detail.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import i18n from '../../i18n';
import { renderWithProviders } from '../../test/utils';
import { useAuthStore } from '../../auth/authStore';
import { LabelPrintDialog } from './LabelPrintDialog';

const ASSET = 'a5000000-0000-4000-8000-000000000001';
const AREA = 'aa000000-0000-4000-8000-000000000001';
const JOB = 'f0000000-0000-4000-8000-000000000001';
const TOKEN = 'header.payload.signature';

interface Call {
  method: string;
  path: string;
  search: string;
  body: unknown;
  authorization: string | null;
  credentials?: RequestCredentials;
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const problem = (status: number, detail: string) =>
  json({ type: 'about:blank', title: 'Error', status, detail }, status);

interface Stub {
  label?: () => Response;
  batch?: () => Response;
  download?: () => Response;
  /** pdf answers the signed URL of the batch PDF (the object store). */
  pdf?: () => Response;
  jobStatus?: string;
}

const SIGNED = 'https://cmdb.example/objects/exports/labels.pdf?sig=1';

function stubApi({ label, batch, download, pdf, jobStatus = 'succeeded' }: Stub = {}) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined;
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const headers = new Headers(init?.headers);
      calls.push({
        method,
        path,
        search: url.search,
        body,
        authorization: headers.get('Authorization'),
        credentials: init?.credentials,
      });
      if (url.origin === 'https://cmdb.example' && path === '/objects/exports/labels.pdf') {
        // The object store answers the signed URL as attachment, here
        // without a PDF type: the dialog sets application/pdf itself.
        return pdf
          ? pdf()
          : new Response('%PDF-1.7 batch', {
              status: 200,
              headers: {
                'Content-Type': 'application/octet-stream',
                'Content-Disposition': 'attachment; filename="labels.pdf"',
              },
            });
      }
      if (path === `/assets/${ASSET}/label`) {
        return label
          ? label()
          : new Response(new Blob(['%PDF-1.7'], { type: 'application/pdf' }), {
              status: 200,
              headers: { 'Content-Type': 'application/pdf' },
            });
      }
      if (path === '/labels/batch' && method === 'POST') {
        return batch ? batch() : json({ job_id: JOB, status: 'queued', kind: 'labels.batch' }, 202);
      }
      if (path === '/jobs') {
        return json({
          data: [
            {
              id: JOB,
              kind: 'labels.batch',
              status: jobStatus,
              progress: jobStatus === 'succeeded' ? 100 : 40,
              attempts: 1,
              max_attempts: 3,
              created_at: '2026-11-01T10:00:00Z',
            },
          ],
          total: 1,
          limit: 100,
          offset: 0,
          has_more: false,
        });
      }
      if (path === `/jobs/${JOB}/result`) {
        return json({ labels: 48, pages: 2, expires_at: '2026-11-08T10:00:00Z' });
      }
      if (path === `/labels/batch/${JOB}/download`) {
        return download
          ? download()
          : json({
              job_id: JOB,
              labels: 48,
              pages: 2,
              expires_at: '2026-11-08T10:00:00Z',
              download_url: SIGNED,
            });
      }
      if (path === '/entitlements') {
        return json({
          data: [{ feature_key: 'barcode', enabled: true }],
          total: 1,
          limit: 50,
          offset: 0,
          has_more: false,
        });
      }
      return json({ data: [], total: 0, limit: 50, offset: 0, has_more: false });
    }),
  );
  return calls;
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
  useAuthStore.setState({
    token: TOKEN,
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: 'user-1' },
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

describe('LabelPrintDialog', () => {
  it('loads the PDF label of an asset with the bearer token and opens it as object URL', async () => {
    const calls = stubApi();
    const createObjectURL = vi.fn(() => 'blob:label');
    Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, configurable: true });
    Object.defineProperty(URL, 'revokeObjectURL', { value: vi.fn(), configurable: true });
    const tab = { opener: {} as unknown };
    const open = vi.spyOn(window, 'open').mockReturnValue(tab as Window);
    renderWithProviders(
      <LabelPrintDialog open onOpenChange={vi.fn()} asset={{ id: ASSET, name: 'Laptop 1' }} />,
    );
    const dialog = await screen.findByRole('dialog', { name: 'Etiketten drucken' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'PDF öffnen' }));
    await waitFor(() => expect(open).toHaveBeenCalledWith('blob:label', '_blank'));
    const call = calls.find((entry) => entry.path === `/assets/${ASSET}/label`)!;
    expect(Object.fromEntries(new URLSearchParams(call.search))).toEqual({
      format: 'pdf',
      template: 'asset_50x25',
    });
    expect(call.authorization).toBe(`Bearer ${TOKEN}`);
    // The blob of the response (its realm is that of fetch, not of jsdom).
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    expect((createObjectURL.mock.calls[0] as unknown[])[0]).toMatchObject({
      type: 'application/pdf',
    });
    expect(tab.opener).toBeNull();
  });

  it('starts a batch of the area, shows the job progress and opens the PDF as object URL', async () => {
    const calls = stubApi();
    const createObjectURL = vi.fn(() => 'blob:batch');
    const revokeObjectURL = vi.fn();
    Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, configurable: true });
    Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, configurable: true });
    const tab = { opener: {} as unknown };
    const open = vi.spyOn(window, 'open').mockReturnValue(tab as Window);
    renderWithProviders(
      <LabelPrintDialog open onOpenChange={vi.fn()} areaId={AREA} areaName="Werk Nord" />,
    );
    const dialog = await screen.findByRole('dialog', { name: 'Etiketten drucken' });
    fireEvent.change(within(dialog).getByLabelText('Vorlage'), { target: { value: 'bin_100x50' } });
    fireEvent.change(within(dialog).getByLabelText('Objektart'), { target: { value: 'location' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Etiketten erzeugen' }));
    await waitFor(() =>
      expect(calls.find((entry) => entry.path === '/labels/batch')?.body).toEqual({
        template: 'bin_100x50',
        kind: 'location',
        location_id: AREA,
      }),
    );
    expect(await within(dialog).findByText('Status: Erfolgreich (100 %)')).toBeInTheDocument();
    expect(await within(dialog).findByText(/48 Etiketten auf 2 Seiten/)).toBeInTheDocument();
    const jobs = calls.find((entry) => entry.path === '/jobs')!;
    expect(new URLSearchParams(jobs.search).get('kind')).toBe('labels.batch');

    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      fireEvent.click(within(dialog).getByRole('button', { name: 'PDF herunterladen' }));
      await waitFor(() => expect(open).toHaveBeenCalledWith('blob:batch', '_blank'));
      // The signed URL is loaded without bearer token and cookie.
      const signed = calls.find((entry) => entry.path === '/objects/exports/labels.pdf')!;
      expect(signed.search).toBe('?sig=1');
      expect(signed.authorization).toBeNull();
      expect(signed.credentials).toBe('omit');
      // The blob is a PDF whatever type the object store named.
      const blob = (createObjectURL.mock.calls[0] as unknown[])[0] as Blob;
      expect(blob.type).toBe('application/pdf');
      expect(blob.size).toBe('%PDF-1.7 batch'.length);
      expect(tab.opener).toBeNull();
      expect(within(dialog).queryByRole('alert')).not.toBeInTheDocument();
      // The object URL is revoked after a delay, not at once.
      expect(revokeObjectURL).not.toHaveBeenCalled();
      vi.advanceTimersByTime(60_000);
      expect(revokeObjectURL).toHaveBeenCalledWith('blob:batch');
    } finally {
      vi.useRealTimers();
    }
  });

  it('offers the download link when the signed PDF cannot be loaded', async () => {
    stubApi({ pdf: () => new Response('denied', { status: 403 }) });
    Object.defineProperty(URL, 'createObjectURL', { value: vi.fn(), configurable: true });
    const open = vi.spyOn(window, 'open').mockReturnValue({ opener: null } as Window);
    renderWithProviders(<LabelPrintDialog open onOpenChange={vi.fn()} areaId={AREA} />);
    const dialog = await screen.findByRole('dialog', { name: 'Etiketten drucken' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Etiketten erzeugen' }));
    fireEvent.click(await within(dialog).findByRole('button', { name: 'PDF herunterladen' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'Das PDF konnte nicht geladen werden (HTTP 403)',
    );
    expect(
      within(dialog).getByRole('link', { name: 'PDF über den Download-Link speichern' }),
    ).toHaveAttribute('href', SIGNED);
    expect(open).not.toHaveBeenCalled();
  });

  it('offers the download link when the pop-up is blocked', async () => {
    stubApi();
    Object.defineProperty(URL, 'createObjectURL', {
      value: vi.fn(() => 'blob:blocked'),
      configurable: true,
    });
    const revokeObjectURL = vi.fn();
    Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, configurable: true });
    vi.spyOn(window, 'open').mockReturnValue(null);
    renderWithProviders(<LabelPrintDialog open onOpenChange={vi.fn()} areaId={AREA} />);
    const dialog = await screen.findByRole('dialog', { name: 'Etiketten drucken' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Etiketten erzeugen' }));
    fireEvent.click(await within(dialog).findByRole('button', { name: 'PDF herunterladen' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Pop-up blockiert');
    expect(
      within(dialog).getByRole('link', { name: 'PDF über den Download-Link speichern' }),
    ).toHaveAttribute('href', SIGNED);
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:blocked');
  });

  it('shows the detail of a batch whose result is not ready (409)', async () => {
    stubApi({ download: () => problem(409, 'label batch not finished') });
    vi.spyOn(window, 'open').mockReturnValue(null);
    renderWithProviders(<LabelPrintDialog open onOpenChange={vi.fn()} areaId={AREA} />);
    const dialog = await screen.findByRole('dialog', { name: 'Etiketten drucken' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Etiketten erzeugen' }));
    fireEvent.click(await within(dialog).findByRole('button', { name: 'PDF herunterladen' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('label batch not finished');
  });

  it('shows the detail without blob storage (503)', async () => {
    stubApi({ batch: () => problem(503, 'blob storage not configured') });
    renderWithProviders(<LabelPrintDialog open onOpenChange={vi.fn()} areaId={AREA} />);
    const dialog = await screen.findByRole('dialog', { name: 'Etiketten drucken' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Etiketten erzeugen' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'blob storage not configured',
    );
  });

  it('shows the detail of a refused asset label', async () => {
    stubApi({ label: () => problem(409, 'label not available') });
    renderWithProviders(
      <LabelPrintDialog open onOpenChange={vi.fn()} asset={{ id: ASSET, name: 'Laptop 1' }} />,
    );
    const dialog = await screen.findByRole('dialog', { name: 'Etiketten drucken' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'PDF öffnen' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('label not available');
  });
});
