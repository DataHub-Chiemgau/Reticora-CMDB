import * as Dialog from '@radix-ui/react-dialog';
import { useId } from 'react';
import { useTranslation } from 'react-i18next';

interface ModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  children: React.ReactNode;
}

export function Modal({ open, onOpenChange, title, description, children }: ModalProps) {
  const { t } = useTranslation();
  const titleId = useId();
  const descriptionId = description ? `${titleId}-description` : undefined;

  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-gray-950/50 backdrop-blur-sm" />
        <Dialog.Content
          aria-labelledby={titleId}
          aria-describedby={descriptionId}
          className="fixed left-1/2 top-1/2 z-50 max-h-[85vh] w-[min(92vw,40rem)] -translate-x-1/2 -translate-y-1/2 overflow-y-auto rounded-2xl border border-gray-200 bg-white p-6 shadow-2xl outline-none dark:border-gray-800 dark:bg-gray-900"
        >
          <div className="mb-5 flex items-start justify-between gap-4">
            <div>
              <Dialog.Title
                id={titleId}
                className="text-lg font-semibold text-gray-900 dark:text-gray-100"
              >
                {title}
              </Dialog.Title>
              {description ? (
                <Dialog.Description
                  id={descriptionId}
                  className="mt-1 text-sm text-gray-600 dark:text-gray-300"
                >
                  {description}
                </Dialog.Description>
              ) : null}
            </div>
            <Dialog.Close asChild>
              <button
                type="button"
                aria-label={t('accessibility.closeModal')}
                className="rounded-md p-2 text-gray-500 transition hover:bg-gray-100 hover:text-gray-700 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 dark:text-gray-400 dark:hover:bg-gray-800 dark:hover:text-gray-100"
              >
                ✕
              </button>
            </Dialog.Close>
          </div>
          {children}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
