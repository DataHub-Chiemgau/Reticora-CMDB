import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import './i18n';
import './index.css';
import App from './App';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      retry: 1,
    },
  },
});

// React Router 7 wraps every router state update in React.startTransition
// unless useTransitions is false. The app keeps the synchronous updates of
// React Router 6: a search parameter set by a control (role, template, filter)
// must re-render the page before the next input lands, otherwise the reload
// of the page state overwrites what the user typed meanwhile (Upd-05, E-245).
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter useTransitions={false}>
      <QueryClientProvider client={queryClient}>
        <App />
      </QueryClientProvider>
    </BrowserRouter>
  </StrictMode>,
);
