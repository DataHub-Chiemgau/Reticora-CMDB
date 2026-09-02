/**
 * Regression test: a 422 rejection from the API must land on the offending
 * form input, not only in a generic banner.
 *
 * The backend validates CI attributes against the CI type's field metadata and
 * answers with per-field `violations`. Before this test the client discarded
 * that array, so the user saw one opaque sentence and had no idea which field
 * to fix.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../test/utils';
import { CIFormModal } from './CIFormModal';

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function violationResponse() {
  return new Response(
    JSON.stringify({
      type: 'https://reticora.io/problems/422',
      title: 'Unprocessable Entity',
      status: 422,
      detail: 'serial_number: must match the required format',
      violations: [
        { field: 'serial_number', detail: 'must match the required format' },
        { field: 'ram_gb', detail: 'field is required' },
      ],
    }),
    { status: 422, headers: { 'Content-Type': 'application/json' } },
  );
}

/** Fills the two client-required fields and submits the modal. */
function fillAndSubmit() {
  fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'srv-01' } });
  fireEvent.change(screen.getByLabelText(/typ|type/i), { target: { value: 'server' } });
  fireEvent.click(screen.getByRole('button', { name: /anlegen|create/i }));
}

describe('CIFormModal server-side validation', () => {
  it('renders a 422 violation on the matching input and unmapped ones in the banner', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async () => violationResponse()),
    );

    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    fillAndSubmit();

    // The violation whose field matches a form input is shown inline.
    await waitFor(() => {
      expect(screen.getByText('must match the required format')).toBeInTheDocument();
    });

    // `ram_gb` is a dynamic attribute with no input in this modal, so it must
    // still be reported rather than silently dropped.
    expect(screen.getByRole('alert')).toHaveTextContent('ram_gb: field is required');
  });

  it('shows the plain detail when the failure has no violations', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(
        async () =>
          new Response(JSON.stringify({ status: 409, detail: 'name already in use' }), {
            status: 409,
            headers: { 'Content-Type': 'application/json' },
          }),
      ),
    );

    renderWithProviders(<CIFormModal open onOpenChange={() => {}} />);

    fillAndSubmit();

    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent('name already in use');
    });
  });
});
