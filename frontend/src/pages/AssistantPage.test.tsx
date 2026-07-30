import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen } from '@testing-library/react';
import { AssistantPage } from './AssistantPage';
import { renderWithProviders, stubFetchRoutes } from '../test/utils';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('AssistantPage', () => {
  it('asks a question and renders citations', async () => {
    stubFetchRoutes({
      '/ai/conversations': [],
      '/ai/ask': {
        conversation_id: 'conv-1',
        answer: 'Router prüfen.',
        citations: [
          {
            entity_type: 'ci',
            entity_id: 'ci-1',
            title: 'Core Router',
            url: '/cmdb/ci-1',
            score: 1,
          },
        ],
        prompt_tokens: 5,
        completion_tokens: 3,
      },
    });
    renderWithProviders(<AssistantPage />, { route: '/assistant' });
    fireEvent.change(await screen.findByLabelText('Frage'), {
      target: { value: 'Was ist kritisch?' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Fragen' }));
    expect(await screen.findByText('Router prüfen.')).toBeInTheDocument();
    expect(screen.getByText('Core Router')).toBeInTheDocument();
  });
});
