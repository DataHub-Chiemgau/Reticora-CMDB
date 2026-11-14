/**
 * WP-1133 (WFL-01): FormRenderer renders a form definition through
 * DynamicForm with the MET-45 conditions of ui_hints evaluated by
 * evaluateCondition; hidden fields are not sent, a signature is uploaded
 * as a PNG document and submitted as its id; a submission is shown with
 * the schema of its own version.
 *
 * Retest:
 *   cd frontend && npx vitest run src/components/form/FormRenderer.test.tsx
 */
import { useState } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import i18n from '../../i18n';
import { renderWithProviders } from '../../test/utils';
import {
  FormFill,
  FormRenderer,
  SubmissionView,
  buildForm,
  formExtras,
  parseForm,
  resolveForm,
  type FormField,
} from './FormRenderer';

const KIND: FormField = {
  key: 'kind',
  title: { de: 'Art', en: 'Kind' },
  type: 'enum',
  required: false,
  enumValues: ['router', 'switch'],
  conditions: {},
};
const SERIAL: FormField = {
  key: 'serial',
  title: { de: 'Seriennummer', en: 'Serial' },
  type: 'string',
  required: false,
  enumValues: [],
  conditions: { visible_when: { op: 'eq', field: 'kind', value: 'router' } },
};

/** The schema round trip of the editor: what the server stores. */
function stored(fields: FormField[]): FormField[] {
  const { schema, ui_hints } = buildForm(fields);
  return parseForm(JSON.parse(JSON.stringify(schema)), JSON.parse(JSON.stringify(ui_hints)));
}

/** MockXHR answers the content upload of a document at once. */
class MockXHR {
  static requests: MockXHR[] = [];
  static fail = false;
  method = '';
  url = '';
  status = 0;
  statusText = '';
  responseText = '';
  upload: { onprogress: ((event: ProgressEvent) => void) | null } = { onprogress: null };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  body: unknown;
  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }
  setRequestHeader() {}
  send(body: unknown) {
    this.body = body;
    MockXHR.requests.push(this);
    queueMicrotask(() => {
      if (MockXHR.fail) {
        this.status = 500;
        this.responseText = JSON.stringify({ status: 500, title: 'Internal Server Error' });
        this.onload?.();
        return;
      }
      this.status = 200;
      this.responseText = JSON.stringify({
        id: 'doc-1',
        title: 'signature',
        file_name: 'signature.png',
      });
      this.onload?.();
    });
  }
  abort() {}
}

beforeEach(async () => {
  await i18n.changeLanguage('de-DE');
  MockXHR.requests = [];
  MockXHR.fail = false;
});

/** signFields draws a signature into the pad of the form. */
async function drawSignature() {
  const pad = screen.getByTestId('signature-pad');
  fireEvent.pointerDown(pad, { clientX: 5, clientY: 5, pointerId: 1 });
  fireEvent.pointerMove(pad, { clientX: 40, clientY: 30, pointerId: 1 });
  fireEvent.pointerUp(pad, { clientX: 40, clientY: 30, pointerId: 1 });
  expect(await screen.findByText('Unterschrift erfasst')).toBeInTheDocument();
}

/** stubSignatureApi mocks canvas and the document API; it returns the calls. */
function stubSignatureApi() {
  const ctx = { beginPath() {}, moveTo() {}, lineTo() {}, stroke() {}, clearRect() {} };
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(
    ctx as unknown as CanvasRenderingContext2D,
  );
  vi.spyOn(HTMLCanvasElement.prototype, 'toBlob').mockImplementation(function (cb: BlobCallback) {
    cb(new Blob(['png'], { type: 'image/png' }));
  });
  const calls: { method: string; path: string; body?: unknown }[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), 'http://localhost').pathname;
      const method = init?.method ?? 'GET';
      calls.push({ method, path, body: init?.body ? JSON.parse(String(init.body)) : undefined });
      if (path === '/api/v1/documents' && method === 'POST') {
        return new Response(
          JSON.stringify({ id: 'doc-1', title: 'signature', file_name: 'signature-sign.png' }),
          { status: 201, headers: { 'Content-Type': 'application/json' } },
        );
      }
      if (path === '/api/v1/documents/doc-1' && method === 'DELETE') {
        return new Response(null, { status: 204 });
      }
      return new Response('{}', { status: 404 });
    }),
  );
  vi.stubGlobal('XMLHttpRequest', MockXHR);
  return calls;
}

const SIGN: FormField = {
  key: 'sign',
  title: { de: 'Unterschrift' },
  type: 'signature',
  required: true,
  enumValues: [],
  conditions: {},
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function Controlled({
  fields,
  initial = {},
}: {
  fields: FormField[];
  initial?: Record<string, unknown>;
}) {
  return <FormFillProbe fields={fields} initial={initial} />;
}

function FormFillProbe({
  fields,
  initial,
}: {
  fields: FormField[];
  initial: Record<string, unknown>;
}) {
  const [values, setValues] = useState(initial);
  return <FormRenderer fields={fields} values={values} onChange={setValues} />;
}

describe('FormRenderer', () => {
  it('hides a field while visible_when is false and omits it from the submission', async () => {
    const onSubmit = vi.fn();
    renderWithProviders(<FormFill fields={stored([KIND, SERIAL])} onSubmit={onSubmit} />);

    expect(screen.queryByLabelText('Seriennummer')).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Art'), { target: { value: 'router' } });
    fireEvent.change(await screen.findByLabelText('Seriennummer'), { target: { value: 'SN-1' } });
    fireEvent.change(screen.getByLabelText('Art'), { target: { value: 'switch' } });
    expect(screen.queryByLabelText('Seriennummer')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Absenden' }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith({ kind: 'switch' }));
  });

  it('requires a field only while required_when is true', async () => {
    const note: FormField = {
      ...SERIAL,
      key: 'note',
      title: { de: 'Notiz' },
      conditions: { required_when: { op: 'eq', field: 'kind', value: 'router' } },
    };
    const onSubmit = vi.fn();
    renderWithProviders(<FormFill fields={stored([KIND, note])} onSubmit={onSubmit} />);

    expect(screen.getByLabelText('Notiz')).not.toBeRequired();
    fireEvent.change(screen.getByLabelText('Art'), { target: { value: 'router' } });
    expect(screen.getByLabelText('Notiz *')).toBeRequired();
    fireEvent.click(screen.getByRole('button', { name: 'Absenden' }));
    expect(await screen.findByText('Pflichtfeld')).toBeInTheDocument();
    expect(onSubmit).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText('Art'), { target: { value: 'switch' } });
    expect(screen.getByLabelText('Notiz')).not.toBeRequired();
  });

  it('locks a field while readonly_when is true', () => {
    const locked: FormField = {
      ...SERIAL,
      key: 'locked',
      title: { de: 'Gesperrt' },
      conditions: { readonly_when: { op: 'eq', field: 'kind', value: 'switch' } },
    };
    renderWithProviders(<Controlled fields={stored([KIND, locked])} />);

    expect(screen.getByLabelText('Gesperrt')).toBeEnabled();
    fireEvent.change(screen.getByLabelText('Art'), { target: { value: 'switch' } });
    expect(screen.getByLabelText('Gesperrt')).toBeDisabled();
  });

  it('renders a submission with the schema of its own version', () => {
    // Version 1 named the field "Seriennummer"; the current version calls it
    // differently. The submission keeps the label of version 1.
    const v1 = stored([KIND, SERIAL]);
    renderWithProviders(<SubmissionView fields={v1} values={{ kind: 'router', serial: 'SN-9' }} />);

    expect(screen.getByText('Seriennummer')).toBeInTheDocument();
    expect(screen.getByText('SN-9')).toBeInTheDocument();
    expect(screen.queryByText('Serial number (v2)')).not.toBeInTheDocument();
  });

  it('uploads the signature and submits the document id', async () => {
    const sign: FormField = {
      key: 'sign',
      title: { de: 'Unterschrift' },
      type: 'signature',
      required: true,
      enumValues: [],
      conditions: {},
    };
    const ctx = { beginPath() {}, moveTo() {}, lineTo() {}, stroke() {}, clearRect() {} };
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(
      ctx as unknown as CanvasRenderingContext2D,
    );
    vi.spyOn(HTMLCanvasElement.prototype, 'toBlob').mockImplementation(function (cb: BlobCallback) {
      cb(new Blob(['png'], { type: 'image/png' }));
    });
    const posts: unknown[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const path = new URL(String(input), 'http://localhost').pathname;
        if (path === '/api/v1/documents' && init?.method === 'POST') {
          posts.push(JSON.parse(String(init.body)));
          return new Response(
            JSON.stringify({ id: 'doc-1', title: 'signature', file_name: 'signature-sign.png' }),
            {
              status: 201,
              headers: { 'Content-Type': 'application/json' },
            },
          );
        }
        return new Response('{}', { status: 404 });
      }),
    );
    vi.stubGlobal('XMLHttpRequest', MockXHR);
    const onSubmit = vi.fn();
    renderWithProviders(<FormFill fields={stored([sign])} onSubmit={onSubmit} />);

    const pad = screen.getByTestId('signature-pad');
    fireEvent.pointerDown(pad, { clientX: 5, clientY: 5, pointerId: 1 });
    fireEvent.pointerMove(pad, { clientX: 40, clientY: 30, pointerId: 1 });
    fireEvent.pointerUp(pad, { clientX: 40, clientY: 30, pointerId: 1 });
    expect(await screen.findByText('Unterschrift erfasst')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Absenden' }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith({ sign: 'doc-1' }));
    expect(posts[0]).toMatchObject({ mime_type: 'image/png', file_name: 'signature-sign.png' });
    expect(MockXHR.requests[0]?.method).toBe('PUT');
    expect(MockXHR.requests[0]?.url).toBe('/api/v1/documents/doc-1/content');
  });

  it('renders the fields through DynamicForm with the evaluated state', () => {
    const { container } = renderWithProviders(
      <Controlled fields={stored([KIND, SERIAL])} initial={{ kind: 'router' }} />,
    );
    // The layout of one section of DynamicForm (data-section-key) with the
    // visible fields as layout fields (data-field-name).
    expect(container.querySelector('[data-section-key="form"]')).not.toBeNull();
    expect(
      [...container.querySelectorAll('[data-field-name]')].map((el) =>
        el.getAttribute('data-field-name'),
      ),
    ).toEqual(['kind', 'serial']);
    expect(screen.getByLabelText('Seriennummer')).toHaveAttribute('type', 'text');
  });

  it('keeps types, titles and conditions through schema and ui_hints', () => {
    const fields: FormField[] = [
      KIND,
      SERIAL,
      {
        key: 'n',
        title: { de: 'N', en: 'N' },
        type: 'integer',
        required: true,
        enumValues: [],
        conditions: {},
      },
      {
        key: 'd',
        title: { de: 'D' },
        type: 'date',
        required: false,
        enumValues: [],
        conditions: {},
      },
      {
        key: 't',
        title: { de: 'T' },
        type: 'text',
        required: false,
        enumValues: [],
        conditions: {},
      },
    ];
    const { schema, ui_hints } = buildForm(fields);
    expect(schema).toMatchObject({
      required: ['n'],
      properties: { d: { type: 'string', format: 'date' }, kind: { enum: ['router', 'switch'] } },
    });
    expect(ui_hints).toMatchObject({
      fields: { serial: { visible_when: SERIAL.conditions.visible_when } },
    });
    expect(parseForm(schema, ui_hints).map((f) => [f.key, f.type])).toEqual([
      ['kind', 'enum'],
      ['serial', 'string'],
      ['n', 'integer'],
      ['d', 'date'],
      ['t', 'text'],
    ]);
  });
  it('keeps the unmodelled parts of a definition through the editor round trip', () => {
    const schema = {
      type: 'object',
      $comment: 'from the API',
      additionalProperties: false,
      required: ['name'],
      properties: {
        name: { type: 'string', title: 'Name', minLength: 2, maxLength: 40, pattern: '^[A-Z]' },
        level: { type: 'integer', title: 'Stufe', enum: [1, 2, 3] },
        amount: { type: 'number', minimum: 0, description: 'Betrag' },
        device: {
          type: 'object',
          properties: { kind: { type: 'string' } },
          required: ['kind'],
        },
      },
    };
    const uiHints = {
      order: ['name', 'level', 'amount', 'device'],
      sections: [{ key: 's1' }],
      fields: { name: { label: { de: 'Name' }, placeholder: 'ABC' } },
    };
    const fields = parseForm(schema, uiHints);
    expect(fields.find((f) => f.key === 'device')?.unsupported).toBe(true);
    expect(fields.find((f) => f.key === 'name')?.unsupported).toBeUndefined();
    const built = buildForm(fields, formExtras(schema, uiHints));
    expect(built.schema).toEqual(schema);
    expect(built.ui_hints).toMatchObject({
      sections: [{ key: 's1' }],
      fields: { name: { label: { de: 'Name', en: '' }, placeholder: 'ABC' } },
    });
    // A new numeric enum value stays a number.
    const level = fields.find((f) => f.key === 'level')!;
    const more = buildForm([{ ...level, enumValues: ['1', '2', '3', '4'] }]);
    expect(more.schema.properties).toEqual({
      level: { type: 'integer', title: 'Stufe', enum: [1, 2, 3, 4] },
    });
  });

  it('marks a definition with a condition it cannot parse as unsupported', () => {
    const fields = parseForm(
      { type: 'object', properties: { a: { type: 'string' }, b: { type: 'string' } } },
      { fields: { b: { visible_when: { op: 'nonsense' } } } },
    );
    expect(fields.find((f) => f.key === 'b')?.unsupported).toBe(true);
    expect(buildForm(fields).ui_hints).toMatchObject({
      fields: { b: { visible_when: { op: 'nonsense' } } },
    });
  });

  it('evaluates the conditions against the sent values until they settle', () => {
    const x: FormField = {
      key: 'x',
      title: { de: 'X' },
      type: 'boolean',
      required: false,
      enumValues: [],
      conditions: {},
    };
    const a: FormField = {
      ...SERIAL,
      key: 'a',
      title: { de: 'A' },
      conditions: { visible_when: { op: 'eq', field: 'x', value: true } },
    };
    const b: FormField = {
      ...SERIAL,
      key: 'b',
      title: { de: 'B' },
      conditions: { visible_when: { op: 'eq', field: 'a', value: 'y' } },
    };
    const resolved = resolveForm([x, a, b], { x: false, a: 'y', b: 'kept' });
    // a is hidden by x, so b (which depends on a) is hidden too, as on the server.
    expect(resolved.values).toEqual({ x: false });
    expect(resolved.states.b?.visible).toBe(false);
    expect(resolveForm([x, a, b], { x: true, a: 'y', b: 'kept' }).values).toEqual({
      x: true,
      a: 'y',
      b: 'kept',
    });
  });

  it('drops the value of a field once it becomes read-only', async () => {
    const lock: FormField = {
      key: 'lock',
      title: { de: 'Sperre' },
      type: 'boolean',
      required: false,
      enumValues: [],
      conditions: {},
    };
    const note: FormField = {
      ...SERIAL,
      key: 'note',
      title: { de: 'Notiz' },
      conditions: { readonly_when: { op: 'eq', field: 'lock', value: true } },
    };
    const onSubmit = vi.fn();
    renderWithProviders(<FormFill fields={stored([lock, note])} onSubmit={onSubmit} />);

    fireEvent.change(screen.getByLabelText('Notiz'), { target: { value: 'x' } });
    fireEvent.click(screen.getByLabelText('Sperre'));
    expect(screen.getByLabelText('Notiz')).toBeDisabled();
    expect(screen.getByLabelText('Notiz')).toHaveValue('');
    fireEvent.click(screen.getByRole('button', { name: 'Absenden' }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith({ lock: true }));
  });

  it('shows a 422 of the submission at the field of its path', async () => {
    const { ApiError } = await import('../../api/client');
    const onSubmit = vi
      .fn()
      .mockRejectedValue(
        new ApiError(422, 'Unprocessable', { detail: 'serial is invalid', field: 'values.serial' }),
      );
    renderWithProviders(<FormFill fields={stored([KIND, SERIAL])} onSubmit={onSubmit} />);
    fireEvent.change(screen.getByLabelText('Art'), { target: { value: 'router' } });
    fireEvent.change(await screen.findByLabelText('Seriennummer'), { target: { value: 'SN' } });
    fireEvent.click(screen.getByRole('button', { name: 'Absenden' }));
    expect(await screen.findByText('serial is invalid')).toBeInTheDocument();
  });

  it('blocks a signature field of a client form instead of storing it org-wide', () => {
    const sign: FormField = {
      key: 'sign',
      title: { de: 'Unterschrift' },
      type: 'signature',
      required: false,
      enumValues: [],
      conditions: {},
    };
    renderWithProviders(
      <FormFill fields={stored([sign])} onSubmit={vi.fn()} signatureUnavailable />,
    );
    expect(screen.getByRole('note')).toHaveTextContent('gehört zu einem Kunden');
    expect(screen.getByRole('button', { name: 'Absenden' })).toBeDisabled();
  });
  it('reuses the stored signature document when a failed submission is retried', async () => {
    const calls = stubSignatureApi();
    const onSubmit = vi.fn().mockRejectedValueOnce(new Error('conflict')).mockResolvedValue({});
    renderWithProviders(
      <FormFill
        fields={stored([SIGN])}
        onSubmit={onSubmit}
        signatureLinks={[{ entity_type: 'ci', entity_id: 'ci-1' }]}
      />,
    );
    await drawSignature();
    fireEvent.click(screen.getByRole('button', { name: 'Absenden' }));
    expect(await screen.findByText(/Einreichung|fehlgeschlagen/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Absenden' }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(2));
    expect(onSubmit).toHaveBeenLastCalledWith({ sign: 'doc-1' });
    const creates = calls.filter((c) => c.method === 'POST' && c.path === '/api/v1/documents');
    expect(creates).toHaveLength(1);
    // The links go with the document (E-61): no org-wide unlinked document.
    expect(creates[0]?.body).toMatchObject({ links: [{ entity_type: 'ci', entity_id: 'ci-1' }] });
  });

  it('deletes the signature document again when its content fails', async () => {
    const calls = stubSignatureApi();
    MockXHR.fail = true;
    const onSubmit = vi.fn();
    renderWithProviders(<FormFill fields={stored([SIGN])} onSubmit={onSubmit} />);
    await drawSignature();
    fireEvent.click(screen.getByRole('button', { name: 'Absenden' }));
    expect(
      await screen.findByText(/Die Unterschrift konnte nicht gespeichert werden/),
    ).toBeInTheDocument();
    expect(onSubmit).not.toHaveBeenCalled();
    await waitFor(() =>
      expect(calls.some((c) => c.method === 'DELETE' && c.path === '/api/v1/documents/doc-1')).toBe(
        true,
      ),
    );
  });
});
