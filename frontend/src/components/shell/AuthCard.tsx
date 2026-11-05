import type { ReactNode } from 'react';
import { BrandMark } from './Brand';
import { cn } from '../ui/utils';

/**
 * AuthCard is the panel of the pages before sign-in: the reticle, the one
 * h1, a sentence of context and the content. It sits in the upper third of
 * the page, where the eye lands.
 */
export function AuthCard({
  title,
  description,
  children,
  className,
}: {
  title: ReactNode;
  description?: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <section className={cn('mx-auto w-full max-w-md pt-[6vh]', className)}>
      <div className="rounded-xl border border-line bg-panel p-8 shadow-md">
        <BrandMark className="mb-5 h-8 w-8 text-accent" />
        <h1 className="text-2xl font-semibold text-fg">{title}</h1>
        {description ? <p className="mt-2 text-sm text-fg-muted">{description}</p> : null}
        {children}
      </div>
    </section>
  );
}
