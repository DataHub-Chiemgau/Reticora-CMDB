import { useId, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { CalendarClock, ChevronDown, ChevronRight, Pencil, Plus, Trash2, X } from 'lucide-react';
import {
  ApiError,
  type BusinessCalendar,
  type BusinessCalendarHoliday,
  type BusinessCalendarWindow,
  type CreateBusinessCalendarRequest,
  type UpdateBusinessCalendarRequest,
} from '../../api/client';
import {
  useBusinessCalendarPages,
  useCreateBusinessCalendar,
  useDeleteBusinessCalendar,
  useHasPermission,
  useUpdateBusinessCalendar,
} from '../../api/hooks';
import { useClients } from '../../api/cmdbHooks';
import { Badge } from '../ui/Badge';
import { Button, IconButton } from '../ui/Button';
import { Card } from '../ui/Card';
import { EmptyState } from '../ui/EmptyState';
import { ErrorState } from '../ui/ErrorState';
import { Input, labelClasses } from '../ui/Input';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';
import { SkeletonList } from '../ui/Skeleton';
import {
  tableClasses,
  tdClasses,
  tdPrimaryClasses,
  theadClasses,
  thClasses,
  trClasses,
} from '../ui/table';
import { ScrollRegion } from '../ScrollRegion';

/**
 * WEEKDAYS are the catalog keys of the weekdays (sla.calendar.weekdays.*);
 * WEEKDAY_NUMBERS maps them onto weekday of weekly_hours, 1 (Monday) to 7
 * (Sunday) as in backend/internal/sla/calendar.go.
 */
export const WEEKDAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'] as const;
export type Weekday = (typeof WEEKDAYS)[number];
export const WEEKDAY_NUMBERS: Record<Weekday, number> = {
  mon: 1,
  tue: 2,
  wed: 3,
  thu: 4,
  fri: 5,
  sat: 6,
  sun: 7,
};

/** weekdayKey is the catalog key of weekday 1–7; undefined outside. */
export function weekdayKey(weekday: number): Weekday | undefined {
  return WEEKDAYS[weekday - 1];
}

/**
 * timeZoneSuggestions lists the IANA zones of the browser. The check runs
 * at run time: tsconfig sets lib ES2020, which does not type
 * Intl.supportedValuesOf, and older browsers lack it.
 */
export function timeZoneSuggestions(): string[] {
  const intl = Intl as unknown as { supportedValuesOf?: (key: string) => string[] };
  if (typeof intl.supportedValuesOf !== 'function') return [];
  try {
    return intl.supportedValuesOf('timeZone');
  } catch {
    return [];
  }
}

interface WindowDraft {
  start: string;
  end: string;
}

interface CalendarDraft {
  name: string;
  clientId: string;
  timezone: string;
  windows: Record<Weekday, WindowDraft[]>;
  holidays: BusinessCalendarHoliday[];
  isDefault: boolean;
}

/** The built-in standard of the backend: Monday to Friday, 08:00–18:00. */
function standardWindows(): Record<Weekday, WindowDraft[]> {
  const workday = () => [{ start: '08:00', end: '18:00' }];
  return {
    mon: workday(),
    tue: workday(),
    wed: workday(),
    thu: workday(),
    fri: workday(),
    sat: [],
    sun: [],
  };
}

function emptyDraft(): CalendarDraft {
  return {
    name: '',
    clientId: '',
    timezone: '',
    windows: standardWindows(),
    holidays: [],
    isDefault: false,
  };
}

function draftOf(calendar: BusinessCalendar): CalendarDraft {
  const windows = Object.fromEntries(WEEKDAYS.map((day) => [day, [] as WindowDraft[]])) as Record<
    Weekday,
    WindowDraft[]
  >;
  for (const window of calendar.weekly_hours) {
    const key = weekdayKey(window.weekday);
    if (key) windows[key].push({ start: window.start, end: window.end });
  }
  return {
    name: calendar.name,
    clientId: calendar.client_id ?? '',
    timezone: calendar.timezone ?? '',
    windows,
    holidays: calendar.holidays.map((holiday) => ({ ...holiday })),
    isDefault: calendar.is_default,
  };
}

function weeklyHoursOf(draft: CalendarDraft): BusinessCalendarWindow[] {
  return WEEKDAYS.flatMap((day) =>
    draft.windows[day].map((window) => ({
      weekday: WEEKDAY_NUMBERS[day],
      start: window.start,
      end: window.end,
    })),
  );
}

function holidaysOf(draft: CalendarDraft): BusinessCalendarHoliday[] {
  return draft.holidays
    .filter((holiday) => holiday.date !== '')
    .map((holiday) =>
      holiday.name ? { date: holiday.date, name: holiday.name } : { date: holiday.date },
    );
}

/** createRequest is the POST body; without windows the built-in standard applies. */
function createRequest(draft: CalendarDraft): CreateBusinessCalendarRequest {
  const weekly = weeklyHoursOf(draft);
  return {
    name: draft.name.trim(),
    client_id: draft.clientId || null,
    timezone: draft.timezone.trim() || null,
    ...(weekly.length > 0 ? { weekly_hours: weekly } : {}),
    holidays: holidaysOf(draft),
    is_default: draft.isDefault,
  };
}

/** updateRequest is the PATCH body; client_id is never sent (400 on client_id). */
function updateRequest(draft: CalendarDraft): UpdateBusinessCalendarRequest {
  return {
    name: draft.name.trim(),
    timezone: draft.timezone.trim() || null,
    weekly_hours: weeklyHoursOf(draft),
    holidays: holidaysOf(draft),
    is_default: draft.isDefault,
  };
}

/** summary words the windows of a calendar, e.g. "Mo 08:00–18:00". */
function weeklySummary(calendar: BusinessCalendar, t: TFunction): string {
  return WEEKDAYS.flatMap((day) => {
    const windows = calendar.weekly_hours.filter((w) => w.weekday === WEEKDAY_NUMBERS[day]);
    if (windows.length === 0) return [];
    const times = windows.map((w) => `${w.start}–${w.end}`).join(', ');
    return [`${t(`sla.calendar.weekdaysShort.${day}`)} ${times}`];
  }).join(' · ');
}

interface FormErrors {
  fields: Record<string, string>;
  message?: string;
}

/**
 * formErrors places a refused write: 400 at its field (time zone,
 * overlapping windows, holidays twice), 409 at the default flag; a 403 of
 * an org-wide calendar for a client-scoped principal and anything else as
 * message.
 */
function formErrors(error: unknown, t: TFunction): FormErrors {
  if (!(error instanceof ApiError)) {
    return { fields: {}, message: error instanceof Error ? error.message : String(error) };
  }
  if (error.status === 403) {
    return { fields: {}, message: t('sla.calendar.errors.forbidden') };
  }
  const fields = Object.fromEntries(
    Object.entries(error.fieldErrors()).map(([field, info]) => [field, info.message]),
  );
  // The unique default per organization and client (business_calendar_default).
  if (error.status === 409) fields.is_default = t('sla.calendar.errors.secondDefault');
  const known = ['name', 'client_id', 'timezone', 'weekly_hours', 'holidays', 'is_default'];
  const placed = Object.keys(fields).some((field) => known.includes(field));
  return {
    fields,
    message: placed ? undefined : (error.detail ?? error.title ?? error.message),
  };
}

interface CalendarFormProps {
  calendar?: BusinessCalendar;
  onDone: () => void;
  onCancel: () => void;
}

function CalendarForm({ calendar, onDone, onCancel }: CalendarFormProps) {
  const { t } = useTranslation();
  const zonesId = useId();
  const [draft, setDraft] = useState<CalendarDraft>(() =>
    calendar ? draftOf(calendar) : emptyDraft(),
  );
  const [errors, setErrors] = useState<FormErrors>({ fields: {} });
  const clients = useClients();
  const create = useCreateBusinessCalendar();
  const update = useUpdateBusinessCalendar();
  const zones = useMemo(() => timeZoneSuggestions(), []);
  const busy = create.isPending || update.isPending;

  const clientOptions = [
    { value: '', label: t('sla.calendar.organizationWide') },
    ...(clients.data?.data ?? []).map((client) => ({ value: client.id, label: client.name })),
  ];

  const setWindows = (day: Weekday, windows: WindowDraft[]) =>
    setDraft({ ...draft, windows: { ...draft.windows, [day]: windows } });

  const submit = () => {
    setErrors({ fields: {} });
    const options = {
      onSuccess: onDone,
      onError: (error: unknown) => setErrors(formErrors(error, t)),
    };
    if (calendar) {
      update.mutate({ id: calendar.id, data: updateRequest(draft) }, options);
    } else {
      create.mutate(createRequest(draft), options);
    }
  };

  const defaultId = useId();
  const defaultErrorId = `${defaultId}-error`;

  return (
    <form
      className="space-y-4"
      onSubmit={(event) => {
        event.preventDefault();
        submit();
      }}
    >
      <Input
        label={t('sla.calendar.name')}
        value={draft.name}
        required
        error={errors.fields.name}
        onChange={(event) => setDraft({ ...draft, name: event.target.value })}
      />
      <Select
        label={t('sla.calendar.client')}
        value={draft.clientId}
        options={clientOptions}
        disabled={!!calendar}
        error={errors.fields.client_id}
        onChange={(event) => setDraft({ ...draft, clientId: event.target.value })}
      />
      {calendar ? <p className="text-xs text-fg-muted">{t('sla.calendar.clientFixed')}</p> : null}
      <Input
        label={t('sla.calendar.timezone')}
        value={draft.timezone}
        placeholder={t('sla.calendar.timezonePlaceholder')}
        list={zones.length > 0 ? zonesId : undefined}
        error={errors.fields.timezone}
        onChange={(event) => setDraft({ ...draft, timezone: event.target.value })}
      />
      {zones.length > 0 ? (
        <datalist id={zonesId}>
          {zones.map((zone) => (
            <option key={zone} value={zone} />
          ))}
        </datalist>
      ) : null}
      <p className="text-xs text-fg-muted">{t('sla.calendar.timezoneHint')}</p>

      <fieldset className="space-y-2">
        <legend className={labelClasses}>{t('sla.calendar.weeklyHours')}</legend>
        {WEEKDAYS.map((day) => (
          <div key={day} className="flex flex-wrap items-center gap-2">
            <span className="w-24 text-sm text-fg">{t(`sla.calendar.weekdays.${day}`)}</span>
            {draft.windows[day].map((window, index) => (
              <span key={index} className="flex items-center gap-1">
                <input
                  type="time"
                  aria-label={t('sla.calendar.windowStart', {
                    day: t(`sla.calendar.weekdays.${day}`),
                    n: index + 1,
                  })}
                  className="h-9 rounded-lg border border-control bg-panel px-2 text-sm text-fg"
                  value={window.start}
                  onChange={(event) =>
                    setWindows(
                      day,
                      draft.windows[day].map((w, i) =>
                        i === index ? { ...w, start: event.target.value } : w,
                      ),
                    )
                  }
                />
                <span aria-hidden="true">–</span>
                <input
                  type="time"
                  aria-label={t('sla.calendar.windowEnd', {
                    day: t(`sla.calendar.weekdays.${day}`),
                    n: index + 1,
                  })}
                  className="h-9 rounded-lg border border-control bg-panel px-2 text-sm text-fg"
                  value={window.end}
                  onChange={(event) =>
                    setWindows(
                      day,
                      draft.windows[day].map((w, i) =>
                        i === index ? { ...w, end: event.target.value } : w,
                      ),
                    )
                  }
                />
                <IconButton
                  icon={<X />}
                  label={t('sla.calendar.removeWindow', {
                    day: t(`sla.calendar.weekdays.${day}`),
                    n: index + 1,
                  })}
                  variant="ghost"
                  size="sm"
                  onClick={() =>
                    setWindows(
                      day,
                      draft.windows[day].filter((_, i) => i !== index),
                    )
                  }
                />
              </span>
            ))}
            <Button
              variant="ghost"
              size="sm"
              icon={<Plus />}
              aria-label={t('sla.calendar.addWindowFor', {
                day: t(`sla.calendar.weekdays.${day}`),
              })}
              onClick={() =>
                setWindows(day, [...draft.windows[day], { start: '08:00', end: '18:00' }])
              }
            >
              {t('sla.calendar.addWindow')}
            </Button>
          </div>
        ))}
        {errors.fields.weekly_hours ? (
          <p className="text-sm text-danger" role="alert">
            {errors.fields.weekly_hours}
          </p>
        ) : null}
      </fieldset>

      <fieldset className="space-y-2">
        <legend className={labelClasses}>{t('sla.calendar.holidays')}</legend>
        {draft.holidays.map((holiday, index) => (
          <div key={index} className="flex flex-wrap items-end gap-2">
            <Input
              type="date"
              label={t('sla.calendar.holidayDate', { n: index + 1 })}
              value={holiday.date}
              onChange={(event) =>
                setDraft({
                  ...draft,
                  holidays: draft.holidays.map((h, i) =>
                    i === index ? { ...h, date: event.target.value } : h,
                  ),
                })
              }
            />
            <Input
              label={t('sla.calendar.holidayName', { n: index + 1 })}
              value={holiday.name ?? ''}
              onChange={(event) =>
                setDraft({
                  ...draft,
                  holidays: draft.holidays.map((h, i) =>
                    i === index ? { ...h, name: event.target.value } : h,
                  ),
                })
              }
            />
            <IconButton
              icon={<Trash2 />}
              label={t('sla.calendar.removeHoliday', { n: index + 1 })}
              variant="ghost"
              size="sm"
              onClick={() =>
                setDraft({ ...draft, holidays: draft.holidays.filter((_, i) => i !== index) })
              }
            />
          </div>
        ))}
        <Button
          variant="ghost"
          size="sm"
          icon={<Plus />}
          onClick={() =>
            setDraft({ ...draft, holidays: [...draft.holidays, { date: '', name: '' }] })
          }
        >
          {t('sla.calendar.addHoliday')}
        </Button>
        {errors.fields.holidays ? (
          <p className="text-sm text-danger" role="alert">
            {errors.fields.holidays}
          </p>
        ) : null}
      </fieldset>

      <div className="space-y-1">
        <label htmlFor={defaultId} className="flex items-center gap-2 text-sm text-fg">
          <input
            id={defaultId}
            type="checkbox"
            checked={draft.isDefault}
            aria-invalid={errors.fields.is_default ? true : undefined}
            aria-describedby={errors.fields.is_default ? defaultErrorId : undefined}
            onChange={(event) => setDraft({ ...draft, isDefault: event.target.checked })}
          />
          {t('sla.calendar.isDefault')}
        </label>
        {errors.fields.is_default ? (
          <p id={defaultErrorId} className="text-sm text-danger">
            {errors.fields.is_default}
          </p>
        ) : null}
      </div>

      {errors.message ? (
        <p className="text-sm text-danger" role="alert">
          {errors.message}
        </p>
      ) : null}

      <div className="flex justify-end gap-2">
        <Button variant="secondary" onClick={onCancel}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" loading={busy} disabled={!draft.name.trim() || busy}>
          {t('common.save')}
        </Button>
      </div>
    </form>
  );
}

/**
 * BusinessCalendarSection maintains the business calendars of the
 * organization and its clients on the SLA page (TKT-02, E-38, WP-768,
 * WP-815): a list page by page over next_cursor, creation, editing and
 * deletion. The client is chosen only at creation (PATCH sends no
 * client_id); writes need sla:write, the server decides each request.
 */
export function BusinessCalendarSection() {
  const { t } = useTranslation();
  const contentId = useId();
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<BusinessCalendar | 'new' | undefined>(undefined);
  const [deleting, setDeleting] = useState<BusinessCalendar | undefined>(undefined);
  const [deleteError, setDeleteError] = useState<string | undefined>(undefined);
  const canWrite = useHasPermission('sla:write') === true;
  const pages = useBusinessCalendarPages();
  const clients = useClients(open);
  const remove = useDeleteBusinessCalendar();

  const calendars = pages.data?.pages.flatMap((page) => page.data) ?? [];
  const total = pages.data?.pages[0]?.total ?? 0;
  const clientName = (id: string | null) =>
    id
      ? (clients.data?.data.find((client) => client.id === id)?.name ?? id)
      : t('sla.calendar.organizationWide');

  return (
    <section className="space-y-3" aria-labelledby="sla-calendars">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id="sla-calendars" className="text-base font-semibold text-fg">
          <button
            type="button"
            className="inline-flex items-center gap-1"
            aria-expanded={open}
            aria-controls={contentId}
            onClick={() => setOpen(!open)}
          >
            {open ? (
              <ChevronDown aria-hidden="true" className="h-4 w-4" />
            ) : (
              <ChevronRight aria-hidden="true" className="h-4 w-4" />
            )}
            {t('sla.calendar.title')} <span className="tabular-nums text-fg-muted">({total})</span>
          </button>
        </h2>
        {open && canWrite ? (
          <Button size="sm" icon={<Plus />} onClick={() => setEditing('new')}>
            {t('sla.calendar.create')}
          </Button>
        ) : null}
      </div>

      {open ? (
        <div id={contentId} className="space-y-3">
          <p className="text-sm text-fg-muted">{t('sla.calendar.hint')}</p>
          {pages.isLoading ? (
            <SkeletonList rows={3} label={t('sla.calendar.loading')} />
          ) : pages.isError ? (
            <ErrorState
              title={t('sla.calendar.errorTitle')}
              description={pages.error.message}
              retryLabel={t('common.retry')}
              onRetry={() => void pages.refetch()}
            />
          ) : calendars.length === 0 ? (
            <EmptyState
              icon={<CalendarClock />}
              title={t('sla.calendar.emptyTitle')}
              description={t('sla.calendar.emptyHint')}
            />
          ) : (
            <Card className="overflow-hidden p-0">
              <ScrollRegion label={t('sla.calendar.title')}>
                <table className={tableClasses}>
                  <thead className={theadClasses}>
                    <tr>
                      <th scope="col" className={thClasses}>
                        {t('sla.calendar.name')}
                      </th>
                      <th scope="col" className={thClasses}>
                        {t('sla.calendar.client')}
                      </th>
                      <th scope="col" className={thClasses}>
                        {t('sla.calendar.timezone')}
                      </th>
                      <th scope="col" className={thClasses}>
                        {t('sla.calendar.weeklyHours')}
                      </th>
                      <th scope="col" className={thClasses}>
                        {t('sla.calendar.holidays')}
                      </th>
                      {canWrite ? (
                        <th scope="col" className={thClasses}>
                          <span className="sr-only">{t('common.actions')}</span>
                        </th>
                      ) : null}
                    </tr>
                  </thead>
                  <tbody>
                    {calendars.map((calendar) => (
                      <tr key={calendar.id} className={trClasses}>
                        <td className={tdPrimaryClasses}>
                          {calendar.name}{' '}
                          {calendar.is_default ? (
                            <Badge variant="info">{t('sla.calendar.default')}</Badge>
                          ) : null}
                        </td>
                        <td className={tdClasses}>{clientName(calendar.client_id)}</td>
                        <td className={tdClasses}>
                          {calendar.timezone ?? t('sla.calendar.timezoneDerived')}
                        </td>
                        <td className={tdClasses}>{weeklySummary(calendar, t)}</td>
                        <td className={tdClasses}>
                          {calendar.holidays.length > 0
                            ? calendar.holidays
                                .map((holiday) =>
                                  holiday.name ? `${holiday.date} ${holiday.name}` : holiday.date,
                                )
                                .join(', ')
                            : '—'}
                        </td>
                        {canWrite ? (
                          <td className={`${tdClasses} whitespace-nowrap text-right`}>
                            <IconButton
                              icon={<Pencil />}
                              label={t('sla.calendar.edit', { name: calendar.name })}
                              variant="ghost"
                              size="sm"
                              onClick={() => setEditing(calendar)}
                            />
                            <IconButton
                              icon={<Trash2 />}
                              label={t('sla.calendar.delete', { name: calendar.name })}
                              variant="ghost"
                              size="sm"
                              onClick={() => {
                                setDeleteError(undefined);
                                setDeleting(calendar);
                              }}
                            />
                          </td>
                        ) : null}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </ScrollRegion>
            </Card>
          )}
          {pages.hasNextPage ? (
            <Button
              variant="secondary"
              size="sm"
              loading={pages.isFetchingNextPage}
              onClick={() => void pages.fetchNextPage()}
            >
              {t('sla.calendar.more')}
            </Button>
          ) : null}
        </div>
      ) : null}

      <Modal
        open={editing !== undefined}
        onOpenChange={(next) => {
          if (!next) setEditing(undefined);
        }}
        title={editing === 'new' ? t('sla.calendar.create') : t('sla.calendar.editTitle')}
        size="lg"
      >
        {editing !== undefined ? (
          <CalendarForm
            key={editing === 'new' ? 'new' : editing.id}
            calendar={editing === 'new' ? undefined : editing}
            onDone={() => setEditing(undefined)}
            onCancel={() => setEditing(undefined)}
          />
        ) : null}
      </Modal>

      <Modal
        open={deleting !== undefined}
        onOpenChange={(next) => {
          if (!next) setDeleting(undefined);
        }}
        title={t('sla.calendar.deleteTitle')}
      >
        <div className="space-y-4">
          <p className="text-sm text-fg">
            {t('sla.calendar.deleteConfirm', { name: deleting?.name ?? '' })}
          </p>
          {deleteError ? (
            <p className="text-sm text-danger" role="alert">
              {deleteError}
            </p>
          ) : null}
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setDeleting(undefined)}>
              {t('common.cancel')}
            </Button>
            <Button
              variant="danger"
              loading={remove.isPending}
              onClick={() => {
                if (!deleting) return;
                remove.mutate(deleting, {
                  onSuccess: () => setDeleting(undefined),
                  onError: (error) => setDeleteError(formErrors(error, t).message ?? error.message),
                });
              }}
            >
              {t('common.delete')}
            </Button>
          </div>
        </div>
      </Modal>
    </section>
  );
}
