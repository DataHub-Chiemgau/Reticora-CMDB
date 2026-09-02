import { useCallback } from 'react';
import { useSearchParams } from 'react-router-dom';

export interface CIFilterState {
  search: string;
  status: string;
  ciTypeId: string;
}

const PARAM_KEYS: Record<keyof CIFilterState, string> = {
  search: 'q',
  status: 'status',
  ciTypeId: 'type',
};

/**
 * CI list filters, stored in the URL query string.
 *
 * The URL is the single source of truth so a filtered list survives a reload
 * and can be shared or bookmarked as a link. Holding this state in memory
 * instead silently discarded it on every refresh.
 *
 * Empty values are removed from the query string to keep shared links short.
 */
export function useCIFilters() {
  const [searchParams, setSearchParams] = useSearchParams();

  const setFilters = useCallback(
    (patch: Partial<CIFilterState>) => {
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current);
          for (const [field, value] of Object.entries(patch)) {
            const key = PARAM_KEYS[field as keyof CIFilterState];
            if (value) {
              next.set(key, value);
            } else {
              next.delete(key);
            }
          }
          return next;
        },
        // Filtering is not a navigation step, so it must not add history
        // entries the user has to press Back through.
        { replace: true },
      );
    },
    [setSearchParams],
  );

  return {
    search: searchParams.get(PARAM_KEYS.search) ?? '',
    status: searchParams.get(PARAM_KEYS.status) ?? '',
    ciTypeId: searchParams.get(PARAM_KEYS.ciTypeId) ?? '',
    setSearch: (search: string) => setFilters({ search }),
    setStatus: (status: string) => setFilters({ status }),
    setCITypeId: (ciTypeId: string) => setFilters({ ciTypeId }),
    reset: () => setFilters({ search: '', status: '', ciTypeId: '' }),
  };
}
