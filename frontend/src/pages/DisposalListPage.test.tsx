import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { DisposalListPage } from './DisposalListPage';
import { renderWithProviders } from '../test/utils';
import i18n from '../i18n';

// WP-815 (UI-14, E-55): the disposal form follows the required fields per
// method of the handler (WP-751), the evidence is uploaded with FileUpload
// before submitting and goes as evidence_document_id; records are
// append-only.

const ASSET_ID = 'a0000000-0000-4000-8000-000000000001';
const SUPPLIER_ID = '50000000-0000-4000-8000-000000000001';
const EVIDENCE_ID = 'd0000000-0000-4000-8000-000000000001';

const RECORD = {
  id: 'e4000000-0000-4000-8000-000000000001',
  organization_id: 'org-1',
  asset_id: ASSET_ID,
  method: 'destruction',
  protection_class: 2,
  security_level: 'H-5',
  data_carrier: 'HDD SN-77',
  supplier_id: SUPPLIER_ID,
  evidence_document_id: EVIDENCE_ID,
  certificate_ref: 'CERT-2026-77',
  performed_by: 'Erika Muster',
  witness: 'Max Zeuge',
  performed_at: '2026-10-01T10:00:00Z',
  notes: 'Vor Ort',
  record_hash: 'ab'.repeat(32),
  created_at: '2026-10-01T10:00:00Z',
};

/** MockXHR answers the content transfer of FileUpload. */
class MockXHR {
  static requests: MockXHR[] = [];
  url = '';
  status = 0;
  statusText = '';
  responseText = '';
  upload = { onprogress: null };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  open(_method: string, url: string) {
    this.url = url;
  }
  setRequestHeader() {}
  send() {
    MockXHR.requests.push(this);
  }
  abort() {}
  respond(status: number, body: unknown) {
    this.status = status;
    this.responseText = JSON.stringify(body);
    this.onload?.();
  }
}

interface Call {
  method: string;
  path: string;
  body?: Record<string, unknown>;
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

function page<T>(data: T[]) {
  return { data, total: data.length, limit: 100, offset: 0, has_more: false };
}

function evidence(scan: string) {
  return {
    id: EVIDENCE_ID,
    organization_id: 'org-1',
    title: 'vernichtung',
    file_name: 'vernichtung.pdf',
    file_size: 10,
    mime_type: 'application/pdf',
    version: 1,
    category: 'certificate',
    tags: [],
    uploaded_by: 'user-1',
    created_at: '2026-10-01T10:00:00Z',
    updated_at: '2026-10-01T10:00:00Z',
    scan_status: scan,
  };
}

/** stubApi answers the disposal page and records every request. */
function stubApi({
  permissions = ['disposal:read', 'disposal:write'],
  records = [] as unknown[],
  scan = 'clean',
  create = (body: Record<string, unknown>) => json({ ...RECORD, ...body }, 201),
}: {
  permissions?: string[];
  records?: unknown[];
  scan?: string;
  create?: (body: Record<string, unknown>) => Response;
} = {}) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const path = url.pathname.replace(/^\/api\/v1/, '');
      const method = init?.method ?? 'GET';
      const body =
        typeof init?.body === 'string'
          ? (JSON.parse(init.body) as Record<string, unknown>)
          : undefined;
      calls.push({ method, path, body });
      if (method === 'POST' && path === '/disposal-records') return create(body ?? {});
      if (method === 'POST' && path === '/documents') {
        return json({ ...evidence('pending_scan'), ...body, id: EVIDENCE_ID }, 201);
      }
      if (method !== 'GET') return json({ detail: `unexpected ${method} ${path}` }, 404);
      if (path === '/me/permissions') return json({ user_id: 'user-1', permissions });
      if (path === '/disposal-records') return json(page(records));
      if (path === '/suppliers') {
        return json(page([{ id: SUPPLIER_ID, name: 'Akten-Ex GmbH', client_id: null }]));
      }
      if (path === '/assets') {
        return json(
          page([{ id: ASSET_ID, name: 'Laptop 7', inventory_no: 'INV-7', custom_fields: {} }]),
        );
      }
      if (path === `/documents/${EVIDENCE_ID}`) return json(evidence(scan));
      if (path === `/documents/${EVIDENCE_ID}/content`) {
        return json({ download_url: 'https://files.test/evidence.pdf' });
      }
      return json({ detail: `unexpected ${path}` }, 404);
    }),
  );
  return calls;
}

beforeEach(async () => {
  MockXHR.requests = [];
  vi.stubGlobal('XMLHttpRequest', MockXHR);
  await i18n.changeLanguage('de-DE');
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function openForm() {
  fireEvent.click(await screen.findByRole('button', { name: 'Entsorgung dokumentieren' }));
  return screen.findByRole('dialog');
}

/** pickAsset chooses the asset Laptop 7 in the ObjectPicker. */
async function pickAsset(dialog: HTMLElement) {
  const combobox = within(dialog).getByRole('combobox', { name: /Asset oder CI/ });
  fireEvent.change(combobox, { target: { value: 'Lap' } });
  fireEvent.click(await within(dialog).findByRole('option', { name: /Laptop 7/ }));
}

/** uploadEvidence uploads the evidence and finishes the transfer. */
async function uploadEvidence(dialog: HTMLElement) {
  fireEvent.change(within(dialog).getByLabelText('Nachweis hochladen'), {
    target: { files: [new File(['%PDF-'], 'vernichtung.pdf', { type: 'application/pdf' })] },
  });
  await waitFor(() => expect(MockXHR.requests).toHaveLength(1));
  act(() => MockXHR.requests[0]?.respond(200, evidence('pending_scan')));
  return within(dialog).findByTestId('evidence-status');
}

function submitButton(dialog: HTMLElement) {
  return within(dialog).getByRole('button', { name: 'Anlegen' });
}

describe('DisposalListPage', () => {
  it('marks the required fields of the method', async () => {
    stubApi();
    renderWithProviders(<DisposalListPage />, { route: '/disposal' });
    const dialog = await openForm();

    // destruction: protection class, security level, data carrier,
    // service provider or witness and the evidence (DIN 66399, E-55).
    for (const label of [
      'Schutzklasse (DIN 66399) *',
      'Sicherheitsstufe (DIN 66399) *',
      'Datenträger *',
      'Dienstleister *',
      'Zeuge *',
      'Durchgeführt von *',
      'Durchgeführt am *',
    ]) {
      expect(within(dialog).getByLabelText(label)).toBeInTheDocument();
    }
    expect(within(dialog).getByText('Nachweis *')).toBeInTheDocument();

    // reuse: only the fields of every method.
    fireEvent.change(within(dialog).getByLabelText('Methode *'), { target: { value: 'reuse' } });
    for (const label of [
      'Schutzklasse (DIN 66399)',
      'Sicherheitsstufe (DIN 66399)',
      'Datenträger',
      'Dienstleister',
      'Zeuge',
    ]) {
      expect(within(dialog).getByLabelText(label)).toBeInTheDocument();
    }
    expect(within(dialog).getByText('Nachweis')).toBeInTheDocument();
    expect(within(dialog).getByLabelText('Durchgeführt von *')).toBeInTheDocument();

    // secure_erasure: data carrier, certificate or notes, evidence.
    fireEvent.change(within(dialog).getByLabelText('Methode *'), {
      target: { value: 'secure_erasure' },
    });
    expect(within(dialog).getByLabelText('Datenträger *')).toBeInTheDocument();
    expect(within(dialog).getByLabelText('Zertifikats-Referenz *')).toBeInTheDocument();
    expect(within(dialog).getByLabelText('Schutzklasse (DIN 66399)')).toBeInTheDocument();
  });

  it('refuses a destruction without its required fields before the request', async () => {
    const calls = stubApi();
    renderWithProviders(<DisposalListPage />, { route: '/disposal' });
    const dialog = await openForm();
    await pickAsset(dialog);
    fireEvent.change(within(dialog).getByLabelText('Durchgeführt von *'), {
      target: { value: 'Erika Muster' },
    });
    fireEvent.click(submitButton(dialog));

    expect(await within(dialog).findAllByText('Pflichtfeld für diese Methode')).not.toHaveLength(0);
    expect(within(dialog).getByLabelText('Schutzklasse (DIN 66399) *')).toHaveAttribute(
      'aria-invalid',
      'true',
    );
    expect(calls.some((c) => c.method === 'POST' && c.path === '/disposal-records')).toBe(false);

    // reuse needs no more than asset, performer and time.
    fireEvent.change(within(dialog).getByLabelText('Methode *'), { target: { value: 'reuse' } });
    fireEvent.click(submitButton(dialog));
    await waitFor(() =>
      expect(calls.some((c) => c.method === 'POST' && c.path === '/disposal-records')).toBe(true),
    );
    const post = calls.find((c) => c.method === 'POST' && c.path === '/disposal-records')!;
    expect(post.body).toMatchObject({
      method: 'reuse',
      asset_id: ASSET_ID,
      performed_by: 'Erika Muster',
    });
    expect(post.body).not.toHaveProperty('evidence_document_id');
  });

  it('uploads the evidence and sends it as evidence_document_id', async () => {
    const calls = stubApi();
    renderWithProviders(<DisposalListPage />, { route: '/disposal' });
    const dialog = await openForm();
    await pickAsset(dialog);
    fireEvent.change(within(dialog).getByLabelText('Schutzklasse (DIN 66399) *'), {
      target: { value: '2' },
    });
    fireEvent.change(within(dialog).getByLabelText('Sicherheitsstufe (DIN 66399) *'), {
      target: { value: 'h-5' },
    });
    fireEvent.change(within(dialog).getByLabelText('Datenträger *'), {
      target: { value: 'HDD SN-77' },
    });
    await waitFor(() =>
      expect(
        within(within(dialog).getByLabelText('Dienstleister *')).getByRole('option', {
          name: 'Akten-Ex GmbH',
        }),
      ).toBeInTheDocument(),
    );
    fireEvent.change(within(dialog).getByLabelText('Dienstleister *'), {
      target: { value: SUPPLIER_ID },
    });
    fireEvent.change(within(dialog).getByLabelText('Durchgeführt von *'), {
      target: { value: 'Erika Muster' },
    });
    fireEvent.change(within(dialog).getByLabelText('Durchgeführt am *'), {
      target: { value: '2026-10-01T12:00' },
    });

    const status = await uploadEvidence(dialog);
    expect(await within(status).findByText('Sauber')).toBeInTheDocument();
    // The evidence is linked to the chosen asset (E-61).
    const upload = calls.find((c) => c.method === 'POST' && c.path === '/documents')!;
    expect(upload.body).toMatchObject({
      category: 'certificate',
      links: [{ entity_type: 'asset', entity_id: ASSET_ID }],
    });

    fireEvent.click(submitButton(dialog));
    await waitFor(() =>
      expect(calls.some((c) => c.method === 'POST' && c.path === '/disposal-records')).toBe(true),
    );
    const post = calls.find((c) => c.method === 'POST' && c.path === '/disposal-records')!;
    expect(post.body).toEqual({
      method: 'destruction',
      asset_id: ASSET_ID,
      protection_class: 2,
      security_level: 'H-5',
      data_carrier: 'HDD SN-77',
      supplier_id: SUPPLIER_ID,
      evidence_document_id: EVIDENCE_ID,
      performed_by: 'Erika Muster',
      performed_at: new Date('2026-10-01T12:00').toISOString(),
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('blocks submitting with an infected evidence', async () => {
    stubApi({ scan: 'infected' });
    renderWithProviders(<DisposalListPage />, { route: '/disposal' });
    const dialog = await openForm();
    const status = await uploadEvidence(dialog);
    expect(await within(status).findByText('Infiziert')).toBeInTheDocument();
    expect(within(status).getByText(/Absenden ist gesperrt/)).toBeInTheDocument();
    expect(submitButton(dialog)).toBeDisabled();
  });

  it('shows a 400 with a field name at the field', async () => {
    stubApi({
      create: () =>
        json(
          {
            type: 'urn:reticora:problem:invalid-field',
            title: 'Bad Request',
            status: 400,
            detail: 'security_level: must match ^[POTEFH]-[1-7]$ (DIN 66399, e.g. P-4)',
            field: 'security_level',
          },
          400,
        ),
    });
    renderWithProviders(<DisposalListPage />, { route: '/disposal' });
    const dialog = await openForm();
    fireEvent.change(within(dialog).getByLabelText('Methode *'), { target: { value: 'reuse' } });
    await pickAsset(dialog);
    fireEvent.change(within(dialog).getByLabelText('Sicherheitsstufe (DIN 66399)'), {
      target: { value: 'X-9' },
    });
    fireEvent.change(within(dialog).getByLabelText('Durchgeführt von *'), {
      target: { value: 'Erika Muster' },
    });
    fireEvent.click(submitButton(dialog));

    const field = within(dialog).getByLabelText('Sicherheitsstufe (DIN 66399)');
    await waitFor(() => expect(field).toHaveAttribute('aria-invalid', 'true'));
    expect(field).toHaveAccessibleDescription(/must match/);
    expect(within(dialog).queryByRole('alert')).toBeNull();
  });

  it('shows a 422 of the lifecycle source state as message', async () => {
    stubApi({
      create: () =>
        json(
          {
            type: 'https://reticora.io/problems/invalid-transition',
            title: 'Unprocessable Entity',
            status: 422,
            detail:
              'the asset cannot be disposed from lifecycle state in_use; allowed source states: retired',
            field: 'asset_id',
          },
          422,
        ),
    });
    renderWithProviders(<DisposalListPage />, { route: '/disposal' });
    const dialog = await openForm();
    fireEvent.change(within(dialog).getByLabelText('Methode *'), {
      target: { value: 'recycling' },
    });
    await pickAsset(dialog);
    fireEvent.change(within(dialog).getByLabelText('Durchgeführt von *'), {
      target: { value: 'Erika Muster' },
    });
    fireEvent.click(submitButton(dialog));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      /allowed source states: retired/,
    );
  });

  it('offers no edit or delete and shows every field with the evidence', async () => {
    const opened = vi.spyOn(window, 'open').mockReturnValue(null);
    stubApi({ records: [RECORD] });
    renderWithProviders(<DisposalListPage />, { route: '/disposal' });

    const row = (await screen.findByText('CERT-2026-77')).closest('tr')!;
    expect(within(row).queryByRole('button', { name: /Bearbeiten|Löschen/ })).toBeNull();
    expect(screen.queryByRole('button', { name: /Bearbeiten|Löschen/ })).toBeNull();

    fireEvent.click(within(row).getByRole('button', { name: 'Details zu CERT-2026-77' }));
    const dialog = await screen.findByRole('dialog', { name: 'Entsorgungsdatensatz' });
    for (const text of [
      'H-5',
      'HDD SN-77',
      'Erika Muster',
      'Max Zeuge',
      'Vor Ort',
      'ab'.repeat(32),
    ]) {
      expect(within(dialog).getByText(text)).toBeInTheDocument();
    }
    expect(await within(dialog).findByText('Akten-Ex GmbH')).toBeInTheDocument();
    expect(await within(dialog).findByText('vernichtung.pdf')).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: /Bearbeiten|Löschen/ })).toBeNull();

    fireEvent.click(within(dialog).getByRole('button', { name: 'Nachweis herunterladen' }));
    await waitFor(() =>
      expect(opened).toHaveBeenCalledWith(
        'https://files.test/evidence.pdf',
        '_blank',
        'noopener,noreferrer',
      ),
    );
  });

  it('offers no download for an evidence that is not clean', async () => {
    stubApi({ records: [RECORD], scan: 'pending_scan' });
    renderWithProviders(<DisposalListPage />, { route: '/disposal' });
    fireEvent.click(await screen.findByRole('button', { name: 'Details zu CERT-2026-77' }));
    const dialog = await screen.findByRole('dialog', { name: 'Entsorgungsdatensatz' });
    expect(await within(dialog).findByText('vernichtung.pdf')).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: 'Nachweis herunterladen' })).toBeNull();
  });

  it('offers the creation only with disposal:write', async () => {
    stubApi({ permissions: ['disposal:read'], records: [RECORD] });
    renderWithProviders(<DisposalListPage />, { route: '/disposal' });
    expect(await screen.findByText('CERT-2026-77')).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'Entsorgung dokumentieren' })).toBeNull(),
    );
  });
});
