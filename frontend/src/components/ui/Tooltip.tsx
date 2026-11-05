import * as RadixTooltip from '@radix-ui/react-tooltip';
import type { ReactNode } from 'react';

/**
 * Tooltip names an icon-only control for pointer and keyboard users. It
 * complements the accessible name of the trigger, it never replaces it.
 */
export function Tooltip({
  label,
  children,
  side = 'bottom',
  disabled = false,
}: {
  label: ReactNode;
  children: ReactNode;
  side?: 'top' | 'right' | 'bottom' | 'left';
  disabled?: boolean;
}) {
  if (disabled) {
    return <>{children}</>;
  }
  return (
    <RadixTooltip.Provider delayDuration={350} skipDelayDuration={200}>
      <RadixTooltip.Root>
        <RadixTooltip.Trigger asChild>{children}</RadixTooltip.Trigger>
        <RadixTooltip.Portal>
          <RadixTooltip.Content
            side={side}
            sideOffset={8}
            className="z-[70] animate-fade-in rounded-md bg-gray-900 px-2 py-1 text-xs font-medium text-gray-50 shadow-md dark:bg-gray-100 dark:text-gray-900"
          >
            {label}
          </RadixTooltip.Content>
        </RadixTooltip.Portal>
      </RadixTooltip.Root>
    </RadixTooltip.Provider>
  );
}
