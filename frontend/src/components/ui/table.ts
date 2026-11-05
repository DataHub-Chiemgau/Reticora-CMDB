/**
 * Class sets of the data tables. Every hand-written <table> in a page uses
 * them, so density, header, hover and focus look the same everywhere:
 *
 *   <Card className="p-0 overflow-hidden">
 *     <ScrollRegion label={…}>
 *       <table className={tableClasses}>
 *         <thead className={theadClasses}><tr><th scope="col" className={thClasses}>…
 *         <tbody><tr className={trClasses}><td className={tdClasses}>…
 *
 * A clickable row (opens a detail) uses trInteractiveClasses; the first
 * cell of a row (its name) uses tdPrimaryClasses.
 */
export const tableClasses = 'w-full text-left text-sm';

export const theadClasses = 'border-b border-line bg-canvas/60';

export const thClasses = 'px-4 py-2.5 text-left font-medium';

export const tdClasses = 'border-b border-line px-4 py-3 align-middle text-fg-muted';

export const tdPrimaryClasses = 'border-b border-line px-4 py-3 align-middle font-medium text-fg';

export const trClasses = 'transition-colors hover:bg-fg/[0.025]';

export const trInteractiveClasses =
  'cursor-pointer transition-colors hover:bg-fg/[0.04] focus-visible:bg-fg/[0.04] focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent';

/** Numeric and identifier cells: figures line up, identifiers are monospace. */
export const tdNumericClasses = `${tdClasses} text-right tabular-nums`;
export const tdMonoClasses = `${tdClasses} font-mono text-xs text-fg`;
