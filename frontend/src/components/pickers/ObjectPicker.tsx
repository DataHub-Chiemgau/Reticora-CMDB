import { useEffect, useId, useRef, useState } from 'react';
import type { KeyboardEvent as ReactKeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
  ApiError,
  PROBLEM_ENTITLEMENT_LIMIT,
  PROBLEM_FEATURE_NOT_ENTITLED,
  type CI,
} from '../../api/client';
import { useAssetList, useCIList, useConsumableList } from '../../api/hooks';
import { useEntitlements, useModules } from '../../hooks/useEntitlements';
import { CIFormModal } from '../../pages/CIFormModal';
import { Plus, Search, X } from 'lucide-react';
import { Button, IconButton } from '../ui/Button';
import { fieldClasses, labelClasses } from '../ui/Input';
import { Select } from '../ui/Select';
import { cn } from '../ui/utils';

/** PICKER_LIMIT is the page size of a picker search (UI-15, WP-318). */
export const PICKER_LIMIT = 20;
/** PICKER_DEBOUNCE_MS is the typing pause after which a picker searches. */
export const PICKER_DEBOUNCE_MS = 300;

/** useDebouncedValue follows value once it rested for delay milliseconds. */
export function useDebouncedValue<T>(value: T, delay = PICKER_DEBOUNCE_MS): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delay);
    return () => clearTimeout(timer);
  }, [value, delay]);
  return debounced;
}

/**
 * accessErrorMessage words a failed request for pickers and dialogs: a 403
 * of a feature outside the plan or a reached limit (ENT-06) as a package or
 * limit message, any other 403 as a missing right (forbidden), else the
 * detail of the problem.
 */
export function accessErrorMessage(error: unknown, t: TFunction, forbidden?: string): string {
  if (error instanceof ApiError) {
    if (error.status === 403) {
      if (error.type === PROBLEM_FEATURE_NOT_ENTITLED) return t('pickers.errors.notEntitled');
      if (error.type === PROBLEM_ENTITLEMENT_LIMIT) return t('pickers.errors.limit');
      return forbidden ?? t('pickers.errors.forbidden');
    }
    return error.detail ?? error.message;
  }
  return error instanceof Error ? error.message : String(error);
}

/** PickerOption is one hit of a picker: kind, name and code, never an id. */
export interface PickerOption {
  key: string;
  kindLabel: string;
  name: string;
  code?: string;
}

interface PickerComboboxProps {
  label: string;
  query: string;
  onQueryChange: (query: string) => void;
  options: PickerOption[];
  onSelect: (index: number) => void;
  /** searching: a search for the current input is running. */
  searching: boolean;
  /** searched: a search for the current input has answered. */
  searched: boolean;
  /** notice replaces the empty list, e.g. a missing right. */
  notice?: string;
  required?: boolean;
}

/**
 * PickerCombobox is the search field of the pickers. It follows the
 * combobox pattern of the CommandPalette (UI-10, WP-192): role=combobox
 * with aria-expanded, aria-controls and aria-activedescendant; the arrow
 * keys move the active option, Enter takes it, Escape closes the list, and
 * the focus stays in the input.
 */
export function PickerCombobox({
  label,
  query,
  onQueryChange,
  options,
  onSelect,
  searching,
  searched,
  notice,
  required,
}: PickerComboboxProps) {
  const { t } = useTranslation();
  const inputId = useId();
  const listboxId = `${inputId}-listbox`;
  const inputRef = useRef<HTMLInputElement>(null);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);

  const expanded = open && options.length > 0;
  const activeIndex = active < options.length ? active : 0;
  const optionId = (index: number) => `${listboxId}-option-${index}`;

  // Escape closes only the list while it is open: the listener runs in the
  // capture phase of window, before a surrounding dialog (Radix listens on
  // the document) would close.
  useEffect(() => {
    if (!expanded) return undefined;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && event.target === inputRef.current) {
        event.stopPropagation();
        setOpen(false);
      }
    };
    window.addEventListener('keydown', onKey, true);
    return () => window.removeEventListener('keydown', onKey, true);
  }, [expanded]);

  function choose(index: number) {
    if (index < 0 || index >= options.length) return;
    onSelect(index);
    setOpen(false);
    setActive(0);
  }

  function handleKeyDown(event: ReactKeyboardEvent<HTMLInputElement>) {
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      if (!expanded) {
        setOpen(true);
        setActive(0);
        return;
      }
      setActive((activeIndex + 1) % options.length);
      return;
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault();
      if (!expanded) {
        setOpen(true);
        setActive(Math.max(options.length - 1, 0));
        return;
      }
      setActive(activeIndex === 0 ? options.length - 1 : activeIndex - 1);
      return;
    }
    if ((event.key === 'Home' || event.key === 'End') && expanded) {
      event.preventDefault();
      setActive(event.key === 'Home' ? 0 : options.length - 1);
      return;
    }
    if (event.key === 'Enter') {
      if (expanded) {
        event.preventDefault();
        choose(activeIndex);
      }
    }
  }

  let status: string | undefined;
  if (open && query.trim() !== '') {
    if (notice) status = notice;
    else if (searching) status = t('pickers.searching');
    else if (searched && options.length === 0) status = t('pickers.noHits');
  }

  return (
    <div className="relative space-y-1.5">
      <label htmlFor={inputId} className={labelClasses}>
        {label}
      </label>
      <div className="relative">
        <Search
          aria-hidden="true"
          className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-fg-subtle"
        />
        <input
          ref={inputRef}
          id={inputId}
          type="text"
          role="combobox"
          autoComplete="off"
          aria-autocomplete="list"
          aria-expanded={expanded}
          aria-controls={listboxId}
          aria-activedescendant={expanded ? optionId(activeIndex) : undefined}
          aria-required={required || undefined}
          value={query}
          placeholder={t('pickers.searchPlaceholder')}
          onChange={(event) => {
            onQueryChange(event.target.value);
            setOpen(true);
            setActive(0);
          }}
          onFocus={() => setOpen(true)}
          onBlur={() => setOpen(false)}
          onKeyDown={handleKeyDown}
          className={cn(fieldClasses, 'pl-9')}
        />
      </div>
      {/* Without options the container carries no listbox role (an empty
          listbox has no required children). */}
      <ul
        id={listboxId}
        role={expanded ? 'listbox' : undefined}
        aria-label={expanded ? label : undefined}
        hidden={!expanded}
        className="absolute z-10 mt-1 max-h-64 w-full overflow-y-auto rounded-lg border border-line bg-raised p-1 shadow-lg"
      >
        {expanded
          ? options.map((option, index) => (
              <li
                key={option.key}
                id={optionId(index)}
                role="option"
                aria-selected={index === activeIndex}
                // Keeps the focus in the input while clicking an option.
                onMouseDown={(event) => event.preventDefault()}
                onMouseEnter={() => setActive(index)}
                onClick={() => choose(index)}
                className={cn(
                  'flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm',
                  index === activeIndex ? 'bg-accent-soft text-accent-strong' : 'text-fg',
                )}
              >
                <span className="text-xs text-fg-muted">{option.kindLabel}</span>
                <span className="font-medium">{option.name}</span>
                {option.code ? (
                  <span className="font-mono text-xs text-fg-muted">{option.code}</span>
                ) : null}
              </li>
            ))
          : null}
      </ul>
      {status ? (
        <p role="status" className="text-sm text-fg-muted">
          {status}
        </p>
      ) : null}
    </div>
  );
}

export type ObjectKind = 'ci' | 'asset' | 'consumable';

/** PickedObject is a chosen CI, asset or consumable with what the UI shows of it. */
export interface PickedObject {
  kind: ObjectKind;
  id: string;
  name: string;
  /** code is the inventory number of an asset or the SKU of a consumable. */
  code?: string;
}

/**
 * useObjectKinds keeps the kinds the release and the plan offer: assets
 * need the module inventory and the feature assets_basic, consumables the
 * module stocktake and the feature stock (WP-214, WP-244, E-44); CIs are
 * always offered.
 */
export function useObjectKinds(kinds: readonly ObjectKind[]): ObjectKind[] {
  const { isModuleEnabled } = useModules();
  const { isEnabled } = useEntitlements();
  return kinds.filter((kind) => {
    if (kind === 'asset') return isModuleEnabled('inventory') && isEnabled('assets_basic');
    if (kind === 'consumable') return isModuleEnabled('stocktake') && isEnabled('stock');
    return true;
  });
}

export interface ObjectPickerProps {
  label: string;
  /** kinds are the object kinds offered; gated kinds drop out (useObjectKinds). */
  kinds: readonly ObjectKind[];
  /** value holds the chosen objects; without multiple at most one. */
  value: PickedObject[];
  onChange: (value: PickedObject[]) => void;
  multiple?: boolean;
  /** ciTypeIds limit the CIs to these types (filter[ci_type_id][in]). */
  ciTypeIds?: readonly string[];
  /** allowCreate offers "Create new" for CIs (CIFormModal). */
  allowCreate?: boolean;
  required?: boolean;
}

/**
 * ObjectPicker chooses a CI, an asset or a consumable by name instead of an
 * id (UI-15, WP-318). It searches the scoped type lists with limit 20 after
 * a typing pause: GET /cis, /assets and /consumables with search; withScope
 * adds client and site of the scope switcher. GET /search stays with the
 * command palette, it is not narrowed by client and site.
 */
export function ObjectPicker({
  label,
  kinds,
  value,
  onChange,
  multiple = false,
  ciTypeIds,
  allowCreate = false,
  required,
}: ObjectPickerProps) {
  const { t } = useTranslation();
  const available = useObjectKinds(kinds);
  const [chosenKind, setChosenKind] = useState<ObjectKind | undefined>(available[0]);
  const kind = chosenKind && available.includes(chosenKind) ? chosenKind : available[0];
  const [query, setQuery] = useState('');
  const debounced = useDebouncedValue(query.trim());
  const [creating, setCreating] = useState(false);

  // One request per typing pause: no search while the input still moves.
  const pending = query.trim() !== debounced;
  const enabled = (k: ObjectKind) => kind === k && debounced !== '' && !pending;
  const ciTypes = ciTypeIds && ciTypeIds.length > 0 ? ciTypeIds.join(',') : undefined;
  const cis = useCIList(
    { search: debounced, limit: PICKER_LIMIT, 'filter[ci_type_id][in]': ciTypes },
    { enabled: enabled('ci') },
  );
  const assets = useAssetList(
    { search: debounced, limit: PICKER_LIMIT },
    { enabled: enabled('asset') },
  );
  const consumables = useConsumableList(
    { search: debounced, limit: PICKER_LIMIT },
    { enabled: enabled('consumable') },
  );

  const kindLabel = (k: ObjectKind) => t(`pickers.kinds.${k}`);
  const search = kind === 'asset' ? assets : kind === 'consumable' ? consumables : cis;
  let hits: PickedObject[] = [];
  if (kind === 'ci') {
    hits = (cis.data?.data ?? []).map((ci) => ({
      kind: 'ci',
      id: ci.id,
      name: ci.name,
      code: ci.asset?.asset_tag ?? ci.serial_number,
    }));
  } else if (kind === 'asset') {
    hits = (assets.data?.data ?? []).map((asset) => ({
      kind: 'asset',
      id: asset.id,
      name: asset.name,
      code: asset.inventory_no ?? asset.asset_tag,
    }));
  } else if (kind === 'consumable') {
    hits = (consumables.data?.data ?? []).map((item) => ({
      kind: 'consumable',
      id: item.id,
      name: item.name,
      code: item.sku,
    }));
  }
  const taken = new Set(value.map((item) => `${item.kind}:${item.id}`));
  hits = hits.filter((hit) => !taken.has(`${hit.kind}:${hit.id}`));

  function pick(item: PickedObject) {
    onChange(multiple ? [...value, item] : [item]);
    setQuery('');
  }

  function created(ci: CI) {
    pick({ kind: 'ci', id: ci.id, name: ci.name, code: ci.serial_number });
  }

  if (!kind) return null;

  return (
    <div className="space-y-2">
      {available.length > 1 ? (
        <Select
          label={t('pickers.kind')}
          value={kind}
          onChange={(event) => {
            setChosenKind(event.target.value as ObjectKind);
            setQuery('');
          }}
          options={available.map((k) => ({ value: k, label: kindLabel(k) }))}
        />
      ) : null}
      <PickerCombobox
        label={label}
        query={query}
        onQueryChange={setQuery}
        required={required && value.length === 0}
        options={hits.map((hit) => ({
          key: `${hit.kind}:${hit.id}`,
          kindLabel: kindLabel(hit.kind),
          name: hit.name,
          code: hit.code,
        }))}
        onSelect={(index) => {
          const hit = hits[index];
          if (hit) pick(hit);
        }}
        searching={pending || search.isFetching}
        searched={!pending && search.isFetched}
        notice={
          search.error
            ? accessErrorMessage(search.error, t, t('pickers.errors.forbiddenSearch'))
            : undefined
        }
      />
      {value.length > 0 ? (
        <ul aria-label={t('pickers.selected')} className="flex flex-wrap gap-2">
          {value.map((item) => (
            <li
              key={`${item.kind}:${item.id}`}
              className="flex items-center gap-2 rounded-lg border border-line bg-panel py-0.5 pl-3 pr-1 text-sm text-fg"
            >
              <span className="text-xs text-fg-muted">{kindLabel(item.kind)}</span>
              <span className="font-medium">{item.name}</span>
              {item.code ? (
                <span className="font-mono text-xs text-fg-muted">{item.code}</span>
              ) : null}
              <IconButton
                size="sm"
                icon={<X />}
                label={t('pickers.remove', { name: item.name })}
                onClick={() => onChange(value.filter((other) => other !== item))}
                className="h-6 w-6"
              />
            </li>
          ))}
        </ul>
      ) : null}
      {allowCreate && kind === 'ci' ? (
        <>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            icon={<Plus />}
            onClick={() => setCreating(true)}
          >
            {t('pickers.createCI')}
          </Button>
          <CIFormModal open={creating} onOpenChange={setCreating} onSuccess={created} />
        </>
      ) : null}
    </div>
  );
}
