import { Slot } from '@radix-ui/react-slot';
import type { ButtonHTMLAttributes, ReactNode } from 'react';
import { cn } from './utils';

type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger';
type ButtonSize = 'sm' | 'md' | 'lg';

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  asChild?: boolean;
  variant?: ButtonVariant;
  size?: ButtonSize;
  loading?: boolean;
  /** A 16px icon before the label (decorative, the label names the action). */
  icon?: ReactNode;
  /** A 16px icon after the label (forward actions such as "Next"). */
  iconAfter?: ReactNode;
  children: ReactNode;
}

export const buttonVariantClasses: Record<ButtonVariant, string> = {
  primary:
    'bg-accent text-accent-fg shadow-sm hover:bg-accent-strong hover:text-accent-fg active:translate-y-px',
  secondary:
    'border border-line-strong bg-panel text-fg shadow-sm hover:bg-fg/[0.04] active:translate-y-px',
  ghost: 'text-fg-muted hover:bg-fg/[0.06] hover:text-fg',
  danger: 'bg-red-600 text-white shadow-sm hover:bg-red-700 active:translate-y-px',
};

export const buttonSizeClasses: Record<ButtonSize, string> = {
  sm: 'h-8 gap-1.5 px-3 text-sm',
  md: 'h-9 gap-2 px-3.5 text-sm',
  lg: 'h-10 gap-2 px-4 text-sm',
};

export const buttonBaseClasses =
  'inline-flex shrink-0 select-none items-center justify-center whitespace-nowrap rounded-lg font-medium transition-[background-color,color,box-shadow,transform] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:pointer-events-none disabled:opacity-50 aria-disabled:pointer-events-none aria-disabled:opacity-50 [&_svg]:h-4 [&_svg]:w-4 [&_svg]:shrink-0';

export function Button({
  asChild = false,
  variant = 'primary',
  size = 'md',
  loading = false,
  icon,
  iconAfter,
  disabled,
  className,
  children,
  type = 'button',
  ...props
}: ButtonProps) {
  const Comp = asChild ? Slot : 'button';
  const isDisabled = disabled || loading;

  return (
    <Comp
      className={cn(
        buttonBaseClasses,
        buttonVariantClasses[variant],
        buttonSizeClasses[size],
        className,
      )}
      {...(!asChild
        ? { disabled: isDisabled, type }
        : { 'aria-disabled': isDisabled || undefined })}
      {...props}
    >
      {asChild ? (
        children
      ) : (
        <>
          {loading ? (
            <span
              aria-hidden="true"
              className="h-4 w-4 animate-spin rounded-full border-2 border-current border-t-transparent"
            />
          ) : icon ? (
            <span aria-hidden="true" className="inline-flex">
              {icon}
            </span>
          ) : null}
          <span>{children}</span>
          {iconAfter ? (
            <span aria-hidden="true" className="inline-flex">
              {iconAfter}
            </span>
          ) : null}
        </>
      )}
    </Comp>
  );
}

export interface IconButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'children'> {
  /** The accessible name; an icon alone names nothing. */
  label: string;
  icon: ReactNode;
  variant?: Exclude<ButtonVariant, 'primary'>;
  size?: 'sm' | 'md';
  loading?: boolean;
}

/** IconButton is a square button with an icon and a required accessible name. */
export function IconButton({
  label,
  icon,
  variant = 'ghost',
  size = 'md',
  className,
  type = 'button',
  ...props
}: IconButtonProps) {
  return (
    <button
      type={type}
      aria-label={label}
      title={props.title ?? label}
      className={cn(
        buttonBaseClasses,
        buttonVariantClasses[variant],
        size === 'sm' ? 'h-8 w-8' : 'h-9 w-9',
        className,
      )}
      {...props}
    >
      <span aria-hidden="true" className="inline-flex">
        {icon}
      </span>
    </button>
  );
}
