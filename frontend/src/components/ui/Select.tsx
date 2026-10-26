import { useId } from 'react';
import type { KeyboardEvent, SelectHTMLAttributes } from 'react';
import { cn } from './utils';

export interface SelectOption {
  value: string;
  label: string;
}

export interface SelectProps extends SelectHTMLAttributes<HTMLSelectElement> {
  label?: string;
  options: SelectOption[];
  error?: string;
}

/**
 * stepSelect moves a single select to the next enabled option for the arrow
 * keys and Home/End and reports it like a user choice (input and change
 * events). Browsers differ here: on macOS the arrow keys open the option
 * popup instead, so the component handles them itself (UI-10, WP-519).
 * Modified keys (Alt+ArrowDown opens the popup) keep their native meaning.
 */
function stepSelect(event: KeyboardEvent<HTMLSelectElement>) {
  const select = event.currentTarget;
  if (event.defaultPrevented || select.multiple || event.altKey || event.ctrlKey || event.metaKey) {
    return;
  }
  const enabled = Array.from(select.options)
    .map((option, index) => (option.disabled ? -1 : index))
    .filter((index) => index >= 0);
  if (enabled.length === 0) {
    return;
  }
  const current = select.selectedIndex;
  let next: number | undefined;
  switch (event.key) {
    case 'ArrowDown':
      next = enabled.find((index) => index > current) ?? current;
      break;
    case 'ArrowUp':
      next = [...enabled].reverse().find((index) => index < current) ?? current;
      break;
    case 'Home':
      next = enabled[0];
      break;
    case 'End':
      next = enabled[enabled.length - 1];
      break;
    default:
      return;
  }
  event.preventDefault();
  if (next === undefined || next === current || next < 0) {
    return;
  }
  select.selectedIndex = next;
  select.dispatchEvent(new Event('input', { bubbles: true }));
  select.dispatchEvent(new Event('change', { bubbles: true }));
}

export function Select({ label, options, error, id, className, onKeyDown, ...props }: SelectProps) {
  const generatedId = useId();
  const selectId = id ?? generatedId;
  const errorId = error ? `${selectId}-error` : undefined;

  return (
    <div className="space-y-1.5">
      {label ? (
        <label
          htmlFor={selectId}
          className="block text-sm font-medium text-gray-700 dark:text-gray-200"
        >
          {label}
        </label>
      ) : null}
      <select
        id={selectId}
        aria-invalid={error ? true : undefined}
        aria-describedby={errorId}
        className={cn(
          'w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 shadow-sm outline-none transition focus:border-primary focus:ring-2 focus:ring-primary/20 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-100',
          error &&
            'border-red-500 focus:border-red-500 focus:ring-red-200 dark:border-red-500 dark:focus:ring-red-500/20',
          className,
        )}
        {...props}
        onKeyDown={(event) => {
          onKeyDown?.(event);
          stepSelect(event);
        }}
      >
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
      {error ? (
        <p id={errorId} className="text-sm text-red-600 dark:text-red-400">
          {error}
        </p>
      ) : null}
    </div>
  );
}
