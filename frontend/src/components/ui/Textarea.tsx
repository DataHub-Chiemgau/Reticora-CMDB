import { useId } from 'react';
import type { TextareaHTMLAttributes } from 'react';
import { fieldClasses, fieldErrorClasses, labelClasses } from './Input';
import { cn } from './utils';

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  label?: string;
  error?: string;
}

export function Textarea({ label, error, id, className, rows = 4, ...props }: TextareaProps) {
  const generatedId = useId();
  const textareaId = id ?? generatedId;
  const errorId = error ? `${textareaId}-error` : undefined;

  return (
    <div className="space-y-1.5">
      {label ? (
        <label htmlFor={textareaId} className={labelClasses}>
          {label}
        </label>
      ) : null}
      <textarea
        id={textareaId}
        rows={rows}
        aria-invalid={error ? true : undefined}
        aria-describedby={errorId}
        className={cn(fieldClasses, 'h-auto py-2', error && fieldErrorClasses, className)}
        {...props}
      />
      {error ? (
        <p id={errorId} className="text-sm text-danger">
          {error}
        </p>
      ) : null}
    </div>
  );
}
