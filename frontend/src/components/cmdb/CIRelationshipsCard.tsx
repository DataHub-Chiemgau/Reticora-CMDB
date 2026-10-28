/**
 * CIRelationshipsCard — the manage surface for a CI's relationships (spec §13:
 * manual relationships can be created, edited, verified and removed).
 *
 * The backend has always exposed POST/DELETE and now also PATCH for
 * verification metadata, but the UI rendered a read-only list, so discovered
 * edges could never be confirmed or disputed and manual edges could not be
 * created at all. This card exposes the full lifecycle and shows the
 * provenance that makes an edge trustworthy: where it came from and whether a
 * human has verified it.
 *
 * With a client or site chosen in the scope control (E-87 option 1), the card
 * lists only the edges whose other CI lies in that scope and names how many
 * it hides; a toggle shows them all. The scope is a view filter: the server
 * decides which edges and CIs the user may see.
 *
 * "Entfernen und unterdrücken" of a discovery edge asks for confirmation and
 * a reason first; the suppressions of the CI, with reason, actor and time,
 * are listed on demand and can be lifted, so discovery creates the edge
 * again (F-273).
 */
import { useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useQueries } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { ApiError, ciApi, verificationStates } from '../../api/client';
import type { Relationship, RelationshipSuppression } from '../../api/client';
import {
  useCIRelationships,
  useCreateRelationship,
  useDeleteRelationship,
  useLiftSuppression,
  useRelationshipSuppressions,
  useUpdateRelationship,
} from '../../api/hooks';
import { useScopeStore } from '../../stores/scope';
import { useLocationNames } from './CIDetailSections';
import { inScope } from '../ScopeBanner';
import { Badge } from '../ui/Badge';
import { Button } from '../ui/Button';
import { Card } from '../ui/Card';
import { EmptyState } from '../ui/EmptyState';
import { ErrorState } from '../ui/ErrorState';
import { Input } from '../ui/Input';
import { Modal } from '../ui/Modal';
import { Select } from '../ui/Select';
import { SkeletonList } from '../ui/Skeleton';

/**
 * Relationship types offered when creating an edge by hand. The structural
 * types (contains, mounted_in, located_in and their inverses) are not
 * offered: they are projected from the CI's parent, rack mount and location
 * (DB-05) and POST /relationships refuses them with 422 (F-242).
 */
const relTypeOptions = [
  'connected_to',
  'hosted_on',
  'depends_on',
  'member_of',
  'powers',
  'powered_by',
  'runs_on',
  'uplink_to',
  'managed_by',
  'uses',
];

/**
 * Verification state drives the badge colour so an unreviewed or contradicted
 * edge is visually distinct from a confirmed one.
 */
export function verificationBadgeVariant(
  state: string | undefined,
): 'success' | 'warning' | 'danger' | 'neutral' {
  switch (state) {
    case 'verified':
      return 'success';
    case 'disputed':
      return 'danger';
    case 'stale':
      return 'warning';
    default:
      return 'neutral';
  }
}

export function CIRelationshipsCard({ ciId }: { ciId: string }) {
  const { t } = useTranslation();
  const [showCreate, setShowCreate] = useState(false);
  const [editing, setEditing] = useState<Relationship | null>(null);
  // The discovery edge whose removal awaits confirmation (F-273).
  const [suppressing, setSuppressing] = useState<Relationship | null>(null);
  const [showSuppressions, setShowSuppressions] = useState(false);

  const relationshipsQuery = useCIRelationships(ciId);
  const createRelationship = useCreateRelationship(ciId, {
    onCreated: () => setShowCreate(false),
  });
  const updateRelationship = useUpdateRelationship(ciId);
  const deleteRelationship = useDeleteRelationship(ciId);
  // The confirmed removal of a discovery edge; its error shows in the dialog.
  const suppressRelationship = useDeleteRelationship(ciId);
  // One create at a time (F-314): a submit while a create is pending is
  // dropped. The ref holds before the pending state has rendered and
  // disabled the button, e.g. for two clicks in one task.
  const creating = useRef(false);

  // A failed create is shown in its dialog.
  const mutationError = (updateRelationship.error || deleteRelationship.error) as Error | null;

  const allRelationships = relationshipsQuery.data?.data ?? [];
  // An edge to a location (located_in) names the node by its path in the
  // location tree, which is read only when there is such an edge (F-237).
  const locations = useLocationNames(allRelationships.some((r) => !!r.target_location_id));

  // Scope filter (E-87): the other CI of each edge is read (the same query as
  // its detail page) to learn its client and site. Edges to a location and
  // edges whose other CI cannot be read stay listed: they are not known to
  // lie outside.
  const scope = useScopeStore();
  const scoped = !!(scope.clientId || scope.siteId);
  const [showAll, setShowAll] = useState(false);
  const otherIds = scoped
    ? [
        ...new Set(
          allRelationships
            .filter((r) => !r.target_location_id)
            .map((r) => (r.source_ci_id === ciId ? r.target_ci_id : r.source_ci_id))
            .filter((id) => id && id !== ciId),
        ),
      ]
    : [];
  const others = useQueries({
    queries: otherIds.map((id) => ({
      queryKey: ['ci', id],
      queryFn: () => ciApi.get(id),
      retry: false,
    })),
  });
  const othersPending = others.some((q) => q.isPending && q.fetchStatus !== 'idle');
  const outsideIds = new Set(
    others.flatMap((q, i) => (q.data && !inScope(scope, q.data) ? [otherIds[i]] : [])),
  );
  const isOutside = (relationship: Relationship) =>
    outsideIds.has(
      relationship.source_ci_id === ciId ? relationship.target_ci_id : relationship.source_ci_id,
    );
  const hiddenCount = allRelationships.filter(isOutside).length;
  const relationships = showAll ? allRelationships : allRelationships.filter((r) => !isOutside(r));

  return (
    <Card title={t('ci.relationships')}>
      <div className="mb-3 flex justify-end">
        <Button
          size="sm"
          variant="secondary"
          onClick={() => {
            createRelationship.reset();
            setShowCreate(true);
          }}
        >
          + {t('relationship.add', 'Beziehung hinzufügen')}
        </Button>
      </div>

      {mutationError ? (
        <p role="alert" className="mb-3 text-sm text-red-600 dark:text-red-400">
          {mutationError.message}
        </p>
      ) : null}

      {hiddenCount > 0 ? (
        <p className="mb-3 flex flex-wrap items-center gap-2 text-xs text-gray-600 dark:text-gray-300">
          <span>{t('relationshipCard.hiddenOutside', { count: hiddenCount })}</span>
          <button
            type="button"
            className="text-primary hover:underline"
            onClick={() => setShowAll((v) => !v)}
          >
            {showAll ? t('relationshipCard.showInScope') : t('relationshipCard.showAll')}
          </button>
        </p>
      ) : null}

      {relationshipsQuery.isLoading || othersPending ? (
        <SkeletonList rows={3} label={t('app.loading')} />
      ) : relationshipsQuery.error ? (
        <ErrorState
          title={t('ci.relationshipsLoadError')}
          retryLabel={t('common.retry')}
          onRetry={() => void relationshipsQuery.refetch()}
        />
      ) : relationships.length === 0 ? (
        <EmptyState title={t('ci.noRelationships')} />
      ) : (
        <ul className="space-y-2 text-sm" aria-label={t('ci.relationships')}>
          {relationships.map((relationship) => {
            const isOutgoing = relationship.source_ci_id === ciId;
            const otherId = isOutgoing ? relationship.target_ci_id : relationship.source_ci_id;
            const state = relationship.verification_state ?? 'unverified';
            return (
              <li
                key={relationship.id}
                className="flex flex-wrap items-center justify-between gap-2 border-b border-gray-100 pb-2 last:border-0 dark:border-gray-800"
              >
                <span className="flex flex-wrap items-center gap-2">
                  <Badge variant="neutral">{relationship.rel_type}</Badge>
                  <span aria-hidden="true">{isOutgoing ? '→' : '←'}</span>
                  {relationship.target_location_id ? (
                    <span className="text-xs">
                      {locations.path(relationship.target_location_id)}
                    </span>
                  ) : (
                    <Link
                      to={`/cmdb/${otherId}`}
                      className="font-mono text-xs text-primary underline-offset-2 hover:underline"
                    >
                      {otherId}
                    </Link>
                  )}
                  <Badge variant={verificationBadgeVariant(state)}>
                    {t(`relationship.state.${state}`, state)}
                  </Badge>
                  {isOutside(relationship) ? (
                    <Badge variant="warning">{t('relationshipCard.outsideScope')}</Badge>
                  ) : null}
                </span>
                <span className="flex items-center gap-2">
                  <span className="text-xs text-gray-500 dark:text-gray-400">
                    {relationship.source_system || relationship.source}
                  </span>
                  {relationship.projected ? (
                    <Badge variant="info">{t('relationship.projected')}</Badge>
                  ) : null}
                  {!relationship.projected && state !== 'verified' ? (
                    <button
                      className="text-xs text-primary hover:underline"
                      disabled={updateRelationship.isPending}
                      onClick={() =>
                        updateRelationship.mutate({
                          id: relationship.id,
                          data: { verification_state: 'verified' },
                        })
                      }
                    >
                      {t('relationship.verify', 'Bestätigen')}
                    </button>
                  ) : null}
                  {relationship.projected ? null : (
                    <>
                      <button
                        className="text-xs text-primary hover:underline"
                        onClick={() => setEditing(relationship)}
                      >
                        {t('common.edit', 'Bearbeiten')}
                      </button>
                      <button
                        className="text-xs text-red-600 hover:underline dark:text-red-400"
                        disabled={deleteRelationship.isPending}
                        title={
                          relationship.source === 'discovery'
                            ? t('relationshipCard.suppressHint')
                            : undefined
                        }
                        onClick={() => {
                          if (relationship.source === 'discovery') {
                            // A suppression lasts: confirm with a reason first.
                            suppressRelationship.reset();
                            setSuppressing(relationship);
                            return;
                          }
                          deleteRelationship.mutate({ id: relationship.id });
                        }}
                      >
                        {relationship.source === 'discovery'
                          ? t('relationshipCard.suppress')
                          : t('common.delete', 'Löschen')}
                      </button>
                    </>
                  )}
                </span>
              </li>
            );
          })}
        </ul>
      )}

      <div className="mt-4 border-t border-gray-100 pt-3 dark:border-gray-800">
        <button
          type="button"
          className="text-xs text-primary hover:underline"
          aria-expanded={showSuppressions}
          onClick={() => setShowSuppressions((v) => !v)}
        >
          {showSuppressions
            ? t('relationshipCard.hideSuppressions')
            : t('relationshipCard.showSuppressions')}
        </button>
        {showSuppressions ? <SuppressionList ciId={ciId} /> : null}
      </div>

      <SuppressRelationshipModal
        relationship={suppressing}
        ciId={ciId}
        onClose={() => setSuppressing(null)}
        pending={suppressRelationship.isPending}
        error={suppressRelationship.error as Error | null}
        onSubmit={(id, reason) =>
          suppressRelationship.mutate(
            { id, force: true, reason },
            { onSuccess: () => setSuppressing(null) },
          )
        }
      />

      <CreateRelationshipModal
        open={showCreate}
        onOpenChange={setShowCreate}
        ciId={ciId}
        pending={createRelationship.isPending}
        error={createRelationship.error as Error | null}
        onSubmit={(data) => {
          if (creating.current) return;
          creating.current = true;
          createRelationship.mutate(data, {
            onSettled: () => {
              creating.current = false;
            },
          });
        }}
      />

      <EditRelationshipModal
        relationship={editing}
        onClose={() => setEditing(null)}
        pending={updateRelationship.isPending}
        error={updateRelationship.error as Error | null}
        onSubmit={(id, data, done) => updateRelationship.mutate({ id, data }, { onSuccess: done })}
      />
    </Card>
  );
}

/**
 * SuppressionList shows the suppressions of the CI's removed discovery
 * edges with reason, actor and time; "Aufheben" lifts one, so discovery
 * creates the edge again in its next cycle (F-273).
 */
function SuppressionList({ ciId }: { ciId: string }) {
  const { t, i18n } = useTranslation();
  const suppressionsQuery = useRelationshipSuppressions(ciId);
  const liftSuppression = useLiftSuppression(ciId);
  const suppressions: RelationshipSuppression[] = suppressionsQuery.data?.data ?? [];

  if (suppressionsQuery.isLoading) {
    return <SkeletonList rows={2} label={t('app.loading')} />;
  }
  if (suppressionsQuery.error) {
    return (
      <ErrorState
        title={t('relationshipCard.suppressionsLoadError')}
        retryLabel={t('common.retry')}
        onRetry={() => void suppressionsQuery.refetch()}
      />
    );
  }
  return (
    <div className="mt-2">
      {liftSuppression.error ? (
        <p role="alert" className="mb-2 text-sm text-red-600 dark:text-red-400">
          {(liftSuppression.error as Error).message}
        </p>
      ) : null}
      {suppressions.length === 0 ? (
        <p className="text-xs text-gray-500 dark:text-gray-400">
          {t('relationshipCard.noSuppressions')}
        </p>
      ) : (
        <ul className="space-y-2 text-sm" aria-label={t('relationshipCard.suppressions')}>
          {suppressions.map((suppression) => {
            const isOutgoing = suppression.source_ci_id === ciId;
            const otherId = isOutgoing ? suppression.target_ci_id : suppression.source_ci_id;
            return (
              <li
                key={suppression.id}
                className="flex flex-wrap items-start justify-between gap-2 border-b border-gray-100 pb-2 last:border-0 dark:border-gray-800"
              >
                <span className="flex flex-col gap-1">
                  <span className="flex flex-wrap items-center gap-2">
                    <Badge variant="neutral">{suppression.rel_type}</Badge>
                    <span aria-hidden="true">{isOutgoing ? '→' : '←'}</span>
                    <Link
                      to={`/cmdb/${otherId}`}
                      className="font-mono text-xs text-primary underline-offset-2 hover:underline"
                    >
                      {otherId}
                    </Link>
                  </span>
                  <span className="text-xs text-gray-700 dark:text-gray-200">
                    {suppression.reason
                      ? t('relationshipCard.suppressionReason', { reason: suppression.reason })
                      : t('relationshipCard.noReason')}
                  </span>
                  <span className="text-xs text-gray-500 dark:text-gray-400">
                    {t('relationshipCard.suppressedBy', {
                      actor: suppression.created_by ?? t('relationshipCard.unknownActor'),
                      date: new Date(suppression.created_at).toLocaleString(i18n.language),
                    })}
                  </span>
                </span>
                <button
                  type="button"
                  className="text-xs text-primary hover:underline"
                  title={t('relationshipCard.liftHint')}
                  disabled={liftSuppression.isPending}
                  onClick={() => liftSuppression.mutate(suppression.id)}
                >
                  {t('relationshipCard.lift')}
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

interface SuppressModalProps {
  relationship: Relationship | null;
  ciId: string;
  onClose: () => void;
  pending: boolean;
  error: Error | null;
  onSubmit: (id: string, reason: string) => void;
}

/**
 * SuppressRelationshipModal confirms "Entfernen und unterdrücken" of a
 * discovery edge with a mandatory reason; nothing is deleted before the
 * confirmation (F-273).
 */
function SuppressRelationshipModal({
  relationship,
  ciId,
  onClose,
  pending,
  error,
  onSubmit,
}: SuppressModalProps) {
  const { t } = useTranslation();
  const [reason, setReason] = useState('');
  // The reason starts empty for every edge the dialog opens for.
  const [shownId, setShownId] = useState<string | null>(null);
  const currentId = relationship?.id ?? null;
  if (currentId !== shownId) {
    setShownId(currentId);
    setReason('');
  }

  const violations = error instanceof ApiError ? error.violationsByField() : {};
  const trimmed = reason.trim();
  const other = relationship
    ? relationship.source_ci_id === ciId
      ? relationship.target_ci_id
      : relationship.source_ci_id
    : '';

  return (
    <Modal
      open={!!relationship}
      onOpenChange={(o) => !o && onClose()}
      title={t('relationshipCard.suppressTitle')}
      description={
        relationship
          ? t('relationshipCard.suppressConfirm', { relType: relationship.rel_type, other })
          : undefined
      }
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!relationship || !trimmed || pending) return;
          onSubmit(relationship.id, trimmed);
        }}
      >
        <Input
          label={t('relationshipCard.suppressReason')}
          value={reason}
          required
          maxLength={1000}
          onChange={(e) => setReason(e.target.value)}
          error={violations.reason ? t('relationshipCard.suppressReasonInvalid') : undefined}
        />
        {error && Object.keys(violations).length === 0 ? (
          <p role="alert" className="text-sm text-red-600 dark:text-red-400">
            {error.message}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel', 'Abbrechen')}
          </Button>
          <Button type="submit" variant="danger" loading={pending} disabled={!trimmed}>
            {t('relationshipCard.suppress')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

interface CreateModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  ciId: string;
  pending: boolean;
  error: Error | null;
  onSubmit: (data: {
    source_ci_id: string;
    target_ci_id: string;
    rel_type: string;
    source: string;
    verification_state: string;
    notes?: string;
  }) => void;
}

function CreateRelationshipModal({
  open,
  onOpenChange,
  ciId,
  pending,
  error,
  onSubmit,
}: CreateModalProps) {
  const { t } = useTranslation();
  const [targetId, setTargetId] = useState('');
  const [relType, setRelType] = useState<string>(relTypeOptions[0] ?? 'connected_to');
  const [direction, setDirection] = useState<'outgoing' | 'incoming'>('outgoing');
  const [notes, setNotes] = useState('');
  // The form starts empty whenever the dialog opens.
  const [shown, setShown] = useState(open);
  if (open !== shown) {
    setShown(open);
    if (open) {
      setTargetId('');
      setNotes('');
    }
  }

  const violations = error instanceof ApiError ? error.violationsByField() : {};
  // An endpoint the caller cannot see, foreign or unknown, is refused with
  // 422 on source_ci_id or target_ci_id (F-145); the dialog names it at the
  // CI ID it asks for, in the UI language.
  const endpointNotFound =
    violations.source_ci_id || violations.target_ci_id
      ? t('relationship.ciNotFound', 'Kein CI mit dieser ID gefunden.')
      : undefined;

  return (
    <Modal
      open={open}
      onOpenChange={onOpenChange}
      title={t('relationship.add', 'Beziehung hinzufügen')}
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          const trimmed = targetId.trim();
          if (!trimmed) return;
          onSubmit({
            // A relationship created here is a human assertion, so it is
            // recorded as manual and pre-verified: the operator entering it
            // is the verification.
            source_ci_id: direction === 'outgoing' ? ciId : trimmed,
            target_ci_id: direction === 'outgoing' ? trimmed : ciId,
            rel_type: relType,
            source: 'manual',
            verification_state: 'verified',
            notes: notes.trim() || undefined,
          });
        }}
      >
        <Select
          label={t('relationship.direction', 'Richtung')}
          value={direction}
          onChange={(e) => setDirection(e.target.value as 'outgoing' | 'incoming')}
          options={[
            { value: 'outgoing', label: t('relationship.outgoing', 'Dieses CI → Ziel') },
            { value: 'incoming', label: t('relationship.incoming', 'Quelle → dieses CI') },
          ]}
        />
        <Select
          label={t('relationship.type', 'Beziehungstyp')}
          value={relType}
          onChange={(e) => setRelType(e.target.value)}
          options={relTypeOptions.map((value) => ({ value, label: value }))}
          error={violations.rel_type}
        />
        <Input
          label={t('relationship.targetCi', 'CI-ID der Gegenstelle')}
          value={targetId}
          required
          onChange={(e) => setTargetId(e.target.value)}
          error={endpointNotFound}
        />
        <Input
          label={t('relationship.notes', 'Notiz')}
          value={notes}
          onChange={(e) => setNotes(e.target.value)}
          error={violations.notes}
        />
        {error && Object.keys(violations).length === 0 ? (
          <p role="alert" className="text-sm text-red-600 dark:text-red-400">
            {error.message}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={() => onOpenChange(false)}>
            {t('common.cancel', 'Abbrechen')}
          </Button>
          <Button type="submit" loading={pending}>
            {t('common.create', 'Anlegen')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

interface EditModalProps {
  relationship: Relationship | null;
  onClose: () => void;
  pending: boolean;
  error: Error | null;
  onSubmit: (
    id: string,
    data: { verification_state: string; notes?: string },
    done: () => void,
  ) => void;
}

function EditRelationshipModal({
  relationship,
  onClose,
  pending,
  error,
  onSubmit,
}: EditModalProps) {
  const { t } = useTranslation();
  const [state, setState] = useState('unverified');
  const [notes, setNotes] = useState('');
  // Re-seed the form whenever a different relationship is opened.
  const [seededId, setSeededId] = useState<string | null>(null);
  if (relationship && relationship.id !== seededId) {
    setSeededId(relationship.id);
    setState(relationship.verification_state ?? 'unverified');
    setNotes(relationship.notes ?? '');
  }

  const violations = error instanceof ApiError ? error.violationsByField() : {};

  return (
    <Modal
      open={!!relationship}
      onOpenChange={(o) => !o && onClose()}
      title={t('relationship.edit', 'Beziehung bearbeiten')}
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!relationship) return;
          onSubmit(relationship.id, { verification_state: state, notes: notes.trim() }, onClose);
        }}
      >
        <p className="text-xs text-gray-500 dark:text-gray-400">
          {t(
            'relationship.editHint',
            'Endpunkte und Typ sind unveränderlich — zum Umhängen die Beziehung löschen und neu anlegen.',
          )}
        </p>
        <Select
          label={t('relationship.verificationState', 'Prüfstatus')}
          value={state}
          onChange={(e) => setState(e.target.value)}
          options={verificationStates.map((value) => ({
            value,
            label: t(`relationship.state.${value}`, value),
          }))}
          error={violations.verification_state}
        />
        <Input
          label={t('relationship.notes', 'Notiz')}
          value={notes}
          onChange={(e) => setNotes(e.target.value)}
          error={violations.notes}
        />
        {error && Object.keys(violations).length === 0 ? (
          <p role="alert" className="text-sm text-red-600 dark:text-red-400">
            {error.message}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common.cancel', 'Abbrechen')}
          </Button>
          <Button type="submit" loading={pending}>
            {t('common.save', 'Speichern')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
