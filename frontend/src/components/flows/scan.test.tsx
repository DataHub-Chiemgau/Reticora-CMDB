// UI-15 and INV-03 (WP-807): scanning with camera, handheld scanner and code
// lookup, and the signature on handover and return. BarcodeDetector and
// getUserMedia are mocked. Without secure context or with a refused
// permission the dialog falls back to the text field and says why; without
// BarcodeDetector @zxing/browser is loaded. An object URN is resolved
// through the GET of its kind without a lookup, any other code through GET
// /labels/lookup; 404 says "not found". A hit inside a flow fills the
// picker, outside a flow it shows its actions. Without barcode neither scan
// button nor command exist. The SignaturePad hands its PNG to the field
// signature of POST /assets/{id}/assign and …/return; without the feature
// signature there is no field, above the size limit nothing is sent.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import i18n from '../../i18n';
import { renderWithProviders } from '../../test/utils';
import { useAuthStore } from '../../auth/authStore';
import { useToastStore } from '../../stores/toast';
import { UNDO_WINDOW_MS } from '../../hooks/useUndoableDelete';
import { CommandPalette } from '../CommandPalette';
import { ToastViewport } from '../ui/Toast';
import { ScanDialog } from './ScanDialog';
import { AssignDialog, ReturnDialog } from './AssignDialogs';

const zxing = vi.hoisted(() => ({
  decodeFromStream: vi.fn(),
  constructed: 0,
}));

vi.mock('@zxing/browser', () => ({
  BrowserMultiFormatReader: class {
    constructor() {
      zxing.constructed += 1;
    }
    decodeFromStream = zxing.decodeFromStream;
  },
}));

const ASSET = 'a5000000-0000-4000-8000-000000000001';
const CONSUMABLE = 'c2000000-0000-4000-8000-000000000001';
const SITE = 'aa000000-0000-4000-8000-000000000001';
const BIN = 'aa000000-0000-4000-8000-000000000002';
const USER = 'b1000000-0000-4000-8000-000000000001';
const PNG =
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==';

interface Call {
  method: string;
  path: string;
  search: string;
  body: unknown;
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' },
  });
}

const page = (data: unknown[]) => ({
  data,
  total: data.length,
  limit: 50,
  offset: 0,
  has_more: false,
});

const asset = { id: ASSET, name: 'Laptop 7', inventory_no: 'INV-7', custom_fields: {} };
const consumable = { id: CONSUMABLE, name: 'Toner', sku: 'TN-1', unit: 'pcs' };
const bin = {
  id: BIN,
  organization_id: 'org',
  client_id: 'cl',
  site_id: SITE,
  parent_id: SITE,
  kind: 'bin',
  name: 'Fach A1',
  path: 'x',
  created_at: '2026-11-01T10:00:00Z',
  updated_at: '2026-11-01T10:00:00Z',
};

interface Stub {
  features?: string[];
  /** lookup answers GET /labels/lookup?code=; 404 by default. */
  lookup?: (code: string) => Response;
  assignments?: unknown[];
}

function stubApi({
  features = ['barcode', 'assets_basic', 'stock'],
  lookup,
  assignments = [],
}: Stub = {}) {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://test');
      const method = (init?.method ?? 'GET').toUpperCase();
      const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined;
      const path = url.pathname.replace(/^\/api\/v1/, '');
      calls.push({ method, path, search: url.search, body });
      if (method !== 'GET') return json({ id: 'new', ...body }, 201);
      if (path === '/modules') {
        return json({
          data: ['inventory', 'stocktake'].map((key) => ({
            key,
            enabled: true,
            phase: 'P2',
            nav: [],
          })),
        });
      }
      if (path === '/entitlements') {
        return json(page(features.map((feature_key) => ({ feature_key, enabled: true }))));
      }
      if (path === '/me/permissions') return json({ user_id: USER, permissions: [] });
      if (path === '/labels/lookup') {
        const code = url.searchParams.get('code') ?? '';
        return lookup
          ? lookup(code)
          : json({ type: 'about:blank', title: 'Not Found', status: 404, detail: 'nope' }, 404);
      }
      if (path === `/assets/${ASSET}`) return json(asset);
      if (path === '/assets') return json(page([asset]));
      if (path === `/consumables/${CONSUMABLE}`) return json(consumable);
      if (path === `/locations/${BIN}`) return json(bin);
      if (path === '/locations/tree') {
        return json({
          data: [
            { id: SITE, name: 'Werk Nord', kind: 'site', children: [{ ...bin, children: [] }] },
          ],
        });
      }
      if (path === '/assignments') return json(page(assignments));
      if (path === '/users') return json(page([{ id: USER, display_name: 'Anna Berg' }]));
      if (path === `/users/${USER}`) return json({ id: USER, display_name: 'Anna Berg' });
      return json(page([]));
    }),
  );
  return calls;
}

const gets = (calls: Call[], path: string) =>
  calls.filter((call) => call.method === 'GET' && call.path === path);
const writes = (calls: Call[]) => calls.filter((call) => call.method !== 'GET');

function signIn() {
  useAuthStore.setState({
    token: 'header.payload.signature',
    expiresAt: new Date(Date.now() + 3600_000).toISOString(),
    user: { sub: 'user-1' },
  });
}

function setSecureContext(value: boolean) {
  Object.defineProperty(window, 'isSecureContext', { value, configurable: true });
}

function setMediaDevices(getUserMedia: (() => Promise<MediaStream>) | undefined) {
  Object.defineProperty(navigator, 'mediaDevices', {
    value: getUserMedia ? { getUserMedia } : undefined,
    configurable: true,
  });
}

/** fakeStream is a camera stream with one track whose stop is recorded. */
function fakeStream() {
  const track = { stop: vi.fn() };
  const stream = { getTracks: () => [track] } as unknown as MediaStream;
  return { stream, track };
}

/** typeCode enters a code into the text field of the scan dialog and ends it with Enter. */
function typeCode(dialog: HTMLElement, code: string) {
  const input = within(dialog).getByLabelText('Code (Handscanner oder Eingabe)');
  fireEvent.change(input, { target: { value: code } });
  fireEvent.submit(input.closest('form')!);
}

async function scanDialog() {
  return screen.findByRole('dialog', { name: 'Code scannen' });
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
  useToastStore.setState({ toasts: [] });
  zxing.decodeFromStream.mockReset();
  zxing.constructed = 0;
  setSecureContext(false);
  setMediaDevices(undefined);
  vi.spyOn(HTMLMediaElement.prototype, 'play').mockResolvedValue(undefined);
  signIn();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useAuthStore.setState({ token: null, expiresAt: null, user: null });
});

describe('scanning', () => {
  it('falls back to the text field without a secure context and resolves the code by lookup', async () => {
    const getUserMedia = vi.fn();
    setMediaDevices(getUserMedia);
    const calls = stubApi({
      lookup: (code) =>
        code === 'INV-7' ? json({ kind: 'asset', id: ASSET, name: 'Laptop 7' }) : json({}, 404),
    });
    renderWithProviders(<ScanDialog open onOpenChange={vi.fn()} />);
    const dialog = await scanDialog();
    expect(within(dialog).getByRole('status')).toHaveTextContent(/nur über HTTPS oder localhost/);
    expect(getUserMedia).not.toHaveBeenCalled();

    typeCode(dialog, 'INV-7');
    const result = await within(dialog).findByRole('region', { name: 'Gescanntes Objekt' });
    expect(within(result).getByText('Laptop 7')).toBeInTheDocument();
    expect(within(result).getByText('INV-7')).toBeInTheDocument();
    const lookups = gets(calls, '/labels/lookup');
    expect(lookups).toHaveLength(1);
    expect(new URLSearchParams(lookups[0]!.search).get('code')).toBe('INV-7');
  });

  it('falls back to the text field when the camera is refused', async () => {
    setSecureContext(true);
    setMediaDevices(() => Promise.reject(new DOMException('no', 'NotAllowedError')));
    stubApi();
    renderWithProviders(<ScanDialog open onOpenChange={vi.fn()} />);
    const dialog = await scanDialog();
    await waitFor(() =>
      expect(within(dialog).getByRole('status')).toHaveTextContent(/nicht freigegeben/),
    );
    expect(within(dialog).getByLabelText('Code (Handscanner oder Eingabe)')).toBeInTheDocument();
  });

  it('loads @zxing/browser without BarcodeDetector, resolves an URN without lookup and ends the stream on close', async () => {
    setSecureContext(true);
    const { stream, track } = fakeStream();
    const getUserMedia = vi.fn(() => Promise.resolve(stream));
    setMediaDevices(getUserMedia);
    const stop = vi.fn();
    zxing.decodeFromStream.mockImplementation(
      async (_stream: MediaStream, _video: HTMLVideoElement, callback: (r: unknown) => void) => {
        callback({ getText: () => `urn:reticora:asset:${ASSET}` });
        return { stop };
      },
    );
    const calls = stubApi();
    const onOpenChange = vi.fn();
    const view = renderWithProviders(<ScanDialog open onOpenChange={onOpenChange} />);
    const dialog = await scanDialog();

    expect(getUserMedia).toHaveBeenCalledWith(
      expect.objectContaining({ video: { facingMode: { ideal: 'environment' } } }),
    );
    await waitFor(() => expect(zxing.decodeFromStream).toHaveBeenCalled());
    expect(zxing.constructed).toBe(1);
    expect(await within(dialog).findByText('Laptop 7')).toBeInTheDocument();
    expect(gets(calls, `/assets/${ASSET}`)).toHaveLength(1);
    expect(gets(calls, '/labels/lookup')).toHaveLength(0);
    // The hit pauses the camera: the stream ends.
    await waitFor(() => expect(track.stop).toHaveBeenCalled());

    fireEvent.click(within(dialog).getByRole('button', { name: 'Weiter scannen' }));
    await waitFor(() => expect(getUserMedia).toHaveBeenCalledTimes(2));
    view.rerender(<ScanDialog open={false} onOpenChange={onOpenChange} />);
    await waitFor(() => expect(stop).toHaveBeenCalled());
    expect(track.stop.mock.calls.length).toBeGreaterThanOrEqual(2);
  });

  it('reads Code128 and QR with BarcodeDetector and shows the actions of an asset', async () => {
    setSecureContext(true);
    const { stream } = fakeStream();
    setMediaDevices(() => Promise.resolve(stream));
    const formats: unknown[] = [];
    vi.stubGlobal(
      'BarcodeDetector',
      class {
        constructor(options: { formats: string[] }) {
          formats.push(options.formats);
        }
        detect() {
          return Promise.resolve([{ rawValue: `urn:reticora:asset:${ASSET}`, format: 'qr_code' }]);
        }
      },
    );
    const calls = stubApi({
      assignments: [
        {
          id: 'as-1',
          asset_id: ASSET,
          assigned_to: USER,
          assigned_by: USER,
          assignment_type: 'assignment',
          status: 'active',
          assigned_at: '2026-11-01T10:00:00Z',
        },
      ],
    });
    renderWithProviders(<ScanDialog open onOpenChange={vi.fn()} />);
    const dialog = await scanDialog();
    const result = await within(dialog).findByRole('region', { name: 'Gescanntes Objekt' });
    expect(formats).toEqual([['code_128', 'qr_code']]);
    expect(zxing.decodeFromStream).not.toHaveBeenCalled();
    expect(gets(calls, '/labels/lookup')).toHaveLength(0);
    expect(within(result).getByText('Asset')).toBeInTheDocument();
    for (const name of ['Zuweisen', 'Reservieren', 'Historie']) {
      expect(within(result).getByRole('button', { name })).toBeInTheDocument();
    }
    await waitFor(() =>
      expect(within(result).getByRole('button', { name: 'Zurücknehmen' })).toBeEnabled(),
    );
    expect(within(result).queryByRole('button', { name: 'Empfangen' })).not.toBeInTheDocument();
  });

  it('says "not found" for an unknown code', async () => {
    stubApi();
    renderWithProviders(<ScanDialog open onOpenChange={vi.fn()} />);
    const dialog = await scanDialog();
    typeCode(dialog, 'XYZ-404');
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('„XYZ-404“ nicht gefunden.');
  });

  it('shows receive, move and history for a consumable and name and path for a location', async () => {
    const calls = stubApi({
      lookup: (code) =>
        code === 'BIN-A1' ? json({ kind: 'location', id: BIN, name: 'Fach A1' }) : json({}, 404),
    });
    renderWithProviders(<ScanDialog open onOpenChange={vi.fn()} />);
    const dialog = await scanDialog();
    typeCode(dialog, `urn:reticora:consumable:${CONSUMABLE}`);
    let result = await within(dialog).findByRole('region', { name: 'Gescanntes Objekt' });
    expect(within(result).getByText('Toner')).toBeInTheDocument();
    expect(gets(calls, `/consumables/${CONSUMABLE}`)).toHaveLength(1);
    for (const name of ['Empfangen', 'Verschieben', 'Historie']) {
      expect(within(result).getByRole('button', { name })).toBeInTheDocument();
    }
    expect(within(result).queryByRole('button', { name: 'Zuweisen' })).not.toBeInTheDocument();

    fireEvent.click(within(result).getByRole('button', { name: 'Weiter scannen' }));
    typeCode(dialog, 'BIN-A1');
    result = await within(dialog).findByRole('region', { name: 'Gescanntes Objekt' });
    expect(within(result).getByText('Standort')).toBeInTheDocument();
    expect(await within(result).findByText('Werk Nord › Fach A1')).toBeInTheDocument();
    expect(within(result).queryByRole('button', { name: 'Historie' })).not.toBeInTheDocument();
  });

  it('puts a hit into the picker of a flow', async () => {
    stubApi({ lookup: () => json({ kind: 'asset', id: ASSET, name: 'Laptop 7' }) });
    renderWithProviders(<AssignDialog open onOpenChange={vi.fn()} />);
    const flow = await screen.findByRole('dialog', { name: 'Zuweisen' });
    fireEvent.click(await within(flow).findByRole('button', { name: 'Asset per Scan wählen' }));
    typeCode(await scanDialog(), 'INV-7');
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Code scannen' })).not.toBeInTheDocument(),
    );
    const chosen = within(flow).getByRole('list', { name: 'Ausgewählt' });
    expect(within(chosen).getByText('Laptop 7')).toBeInTheDocument();
  });

  it('refuses a hit of another kind inside a flow', async () => {
    stubApi();
    renderWithProviders(<AssignDialog open onOpenChange={vi.fn()} />);
    const flow = await screen.findByRole('dialog', { name: 'Zuweisen' });
    fireEvent.click(await within(flow).findByRole('button', { name: 'Asset per Scan wählen' }));
    const dialog = await scanDialog();
    typeCode(dialog, `urn:reticora:consumable:${CONSUMABLE}`);
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(/einen Mengenartikel/);
  });

  it('offers neither scan button nor command without the feature barcode', async () => {
    stubApi({ features: ['assets_basic'] });
    renderWithProviders(<AssignDialog open onOpenChange={vi.fn()} />);
    const flow = await screen.findByRole('dialog', { name: 'Zuweisen' });
    await waitFor(() =>
      expect(
        within(flow).queryByRole('button', { name: 'Asset per Scan wählen' }),
      ).not.toBeInTheDocument(),
    );
    expect(within(flow).getByRole('combobox', { name: 'Asset' })).toBeInTheDocument();
  });

  it('has the command "Scannen" only with onScan, which it calls', () => {
    stubApi();
    const props = { onNavigate: vi.fn(), onCreateCI: vi.fn(), onToggleDarkMode: vi.fn() };
    const view = renderWithProviders(<CommandPalette {...props} />);
    fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'Scannen' } });
    expect(screen.queryByRole('option', { name: /^Scannen/ })).not.toBeInTheDocument();
    view.unmount();

    const onScan = vi.fn();
    renderWithProviders(<CommandPalette {...props} onScan={onScan} />);
    fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'Scannen' } });
    fireEvent.click(screen.getByRole('option', { name: /^Scannen/ }));
    expect(onScan).toHaveBeenCalledTimes(1);
  });
});

describe('signature', () => {
  let dataUrl = `data:image/png;base64,${PNG}`;

  beforeEach(() => {
    dataUrl = `data:image/png;base64,${PNG}`;
    const context = {
      beginPath: vi.fn(),
      moveTo: vi.fn(),
      lineTo: vi.fn(),
      stroke: vi.fn(),
      arc: vi.fn(),
      fill: vi.fn(),
      clearRect: vi.fn(),
    };
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(
      context as unknown as CanvasRenderingContext2D,
    );
    vi.spyOn(HTMLCanvasElement.prototype, 'toDataURL').mockImplementation(() => dataUrl);
  });

  /** sign draws a stroke on the pad of the scope. */
  function sign(scope: HTMLElement, label: string) {
    const pad = within(scope).getByRole('img', { name: label });
    fireEvent.pointerDown(pad, { clientX: 10, clientY: 10, pointerId: 1 });
    fireEvent.pointerMove(pad, { clientX: 40, clientY: 30, pointerId: 1 });
    fireEvent.pointerUp(pad, { clientX: 40, clientY: 30, pointerId: 1 });
  }

  async function choose(scope: HTMLElement, label: string, text: string, option: RegExp) {
    fireEvent.change(within(scope).getByRole('combobox', { name: label }), {
      target: { value: text },
    });
    fireEvent.click(await within(scope).findByRole('option', { name: option }));
  }

  it('sends the PNG of the pad as signature of the assignment after the undo window', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = stubApi({ features: ['assets_basic', 'signature'] });
    renderWithProviders(
      <>
        <AssignDialog open onOpenChange={vi.fn()} />
        <ToastViewport />
      </>,
    );
    const flow = await screen.findByRole('dialog', { name: 'Zuweisen' });
    await choose(flow, 'Asset', 'Lap', /Laptop 7/);
    await choose(flow, 'Empfänger', 'Ann', /Anna Berg/);
    sign(flow, 'Unterschrift bei Übergabe');
    expect(await within(flow).findByText(/Unterschrift erfasst/)).toBeInTheDocument();
    fireEvent.click(within(flow).getByRole('button', { name: 'Zuweisen' }));
    expect(writes(calls)).toHaveLength(0);
    act(() => {
      vi.advanceTimersByTime(UNDO_WINDOW_MS);
    });
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    const [write] = writes(calls);
    expect(write!.path).toBe(`/assets/${ASSET}/assign`);
    expect(write!.body).toMatchObject({
      assigned_user_id: USER,
      signature: { image_png_base64: PNG },
    });
  });

  it('sends the signature of a return', async () => {
    const calls = stubApi({ features: ['assets_basic', 'signature'] });
    renderWithProviders(
      <ReturnDialog
        open
        onOpenChange={vi.fn()}
        target={{ assetId: ASSET, assetLabel: 'Laptop 7', holder: USER, holderLabel: 'Anna Berg' }}
      />,
    );
    const flow = await screen.findByRole('dialog');
    sign(flow, 'Unterschrift bei Rückgabe');
    fireEvent.click(within(flow).getByRole('button', { name: 'Zurücknehmen' }));
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    expect(writes(calls)[0]!.path).toBe(`/assets/${ASSET}/return`);
    expect(writes(calls)[0]!.body).toMatchObject({
      expected_holder: USER,
      signature: { image_png_base64: PNG },
    });
  });

  it('has no signature field without the feature signature', async () => {
    const calls = stubApi({ features: ['assets_basic'] });
    renderWithProviders(
      <ReturnDialog
        open
        onOpenChange={vi.fn()}
        target={{ assetId: ASSET, assetLabel: 'Laptop 7', holder: USER, holderLabel: 'Anna Berg' }}
      />,
    );
    const flow = await screen.findByRole('dialog');
    await waitFor(() => expect(gets(calls, '/entitlements')).toHaveLength(1));
    await waitFor(() =>
      expect(within(flow).queryByRole('img', { name: 'Unterschrift bei Rückgabe' })).toBeNull(),
    );
    fireEvent.click(within(flow).getByRole('button', { name: 'Zurücknehmen' }));
    await waitFor(() => expect(writes(calls)).toHaveLength(1));
    expect(writes(calls)[0]!.body).not.toHaveProperty('signature');
  });

  it('sends nothing above the size limit and says so', async () => {
    dataUrl = `data:image/png;base64,${'A'.repeat(800_000)}`;
    const calls = stubApi({ features: ['assets_basic', 'signature'] });
    renderWithProviders(
      <ReturnDialog
        open
        onOpenChange={vi.fn()}
        target={{ assetId: ASSET, assetLabel: 'Laptop 7', holder: USER, holderLabel: 'Anna Berg' }}
      />,
    );
    const flow = await screen.findByRole('dialog');
    sign(flow, 'Unterschrift bei Rückgabe');
    fireEvent.click(within(flow).getByRole('button', { name: 'Zurücknehmen' }));
    expect(await within(flow).findByText(/zu groß/)).toBeInTheDocument();
    expect(writes(calls)).toHaveLength(0);
  });
});
