import * as Dialog from '@radix-ui/react-dialog';
import { X } from 'lucide-react';
import { useId, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { cn } from './utils';

type ModalSize = 'sm' | 'md' | 'lg' | 'xl';

const sizeClasses: Record<ModalSize, string> = {
  sm: 'w-[min(92vw,28rem)]',
  md: 'w-[min(92vw,40rem)]',
  lg: 'w-[min(94vw,56rem)]',
  xl: 'w-[min(96vw,72rem)]',
};

interface ModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  size?: ModalSize;
  children: React.ReactNode;
}

export function Modal({
  open,
  onOpenChange,
  title,
  description,
  size = 'md',
  children,
}: ModalProps) {
  const { t } = useTranslation();
  const titleId = useId();
  const descriptionId = description ? `${titleId}-description` : undefined;
  // Radix returns focus only to a Dialog.Trigger. Modal is opened from
  // arbitrary controls, so it remembers the focused element itself and
  // restores it when the dialog closes (UI-10, WP-192).
  const returnFocusRef = useRef<HTMLElement | null>(null);
  // Taken while the dialog renders open, before its content mounts: a field
  // with autoFocus would already have moved focus when Radix's open handler
  // runs, and Escape would return focus to that field instead of the opener.
  const wasOpenRef = useRef(false);
  if (open && !wasOpenRef.current) {
    wasOpenRef.current = true;
    returnFocusRef.current =
      typeof document !== 'undefined' && document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
  } else if (!open) {
    wasOpenRef.current = false;
  }

  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 animate-fade-in bg-gray-950/55 backdrop-blur-[2px]" />
        {/* Portaled under <body>, outside the App shell that gives the pages
            their text colour: the dialog sets the theme text colour itself, so
            text without a colour class (checkbox labels) is not the browser
            default black on the dark dialog. */}
        <Dialog.Content
          onCloseAutoFocus={(event) => {
            const target = returnFocusRef.current;
            if (target && target.isConnected) {
              event.preventDefault();
              target.focus();
            }
          }}
          aria-labelledby={titleId}
          aria-describedby={descriptionId}
          className={cn(
            'fixed left-1/2 top-1/2 z-50 max-h-[88vh] -translate-x-1/2 -translate-y-1/2 animate-pop-in overflow-y-auto rounded-xl border border-line bg-panel p-6 text-gray-900 shadow-2xl outline-none scroll-thin dark:text-gray-100',
            sizeClasses[size],
          )}
        >
          <div className="mb-5 flex items-start justify-between gap-4">
            <div className="min-w-0">
              <Dialog.Title id={titleId} className="text-lg font-semibold text-fg">
                {title}
              </Dialog.Title>
              {description ? (
                <Dialog.Description id={descriptionId} className="mt-1 text-sm text-fg-muted">
                  {description}
                </Dialog.Description>
              ) : null}
            </div>
            <Dialog.Close asChild>
              <button
                type="button"
                aria-label={t('accessibility.closeModal')}
                className="-mr-2 -mt-1 inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg text-fg-muted transition-colors hover:bg-fg/[0.06] hover:text-fg focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
              >
                <X aria-hidden="true" className="h-4 w-4" />
              </button>
            </Dialog.Close>
          </div>
          {children}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
