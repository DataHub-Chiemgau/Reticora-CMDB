/**
 * TicketDetailPage shows one ticket (UI-14, WP-814): the head with number,
 * title, status, priority, category and due date; editing over PATCH
 * /tickets/{id}; the status changes of the state machine (TKT-01, WP-290);
 * the assignment to a person or a team; the object links (WP-764); the
 * comments with internal and external ones and the system comments
 * (WP-291); the attachments (WP-765) through LinkedDocuments and the SLA
 * state (WP-292) with Ticketing Standard only (E-39). The server decides
 * every request: a refused status change (409) reads as a conflict notice
 * and reloads the ticket, a 422 with a field appears at that field.
 */
import { useState } from 'react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Link, useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Link2, Lock, MessageSquare, Pencil, Plus, X } from 'lucide-react';
import {
  ApiError,
  assetApi,
  type Ticket,
  type TicketComment,
  type TicketLink,
  type TicketLinkTarget,
  type TicketSLA,
  type TicketStatus,
  type TicketUpdateRequest,
} from '../api/client';
import {
  fetchTicketAttachments,
  useAddTicketComment,
  useAddTicketLink,
  useCI,
  useHasPermission,
  useRemoveTicketLink,
  useTicket,
  useTicketAttachments,
  useTicketComments,
  useTicketLinks,
  useTicketSLA,
  useUpdateTicket,
} from '../api/hooks';
import { LinkedDocuments } from '../components/documents/LinkedDocuments';
import { formatDateTime, useLocationNames } from '../components/cmdb/CIDetailSections';
import { LocationPicker, type PickedLocation } from '../components/pickers/LocationPicker';
import { ObjectPicker, type PickedObject } from '../components/pickers/ObjectPicker';
import { UserTeamPicker, type PickedHolder } from '../components/pickers/UserTeamPicker';
import { Badge } from '../components/ui/Badge';
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { Modal } from '../components/ui/Modal';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';
import { SkeletonList } from '../components/ui/Skeleton';
import { Textarea } from '../components/ui/Textarea';
import {
  TICKET_CATEGORIES,
  TICKET_PRIORITIES,
  priorityVariant,
  statusVariant,
  ticketErrorMessage,
  useTicketingStandard,
} from './TicketListPage';

/**
 * TICKET_TRANSITIONS mirrors the state machine of
 * backend/internal/ticket/states.go (WP-290): the states each state may
 * change to. resolved and closed reopen to open. The server stays the
 * authority; a refused change answers 409.
 */
export const TICKET_TRANSITIONS: Record<TicketStatus, readonly TicketStatus[]> = {
  new: ['open', 'in_progress', 'closed'],
  open: ['in_progress', 'waiting', 'resolved', 'closed'],
  in_progress: ['open', 'waiting', 'resolved'],
  waiting: ['in_progress', 'resolved'],
  resolved: ['closed', 'open'],
  closed: ['open'],
};

/** isReopen tells whether a change reopens a resolved or closed ticket. */
export function isReopen(from: TicketStatus, to: TicketStatus): boolean {
  return to === 'open' && (from === 'resolved' || from === 'closed');
}

/** The link targets this page creates; contract and review item links are only shown. */
type LinkKind = 'object' | 'location';

/**
 * fieldError returns the message of a refused request at one field: the
 * field of a FieldProblem or a violation that names it (422).
 */
export function fieldError(error: unknown, ...fields: string[]): string | undefined {
  if (!(error instanceof ApiError)) return undefined;
  if (error.field && fields.includes(error.field)) return ticketErrorMessage(error);
  const violation = error.violations.find((v) => fields.includes(v.field));
  return violation?.detail;
}

/**
 * formatRemaining words the remaining business minutes of an SLA due time
 * (WP-292): null once responded or resolved, negative once overdue.
 */
export function formatRemaining(minutes: number | null | undefined, t: TFunction): string {
  if (minutes === null) return t('ticket.sla.met');
  if (minutes === undefined) return '—';
  const total = Math.abs(minutes);
  const duration = t('ticket.sla.duration', {
    hours: Math.floor(total / 60),
    minutes: total % 60,
  });
  return minutes < 0
    ? t('ticket.sla.overdue', { duration })
    : t('ticket.sla.remaining', { duration });
}

export function TicketDetailPage() {
  const { t } = useTranslation();
  const { id = '' } = useParams();
  const ticket = useTicket(id);

  if (ticket.isLoading) return <SkeletonList rows={4} label={t('app.loading')} />;
  if (ticket.error || !ticket.data) {
    const notFound = ticket.error instanceof ApiError && ticket.error.status === 404;
    return (
      <ErrorState
        title={notFound ? t('ticket.detail.notFound') : t('app.error')}
        description={ticket.error && !notFound ? ticketErrorMessage(ticket.error) : undefined}
        retryLabel={t('common.retry')}
        onRetry={() => void ticket.refetch()}
      />
    );
  }
  return <TicketDetail ticket={ticket.data} onReload={() => void ticket.refetch()} />;
}

function TicketDetail({ ticket, onReload }: { ticket: Ticket; onReload: () => void }) {
  const { t, i18n } = useTranslation();
  const standard = useTicketingStandard();
  const canUpload = useHasPermission('document:write') === true;
  const attachments = useTicketAttachments(ticket.id);
  const [editing, setEditing] = useState(false);
  const label = `#${ticket.ticket_number}`;

  return (
    <div className="space-y-4">
      <PageHeader
        title={
          <>
            <span className="mr-2 font-mono text-fg-muted">{label}</span>
            {ticket.title}
          </>
        }
        breadcrumbs={[{ label: t('ticket.title'), to: '/tickets' }, { label }]}
        meta={
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant={statusVariant(ticket.status)}>
              {t(`ticket.statuses.${ticket.status}`, ticket.status)}
            </Badge>
            <Badge variant={priorityVariant(ticket.priority)}>
              {t(`ticket.priorities.${ticket.priority}`, ticket.priority)}
            </Badge>
            <Badge variant="neutral">
              {t(`ticket.categories.${ticket.category}`, ticket.category)}
            </Badge>
            <span className="text-sm text-fg-muted">
              {t('ticket.detail.dueDate')}:{' '}
              {formatDateTime(ticket.due_date, i18n.language) ?? t('ticket.detail.noDueDate')}
            </span>
          </div>
        }
        actions={
          <Button variant="secondary" icon={<Pencil />} onClick={() => setEditing(true)}>
            {t('ticket.detail.edit')}
          </Button>
        }
      />
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <DetailsCard ticket={ticket} />
        <div className="space-y-4">
          <WorkflowCard ticket={ticket} onReload={onReload} />
          <AssignmentCard ticket={ticket} />
        </div>
      </div>
      {standard ? <SLACard ticketId={ticket.id} /> : null}
      <LinksCard ticket={ticket} />
      <CommentsCard ticketId={ticket.id} />
      <LinkedDocuments
        entityType="ticket"
        entityId={ticket.id}
        load={() => fetchTicketAttachments(ticket.id)}
        title={t('ticket.attachments.title', { count: attachments.data?.length ?? 0 })}
        canWrite={canUpload}
      />
      <Modal open={editing} onOpenChange={setEditing} title={t('ticket.detail.edit')}>
        <EditTicketForm ticket={ticket} onDone={() => setEditing(false)} />
      </Modal>
    </div>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-fg-muted">{label}</dt>
      <dd className="min-w-0 text-fg [overflow-wrap:anywhere]">{children}</dd>
    </>
  );
}

function DetailsCard({ ticket }: { ticket: Ticket }) {
  const { t, i18n } = useTranslation();
  return (
    <Card title={t('ticket.detail.details')}>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
        <Field label={t('common.description')}>
          {ticket.description ? (
            <span className="whitespace-pre-wrap">{ticket.description}</span>
          ) : (
            <span className="text-fg-muted">{t('ticket.detail.noDescription')}</span>
          )}
        </Field>
        <Field label={t('ticket.relatedObject')}>
          {ticket.related_ci_id ? (
            <CIName id={ticket.related_ci_id} />
          ) : ticket.related_asset_id ? (
            <AssetName id={ticket.related_asset_id} />
          ) : (
            <span className="text-fg-muted">{t('ticket.detail.noRelated')}</span>
          )}
        </Field>
        <Field label={t('ticket.createdAt')}>
          {formatDateTime(ticket.created_at, i18n.language)}
        </Field>
        <Field label={t('ticket.detail.updatedAt')}>
          {formatDateTime(ticket.updated_at, i18n.language)}
        </Field>
        {ticket.resolved_at ? (
          <Field label={t('ticket.detail.resolvedAt')}>
            {formatDateTime(ticket.resolved_at, i18n.language)}
          </Field>
        ) : null}
        {ticket.closed_at ? (
          <Field label={t('ticket.detail.closedAt')}>
            {formatDateTime(ticket.closed_at, i18n.language)}
          </Field>
        ) : null}
      </dl>
    </Card>
  );
}

function WorkflowCard({ ticket, onReload }: { ticket: Ticket; onReload: () => void }) {
  const { t } = useTranslation();
  const update = useUpdateTicket();
  const next = TICKET_TRANSITIONS[ticket.status] ?? [];
  const conflict =
    update.error instanceof ApiError && update.error.status === 409 ? update.error : undefined;

  return (
    <Card title={t('ticket.workflow.title')}>
      <p className="mb-3 text-sm text-fg-muted">
        {t('ticket.workflow.current')}{' '}
        <Badge variant={statusVariant(ticket.status)}>
          {t(`ticket.statuses.${ticket.status}`, ticket.status)}
        </Badge>
      </p>
      {next.length > 0 ? (
        <div className="flex flex-wrap gap-2" role="group" aria-label={t('ticket.workflow.title')}>
          {next.map((to) => (
            <Button
              key={to}
              size="sm"
              variant="secondary"
              loading={update.isPending && update.variables?.data.status === to}
              disabled={update.isPending}
              onClick={() =>
                update.mutate(
                  { id: ticket.id, data: { status: to } },
                  {
                    onError: (error) => {
                      if (error instanceof ApiError && error.status === 409) onReload();
                    },
                  },
                )
              }
            >
              {isReopen(ticket.status, to)
                ? t('ticket.workflow.reopen')
                : t('ticket.workflow.moveTo', {
                    status: t(`ticket.statuses.${to}`, to),
                  })}
            </Button>
          ))}
        </div>
      ) : (
        <p className="text-sm text-fg-muted">{t('ticket.workflow.none')}</p>
      )}
      {conflict ? (
        <p
          role="alert"
          className="mt-3 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-fg"
        >
          {t('ticket.workflow.conflict', { detail: ticketErrorMessage(conflict) })}
        </p>
      ) : update.error ? (
        <p role="alert" className="mt-3 text-sm text-danger">
          {ticketErrorMessage(update.error)}
        </p>
      ) : null}
    </Card>
  );
}

function AssignmentCard({ ticket }: { ticket: Ticket }) {
  const { t } = useTranslation();
  const update = useUpdateTicket();
  const [holder, setHolder] = useState<PickedHolder | null>(null);
  // The names of the holders this session chose; the ticket names only ids.
  const [names, setNames] = useState<Record<string, string>>({});
  const error = fieldError(update.error, 'assignee_id', 'team_id');

  function assign() {
    if (!holder) return;
    const data: TicketUpdateRequest =
      holder.kind === 'user' ? { assignee_id: holder.id } : { team_id: holder.id };
    update.mutate(
      { id: ticket.id, data },
      {
        onSuccess: () => {
          setNames((prev) => ({ ...prev, [holder.id]: holder.name }));
          setHolder(null);
        },
      },
    );
  }

  const holderName = (id: string) => names[id] ?? id;

  return (
    <Card title={t('ticket.assign.title')}>
      <dl className="mb-3 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
        <Field label={t('ticket.assign.user')}>
          {ticket.assignee_id ? (
            <span className="font-mono text-xs">{holderName(ticket.assignee_id)}</span>
          ) : (
            <span className="text-fg-muted">{t('ticket.assign.none')}</span>
          )}
        </Field>
        <Field label={t('ticket.assign.team')}>
          {ticket.team_id ? (
            <span className="font-mono text-xs">{holderName(ticket.team_id)}</span>
          ) : (
            <span className="text-fg-muted">{t('ticket.assign.none')}</span>
          )}
        </Field>
      </dl>
      <form
        className="space-y-2"
        onSubmit={(event) => {
          event.preventDefault();
          assign();
        }}
      >
        <UserTeamPicker
          label={t('ticket.assignment')}
          value={holder}
          onChange={(value) => {
            update.reset();
            setHolder(value);
          }}
        />
        {error ? (
          <p role="alert" className="text-sm text-danger">
            {error}
          </p>
        ) : update.error ? (
          <p role="alert" className="text-sm text-danger">
            {ticketErrorMessage(update.error)}
          </p>
        ) : null}
        <div className="flex justify-end">
          <Button type="submit" size="sm" disabled={!holder} loading={update.isPending}>
            {t('ticket.assign.submit')}
          </Button>
        </div>
      </form>
    </Card>
  );
}

function EditTicketForm({ ticket, onDone }: { ticket: Ticket; onDone: () => void }) {
  const { t } = useTranslation();
  const update = useUpdateTicket();
  const [form, setForm] = useState({
    title: ticket.title,
    description: ticket.description ?? '',
    priority: ticket.priority,
    category: ticket.category,
    due_date: ticket.due_date ? toLocalInput(ticket.due_date) : '',
  });
  const err = update.error;

  function save() {
    const data: TicketUpdateRequest = {};
    if (form.title !== ticket.title) data.title = form.title;
    if (form.description !== (ticket.description ?? '')) data.description = form.description;
    if (form.priority !== ticket.priority) data.priority = form.priority;
    if (form.category !== ticket.category) data.category = form.category;
    const due = form.due_date ? new Date(form.due_date).toISOString() : '';
    if (form.due_date !== (ticket.due_date ? toLocalInput(ticket.due_date) : '') && due) {
      data.due_date = due;
    }
    if (Object.keys(data).length === 0) {
      onDone();
      return;
    }
    update.mutate({ id: ticket.id, data }, { onSuccess: onDone });
  }

  const unplaced =
    err &&
    !fieldError(err, 'title', 'description', 'priority', 'category', 'due_date') &&
    ticketErrorMessage(err);

  return (
    <form
      className="space-y-4"
      onSubmit={(event) => {
        event.preventDefault();
        save();
      }}
    >
      <Input
        label={t('ticket.ticketTitle')}
        value={form.title}
        required
        error={fieldError(err, 'title')}
        onChange={(e) => setForm({ ...form, title: e.target.value })}
      />
      <Textarea
        label={t('common.description')}
        rows={4}
        value={form.description}
        error={fieldError(err, 'description')}
        onChange={(e) => setForm({ ...form, description: e.target.value })}
      />
      <Select
        label={t('ticket.priority')}
        value={form.priority}
        error={fieldError(err, 'priority')}
        onChange={(e) => setForm({ ...form, priority: e.target.value })}
        options={TICKET_PRIORITIES.map((value) => ({
          value,
          label: t(`ticket.priorities.${value}`, value),
        }))}
      />
      <Select
        label={t('ticket.category')}
        value={form.category}
        error={fieldError(err, 'category')}
        onChange={(e) => setForm({ ...form, category: e.target.value })}
        options={TICKET_CATEGORIES.map((value) => ({
          value,
          label: t(`ticket.categories.${value}`, value),
        }))}
      />
      <Input
        type="datetime-local"
        label={t('ticket.detail.dueDate')}
        value={form.due_date}
        error={fieldError(err, 'due_date')}
        onChange={(e) => setForm({ ...form, due_date: e.target.value })}
      />
      {unplaced ? (
        <p role="alert" className="text-sm text-danger">
          {unplaced}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={onDone}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" loading={update.isPending} disabled={!form.title}>
          {t('common.save')}
        </Button>
      </div>
    </form>
  );
}

/** toLocalInput turns an ISO time into the value of a datetime-local input. */
function toLocalInput(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

function CIName({ id }: { id: string }) {
  const ci = useCI(id);
  return (
    <Link to={`/cmdb/${id}`} className="text-accent hover:underline">
      {ci.data?.name ?? id}
    </Link>
  );
}

function AssetName({ id }: { id: string }) {
  const asset = useQuery({ queryKey: ['assets', 'detail', id], queryFn: () => assetApi.get(id) });
  return <>{asset.data ? asset.data.name : id}</>;
}

/** LinkTargetName names a linked object: a CI links to its page, a location shows its path. */
function LinkTargetName({
  link,
  locationPath,
}: {
  link: TicketLink;
  locationPath: (id?: string) => string | undefined;
}) {
  if (link.entity_type === 'ci') return <CIName id={link.entity_id} />;
  if (link.entity_type === 'asset') return <AssetName id={link.entity_id} />;
  if (link.entity_type === 'location') return <>{locationPath(link.entity_id)}</>;
  return <span className="font-mono text-xs">{link.entity_id}</span>;
}

/** linkErrorMessage words a refused link: 409 a link that exists, 404 an unknown target. */
function linkErrorMessage(error: unknown, t: TFunction): string {
  if (error instanceof ApiError && error.status === 409) return t('ticket.links.duplicate');
  if (error instanceof ApiError && error.status === 404) return t('ticket.links.notFound');
  return ticketErrorMessage(error);
}

function LinksCard({ ticket }: { ticket: Ticket }) {
  const { t } = useTranslation();
  const links = useTicketLinks(ticket.id);
  const remove = useRemoveTicketLink(ticket.id);
  const [adding, setAdding] = useState(false);
  const rows = (links.data?.pages ?? []).flatMap((page) => page.data ?? []);
  const locationNames = useLocationNames(rows.some((link) => link.entity_type === 'location'));

  return (
    <Card
      title={t('ticket.links.title')}
      actions={
        <Button size="sm" icon={<Plus />} onClick={() => setAdding(true)}>
          {t('ticket.links.add')}
        </Button>
      }
    >
      {links.error ? (
        <p role="alert" className="text-sm text-danger">
          {ticketErrorMessage(links.error)}
        </p>
      ) : null}
      {remove.error ? (
        <p role="alert" className="text-sm text-danger">
          {linkErrorMessage(remove.error, t)}
        </p>
      ) : null}
      {links.data && rows.length === 0 ? (
        <p className="text-sm text-fg-muted">{t('ticket.links.empty')}</p>
      ) : null}
      {rows.length > 0 ? (
        <ul aria-label={t('ticket.links.title')} className="divide-y divide-line">
          {rows.map((link) => {
            const type = t(`ticket.links.types.${link.entity_type}`, link.entity_type);
            // Contract and review item links are shown, not changed here.
            const removable =
              link.entity_type === 'ci' ||
              link.entity_type === 'asset' ||
              link.entity_type === 'location';
            return (
              <li key={link.id} className="flex items-center gap-3 py-2">
                <span className="w-28 shrink-0 text-xs text-fg-muted">{type}</span>
                <span className="min-w-0 flex-1 truncate text-sm text-fg">
                  <LinkTargetName link={link} locationPath={locationNames.path} />
                </span>
                {removable ? (
                  <IconButton
                    size="sm"
                    icon={<X />}
                    label={t('ticket.links.remove', { type })}
                    onClick={() => remove.mutate(link.id)}
                  />
                ) : null}
              </li>
            );
          })}
        </ul>
      ) : null}
      {links.hasNextPage ? (
        <div className="flex justify-center pt-2">
          <Button
            size="sm"
            variant="secondary"
            disabled={links.isFetchingNextPage}
            onClick={() => void links.fetchNextPage()}
          >
            {t('ticket.links.more')}
          </Button>
        </div>
      ) : null}
      <Modal open={adding} onOpenChange={setAdding} title={t('ticket.links.add')}>
        <AddLinkForm ticketId={ticket.id} onDone={() => setAdding(false)} />
      </Modal>
    </Card>
  );
}

function AddLinkForm({ ticketId, onDone }: { ticketId: string; onDone: () => void }) {
  const { t } = useTranslation();
  const add = useAddTicketLink(ticketId);
  const [kind, setKind] = useState<LinkKind>('object');
  const [objects, setObjects] = useState<PickedObject[]>([]);
  const [location, setLocation] = useState<PickedLocation | null>(null);

  let target: TicketLinkTarget | undefined;
  const object = objects[0];
  if (kind === 'object' && object && (object.kind === 'ci' || object.kind === 'asset')) {
    target = { entity_type: object.kind, entity_id: object.id };
  }
  if (kind === 'location' && location) target = { entity_type: 'location', entity_id: location.id };

  return (
    <form
      className="space-y-4"
      onSubmit={(event) => {
        event.preventDefault();
        if (!target) return;
        add.mutate(target, { onSuccess: onDone });
      }}
    >
      <Select
        label={t('ticket.links.kind')}
        value={kind}
        onChange={(event) => {
          add.reset();
          setKind(event.target.value as LinkKind);
        }}
        options={[
          { value: 'object', label: t('ticket.links.kinds.object') },
          { value: 'location', label: t('ticket.links.kinds.location') },
        ]}
      />
      {kind === 'object' ? (
        <ObjectPicker
          label={t('ticket.links.object')}
          kinds={['ci', 'asset']}
          value={objects}
          onChange={(value) => {
            add.reset();
            setObjects(value);
          }}
        />
      ) : (
        <LocationPicker
          label={t('ticket.links.types.location')}
          value={location?.id ?? null}
          onChange={(value) => {
            add.reset();
            setLocation(value);
          }}
        />
      )}
      {add.error ? (
        <p role="alert" className="text-sm text-danger">
          {linkErrorMessage(add.error, t)}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={onDone}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" icon={<Link2 />} disabled={!target} loading={add.isPending}>
          {t('ticket.links.submit')}
        </Button>
      </div>
    </form>
  );
}

function CommentsCard({ ticketId }: { ticketId: string }) {
  const { t, i18n } = useTranslation();
  const comments = useTicketComments(ticketId);
  const add = useAddTicketComment(ticketId);
  const [content, setContent] = useState('');
  const [internal, setInternal] = useState(false);
  const rows = (comments.data?.pages ?? []).flatMap((page) => page.data ?? []);

  return (
    <Card title={t('ticket.comments.title')}>
      {comments.error ? (
        <p role="alert" className="text-sm text-danger">
          {ticketErrorMessage(comments.error)}
        </p>
      ) : null}
      {comments.data && rows.length === 0 ? (
        <p className="flex items-center gap-2 text-sm text-fg-muted">
          <MessageSquare aria-hidden="true" className="h-4 w-4" />
          {t('ticket.comments.empty')}
        </p>
      ) : null}
      {rows.length > 0 ? (
        <ol aria-label={t('ticket.comments.title')} className="space-y-3">
          {rows.map((comment) => (
            <CommentItem key={comment.id} comment={comment} language={i18n.language} />
          ))}
        </ol>
      ) : null}
      {comments.hasNextPage ? (
        <div className="flex justify-center pt-2">
          <Button
            size="sm"
            variant="secondary"
            disabled={comments.isFetchingNextPage}
            onClick={() => void comments.fetchNextPage()}
          >
            {t('ticket.comments.more')}
          </Button>
        </div>
      ) : null}
      <form
        className="mt-4 space-y-2 border-t border-line pt-4"
        onSubmit={(event) => {
          event.preventDefault();
          const text = content.trim();
          if (!text) return;
          add.mutate(
            { content: text, is_internal: internal },
            {
              onSuccess: () => {
                setContent('');
              },
            },
          );
        }}
      >
        <Textarea
          label={t('ticket.comments.new')}
          rows={3}
          value={content}
          onChange={(e) => setContent(e.target.value)}
        />
        <div className="flex flex-wrap items-center justify-between gap-2">
          <label className="inline-flex items-center gap-2 text-sm text-fg">
            <input
              type="checkbox"
              checked={internal}
              onChange={(e) => setInternal(e.target.checked)}
            />
            {t('ticket.comments.internalToggle')}
          </label>
          <Button type="submit" size="sm" disabled={!content.trim()} loading={add.isPending}>
            {t('ticket.comments.submit')}
          </Button>
        </div>
        {add.error ? (
          <p role="alert" className="text-sm text-danger">
            {ticketErrorMessage(add.error)}
          </p>
        ) : null}
      </form>
    </Card>
  );
}

function CommentItem({ comment, language }: { comment: TicketComment; language: string }) {
  const { t } = useTranslation();
  const system = comment.author_type === 'system' || !comment.author_id;
  return (
    <li
      className="rounded-lg border border-line bg-panel px-3 py-2"
      data-internal={comment.is_internal ? 'true' : undefined}
    >
      <div className="mb-1 flex flex-wrap items-center gap-2 text-xs text-fg-muted">
        <span className="font-medium text-fg">
          {system ? (
            t('ticket.comments.system')
          ) : (
            <span className="font-mono">{comment.author_id}</span>
          )}
        </span>
        <time dateTime={comment.created_at}>{formatDateTime(comment.created_at, language)}</time>
        {comment.is_internal ? (
          <Badge variant="warning">
            <Lock aria-hidden="true" className="mr-1 inline h-3 w-3" />
            {t('ticket.comments.internal')}
          </Badge>
        ) : (
          <Badge variant="neutral">{t('ticket.comments.external')}</Badge>
        )}
      </div>
      <p className="whitespace-pre-wrap text-sm text-fg [overflow-wrap:anywhere]">
        {comment.content}
      </p>
    </li>
  );
}

function SLACard({ ticketId }: { ticketId: string }) {
  const { t } = useTranslation();
  const sla = useTicketSLA(ticketId);
  const missing = sla.error instanceof ApiError && sla.error.status === 404;

  return (
    <Card title={t('ticket.sla.title')}>
      {sla.isLoading ? <p className="text-sm text-fg-muted">{t('app.loading')}</p> : null}
      {missing ? <p className="text-sm text-fg-muted">{t('ticket.sla.none')}</p> : null}
      {sla.error && !missing ? (
        <p role="alert" className="text-sm text-danger">
          {ticketErrorMessage(sla.error)}
        </p>
      ) : null}
      {sla.data ? <SLAState sla={sla.data} /> : null}
    </Card>
  );
}

function SLAState({ sla }: { sla: TicketSLA }) {
  const { t, i18n } = useTranslation();
  const rows = [
    {
      key: 'response',
      label: t('ticket.sla.response'),
      due: sla.response_due_at,
      remaining: sla.response_remaining_minutes,
      breached: sla.response_breached,
    },
    {
      key: 'resolution',
      label: t('ticket.sla.resolution'),
      due: sla.resolution_due_at,
      remaining: sla.resolution_remaining_minutes,
      breached: sla.resolution_breached,
    },
  ];
  return (
    <div className="space-y-3">
      {sla.paused ? <Badge variant="info">{t('ticket.sla.paused')}</Badge> : null}
      <dl className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        {rows.map((row) => (
          <div key={row.key} className="rounded-lg border border-line px-3 py-2 text-sm">
            <dt className="flex flex-wrap items-center gap-2 font-medium text-fg">
              {row.label}
              {row.breached ? <Badge variant="danger">{t('ticket.sla.breached')}</Badge> : null}
            </dt>
            <dd className="mt-1 text-fg-muted">
              {t('ticket.sla.due')}: {formatDateTime(row.due, i18n.language)}
            </dd>
            <dd className="tabular-nums text-fg">{formatRemaining(row.remaining, t)}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}
