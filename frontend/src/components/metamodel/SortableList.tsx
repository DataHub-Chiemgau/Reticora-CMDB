/**
 * SortableList is the ordered list of the metamodel administration (E-54,
 * WP-797): drag and drop with @dnd-kit/core and @dnd-kit/sortable, with the
 * keyboard through the KeyboardSensor (Space picks an entry up, the arrow
 * keys move it, Space drops it, Escape cancels) and the buttons "up" and
 * "down" of each entry. Screen readers hear the dnd-kit announcements and
 * instructions in the UI language (WP-192, WP-193, WP-194). The layout
 * editor of WP-316 builds on it.
 */
import { useId, useRef, useState, type CSSProperties, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type Announcements,
  type DragEndEvent,
  type UniqueIdentifier,
} from '@dnd-kit/core';
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable';
import { ArrowDown, ArrowUp, GripVertical } from 'lucide-react';

export interface SortableListProps<T> {
  items: T[];
  getId: (item: T) => string;
  /** The name of an entry in the announcements and button labels. */
  getName: (item: T) => string;
  renderItem: (item: T, index: number) => ReactNode;
  /** The new order and the entry that moved. */
  onReorder: (next: T[], moved: T) => void;
  /** A read-only list: no handles, no buttons. */
  disabled?: boolean;
  /** The accessible name of the list. */
  label: string;
  className?: string;
}

const iconButton =
  'inline-flex h-7 w-7 items-center justify-center rounded text-gray-600 hover:bg-gray-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary disabled:opacity-40 dark:text-gray-300 dark:hover:bg-gray-800';

export function SortableList<T>({
  items,
  getId,
  getName,
  renderItem,
  onReorder,
  disabled = false,
  label,
  className,
}: SortableListProps<T>) {
  const { t } = useTranslation();
  const contextId = useId();
  const [status, setStatus] = useState('');
  // The entry the dragged one is over: dnd-kit reports the start position as
  // an "over" at once, which would replace the pick-up announcement.
  const lastOver = useRef<UniqueIdentifier | null>(null);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  const ids = items.map(getId);
  const total = items.length;
  const nameOf = (id: UniqueIdentifier) => {
    const item = items.find((i) => getId(i) === String(id));
    return item ? getName(item) : String(id);
  };
  const positionOf = (id: UniqueIdentifier) => ids.indexOf(String(id)) + 1;

  const announcements: Announcements = {
    onDragStart: ({ active }) => {
      lastOver.current = active.id;
      return t('citypes.sets.sortable.pickedUp', {
        name: nameOf(active.id),
        position: positionOf(active.id),
        total,
      });
    },
    onDragOver: ({ active, over }) => {
      const overId = over?.id ?? null;
      if (overId === lastOver.current) return undefined;
      lastOver.current = overId;
      return over
        ? t('citypes.sets.sortable.movedOver', {
            name: nameOf(active.id),
            position: positionOf(over.id),
            total,
          })
        : t('citypes.sets.sortable.outside', { name: nameOf(active.id) });
    },
    onDragEnd: ({ active, over }) =>
      over
        ? t('citypes.sets.sortable.dropped', {
            name: nameOf(active.id),
            position: positionOf(over.id),
            total,
          })
        : t('citypes.sets.sortable.droppedOutside', { name: nameOf(active.id) }),
    onDragCancel: ({ active }) =>
      t('citypes.sets.sortable.cancelled', {
        name: nameOf(active.id),
        position: positionOf(active.id),
        total,
      }),
  };

  const move = (from: number, to: number) => {
    if (from === to || to < 0 || to >= total) return;
    const moved = items[from]!;
    onReorder(arrayMove(items, from, to), moved);
    setStatus(
      t('citypes.sets.sortable.dropped', { name: getName(moved), position: to + 1, total }),
    );
  };

  const onDragEnd = ({ active, over }: DragEndEvent) => {
    if (!over) return;
    move(ids.indexOf(String(active.id)), ids.indexOf(String(over.id)));
  };

  if (disabled) {
    return (
      <ul aria-label={label} className={className}>
        {items.map((item, index) => (
          <li key={getId(item)} className="py-2">
            {renderItem(item, index)}
          </li>
        ))}
      </ul>
    );
  }

  return (
    <DndContext
      id={contextId}
      sensors={sensors}
      collisionDetection={closestCenter}
      onDragEnd={onDragEnd}
      accessibility={{
        announcements,
        screenReaderInstructions: { draggable: t('citypes.sets.sortable.instructions') },
      }}
    >
      <SortableContext items={ids} strategy={verticalListSortingStrategy}>
        <ul aria-label={label} className={className}>
          {items.map((item, index) => (
            <SortableEntry
              key={getId(item)}
              id={getId(item)}
              index={index}
              name={getName(item)}
              first={index === 0}
              last={index === total - 1}
              onUp={() => move(index, index - 1)}
              onDown={() => move(index, index + 1)}
            >
              {renderItem(item, index)}
            </SortableEntry>
          ))}
        </ul>
      </SortableContext>
      <div role="status" aria-live="polite" className="sr-only">
        {status}
      </div>
    </DndContext>
  );
}

function SortableEntry({
  id,
  index,
  name,
  first,
  last,
  onUp,
  onDown,
  children,
}: {
  id: string;
  index: number;
  name: string;
  first: boolean;
  last: boolean;
  onUp: () => void;
  onDown: () => void;
  children: ReactNode;
}) {
  const { t } = useTranslation();
  const {
    attributes,
    listeners,
    setNodeRef,
    setActivatorNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id });
  const style: CSSProperties = {
    transform: transform ? `translate3d(${transform.x}px, ${transform.y}px, 0)` : undefined,
    transition,
  };
  return (
    <li
      ref={setNodeRef}
      style={style}
      data-sortable-index={index}
      className={`flex items-start gap-2 py-2 ${isDragging ? 'relative z-10 rounded-lg bg-gray-50 shadow dark:bg-gray-800' : ''}`}
    >
      <button
        type="button"
        ref={setActivatorNodeRef}
        className={`${iconButton} cursor-grab`}
        {...attributes}
        {...listeners}
        aria-label={t('citypes.sets.sortable.handle', { name })}
      >
        <GripVertical aria-hidden="true" className="h-4 w-4" />
      </button>
      <div className="min-w-0 flex-1">{children}</div>
      <div className="flex gap-1">
        <button
          type="button"
          className={iconButton}
          disabled={first}
          onClick={onUp}
          aria-label={t('citypes.sets.sortable.up', { name })}
        >
          <ArrowUp aria-hidden="true" className="h-4 w-4" />
        </button>
        <button
          type="button"
          className={iconButton}
          disabled={last}
          onClick={onDown}
          aria-label={t('citypes.sets.sortable.down', { name })}
        >
          <ArrowDown aria-hidden="true" className="h-4 w-4" />
        </button>
      </div>
    </li>
  );
}
