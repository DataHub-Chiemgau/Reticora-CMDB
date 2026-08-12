import { create } from 'zustand';

export type ToastVariant = 'info' | 'success' | 'error';

export interface Toast {
  id: number;
  message: string;
  variant: ToastVariant;
  actionLabel?: string;
  onAction?: () => void;
  durationMs: number;
}

interface ToastState {
  toasts: Toast[];
  push: (toast: {
    message: string;
    variant?: ToastVariant;
    actionLabel?: string;
    onAction?: () => void;
    durationMs?: number;
  }) => number;
  dismiss: (id: number) => void;
}

let nextToastId = 1;

export const useToastStore = create<ToastState>((set) => ({
  toasts: [],
  push: ({ message, variant = 'info', actionLabel, onAction, durationMs = 6000 }) => {
    const id = nextToastId;
    nextToastId += 1;
    set((state) => ({
      toasts: [...state.toasts, { id, message, variant, actionLabel, onAction, durationMs }],
    }));
    return id;
  },
  dismiss: (id) => {
    set((state) => ({ toasts: state.toasts.filter((toast) => toast.id !== id) }));
  },
}));
