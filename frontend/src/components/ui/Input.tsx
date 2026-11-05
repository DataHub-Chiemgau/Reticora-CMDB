import { useId } from 'react';
import type { InputHTMLAttributes, ReactNode } from 'react';
import { cn } from './utils';

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  label?: string;
  error?: string;
  /** A 16px icon inside the field, before the text (decorative). */
  icon?: ReactNode;
}

/** fieldClasses is the look of every single-line control (input, select). */
export const fieldClasses =
  'h-9 w-full rounded-lg border border-control bg-panel px-3 text-sm text-fg shadow-sm outline-none transition-[border-color,box-shadow] placeholder:text-fg-subtle hover:border-fg-muted focus:border-accent focus:ring-2 focus:ring-accent/25 disabled:cursor-not-allowed disabled:bg-canvas disabled:opacity-60';

export const fieldErrorClasses =
  'border-red-500 hover:border-red-500 focus:border-red-500 focus:ring-red-500/25';

export const labelClasses = 'block text-sm font-medium text-fg';

export function Input({ label, error, id, className, icon, ...props }: InputProps) {
  const generatedId = useId();
  const inputId = id ?? generatedId;
  const errorId = error ? `${inputId}-error` : undefined;

  const input = (
    <input
      id={inputId}
      aria-invalid={error ? true : undefined}
      aria-describedby={errorId}
      className={cn(fieldClasses, icon ? 'pl-9' : false, error && fieldErrorClasses, className)}
      {...props}
    />
  );

  return (
    <div className="space-y-1.5">
      {label ? (
        <label htmlFor={inputId} className={labelClasses}>
          {label}
        </label>
      ) : null}
      {icon ? (
        <div className="relative">
          <span
            aria-hidden="true"
            className="pointer-events-none absolute inset-y-0 left-3 flex items-center text-fg-subtle [&_svg]:h-4 [&_svg]:w-4"
          >
            {icon}
          </span>
          {input}
        </div>
      ) : (
        input
      )}
      {error ? (
        <p id={errorId} className="text-sm text-danger">
          {error}
        </p>
      ) : null}
    </div>
  );
}
