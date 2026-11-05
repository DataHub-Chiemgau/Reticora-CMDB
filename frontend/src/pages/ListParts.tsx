import type { ReactNode } from 'react';
import { EmptyState } from '../components/ui/EmptyState';

interface TableEmptyRowProps {
  /** Number of columns of the table, so the cell spans the full width. */
  colSpan: number;
  icon: ReactNode;
  title: string;
  description: string;
}

/**
 * TableEmptyRow is the empty state of a table that keeps its header: one
 * cell across all columns. The content sticks to the left edge of the
 * scroll region, so on a narrow screen it is centred in what is visible and
 * not in the full width of a wide table.
 */
export function TableEmptyRow({ colSpan, icon, title, description }: TableEmptyRowProps) {
  return (
    <tr>
      <td colSpan={colSpan} className="p-0">
        <div className="sticky left-0 w-full max-w-[calc(100vw-3.25rem)]">
          <EmptyState
            icon={icon}
            title={title}
            description={description}
            className="rounded-none border-0"
          />
        </div>
      </td>
    </tr>
  );
}
