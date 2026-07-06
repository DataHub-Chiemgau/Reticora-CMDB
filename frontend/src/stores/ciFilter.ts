import { create } from 'zustand';

interface CIFilterState {
  search: string;
  status: string;
  ciTypeId: string;
  setSearch: (search: string) => void;
  setStatus: (status: string) => void;
  setCITypeId: (ciTypeId: string) => void;
  reset: () => void;
}

export const useCIFilterStore = create<CIFilterState>((set) => ({
  search: '',
  status: '',
  ciTypeId: '',
  setSearch: (search) => set({ search }),
  setStatus: (status) => set({ status }),
  setCITypeId: (ciTypeId) => set({ ciTypeId }),
  reset: () => set({ search: '', status: '', ciTypeId: '' }),
}));
