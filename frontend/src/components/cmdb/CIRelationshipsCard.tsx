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
 *
 * The relationship types come from GET /relationship-types, system and
 * organization types (WP-323). The create dialog offers the types the
 * direction and the CI types of the edge allow (allowed_source_types,
 * allowed_target_types, subtypes included), names them by display_name
 * (outgoing) or reverse_name (incoming), finds the other end with the
 * ObjectPicker among the CIs of the allowed types and renders the
 * attribute schema of the type (metadata_schema) with DynamicForm. The edit
 * dialog edits the attributes too and shows the history of the edge
 * (GET /relationships/{id}/changes, WP-296).
 */
import { useId, useRef, useState } from 'react';
import type { TFunction } from 'i18next';
import { ArrowLeft, ArrowRight, ChevronDown, ChevronRight, GitFork, Plus } from 'lucide-react';
import { Link } from 'react-router';
import { useQueries } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { ApiError, ciApi, verificationStates } from '../../api/client';
import type {
  Relationship,
  RelationshipChange,
  RelationshipCreateRequest,
  RelationshipSuppression,
  RelationshipUpdateRequest,
} from '../../api/client';
import {
  relationshipAttributeDefaults,
  relationshipAttributeFields,
  type CIType,
  type RelationshipType,
} from '../../api/cmdb';
import { useAllCITypes, useRelationshipTypes } from '../../api/cmdbHooks';
import {
  useCI,
  useCIRelationships,
  useCreateRelationship,
  useDeleteRelationship,
  useLiftSuppression,
  useRelationshipChanges,
  useRelationshipSuppressions,
  useUpdateRelationship,
} from '../../api/hooks';
import { DynamicForm } from '../form/DynamicForm';
import { ObjectPicker, type PickedObject } from '../pickers/ObjectPicker';
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
 * The structural types (contains, mounted_in, located_in and their
 * inverses) are not offered when creating an edge by hand: they are
 * projected from the CI's parent, rack mount and location (DB-05) and
 * POST /relationships refuses them with 422 (F-242).
 */
export const STRUCTURAL_REL_TYPES: readonly string[] = [
  'contains',
  'contained_by',
  'parent_of',
  'child_of',
  'mounted_in',
  'located_in',
];

/** The attribute key the server records the provenance of an edge under (REL-08). */
const PROVENANCE_KEY = 'provenance';

/**
 * matchesAllowedTypes tells whether a CI type is one of the allowed CI type
 * keys or a subtype of one (parent_type_id), as POST /relationships checks
 * it (REL-06, WP-294). An empty list allows every type; an unknown type is
 * not refused here, the server decides.
 */
export function matchesAllowedTypes(
  typeId: string | undefined,
  allowed: readonly string[] | undefined,
  types: readonly CIType[],
): boolean {
  if (!allowed || allowed.length === 0 || !typeId) return true;
  const byId = new Map(types.map((type) => [type.id, type]));
  if (!byId.has(typeId)) return true;
  const seen = new Set<string>();
  let current = byId.get(typeId);
  while (current && !seen.has(current.id)) {
    if (allowed.includes(current.key)) return true;
    seen.add(current.id);
    current = current.parent_type_id ? byId.get(current.parent_type_id) : undefined;
  }
  return false;
}

/**
 * allowedTypeIds are the ids of the CI types an end of an edge may have:
 * the allowed keys and their subtypes. Undefined allows every type.
 */
export function allowedTypeIds(
  allowed: readonly string[] | undefined,
  types: readonly CIType[],
): string[] | undefined {
  if (!allowed || allowed.length === 0) return undefined;
  return types.filter((type) => matchesAllowedTypes(type.id, allowed, types)).map((t) => t.id);
}

/**
 * relationshipTypeLabel names an edge seen from the CI: display_name for an
 * outgoing edge, reverse_name for an incoming one; the key while the type
 * is unknown.
 */
export function relationshipTypeLabel(
  types: readonly RelationshipType[],
  key: string,
  outgoing: boolean,
): string {
  const type = types.find((candidate) => candidate.key === key);
  if (!type) return key;
  return (outgoing ? type.display_name : type.reverse_name) || type.display_name || key;
}

/** attributeErrors maps the 422 violations of an edge write to its attribute keys. */
function attributeErrors(
  error: Error | null,
  fields: readonly { name: string }[],
): Record<string, string> {
  if (!(error instanceof ApiError)) return {};
  const violations = error.violationsByField();
  const out: Record<string, string> = {};
  for (const field of fields) {
    const detail = violations[field.name] ?? violations[`attributes.${field.name}`];
    if (detail) out[field.name] = detail;
  }
  return out;
}

/** attributePayload keeps the values of the schema keys that are set. */
function attributePayload(
  fields: readonly { name: string }[],
  values: Record<string, unknown>,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const field of fields) {
    const value = values[field.name];
    if (value !== undefined && value !== '') out[field.name] = value;
  }
  return out;
}

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
  const relationshipTypes = useRelationshipTypes().data?.data ?? [];
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
    <Card
      title={t('ci.relationships')}
      actions={
        <Button
          size="sm"
          variant="secondary"
          icon={<Plus />}
          onClick={() => {
            createRelationship.reset();
            setShowCreate(true);
          }}
        >
          {t('relationship.add', 'Beziehung hinzufügen')}
        </Button>
      }
    >
      {mutationError ? (
        <p role="alert" className="mb-3 text-sm text-danger">
          {mutationError.message}
        </p>
      ) : null}

      {hiddenCount > 0 ? (
        <p className="mb-3 flex flex-wrap items-center gap-x-2 text-sm text-fg-muted">
          <span>{t('relationshipCard.hiddenOutside', { count: hiddenCount })}</span>
          <Button
            size="sm"
            variant="ghost"
            className="-mx-1 px-2 text-accent hover:text-accent"
            onClick={() => setShowAll((v) => !v)}
          >
            {showAll ? t('relationshipCard.showInScope') : t('relationshipCard.showAll')}
          </Button>
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
        <EmptyState title={t('ci.noRelationships')} icon={<GitFork />} className="py-8" />
      ) : (
        <ul className="-my-2 divide-y divide-line text-sm" aria-label={t('ci.relationships')}>
          {relationships.map((relationship) => {
            const isOutgoing = relationship.source_ci_id === ciId;
            const otherId = isOutgoing ? relationship.target_ci_id : relationship.source_ci_id;
            const state = relationship.verification_state ?? 'unverified';
            return (
              <li
                key={relationship.id}
                className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 py-2.5"
              >
                <span className="flex flex-wrap items-center gap-2">
                  <Badge variant="neutral">
                    {relationshipTypeLabel(relationshipTypes, relationship.rel_type, isOutgoing)}
                  </Badge>
                  {isOutgoing ? (
                    <ArrowRight aria-hidden="true" className="h-4 w-4 text-fg-subtle" />
                  ) : (
                    <ArrowLeft aria-hidden="true" className="h-4 w-4 text-fg-subtle" />
                  )}
                  {relationship.target_location_id ? (
                    <span className="text-sm text-fg">
                      {locations.path(relationship.target_location_id)}
                    </span>
                  ) : (
                    <Link
                      to={`/cmdb/${otherId}`}
                      className="rounded font-mono text-xs text-accent underline-offset-2 [overflow-wrap:anywhere] hover:underline"
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
                <span className="flex flex-wrap items-center gap-x-1 gap-y-0.5">
                  <span className="mr-1 text-xs text-fg-muted">
                    {relationship.source_system || relationship.source}
                  </span>
                  {relationship.projected ? (
                    <Badge variant="info">{t('relationship.projected')}</Badge>
                  ) : null}
                  {!relationship.projected && state !== 'verified' ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      className="px-2 text-accent hover:text-accent"
                      disabled={updateRelationship.isPending}
                      onClick={() =>
                        updateRelationship.mutate({
                          id: relationship.id,
                          data: { verification_state: 'verified' },
                        })
                      }
                    >
                      {t('relationship.verify', 'Bestätigen')}
                    </Button>
                  ) : null}
                  {relationship.projected ? null : (
                    <>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="px-2 text-accent hover:text-accent"
                        onClick={() => setEditing(relationship)}
                      >
                        {t('common.edit', 'Bearbeiten')}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="px-2 text-danger hover:text-danger"
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
                      </Button>
                    </>
                  )}
                </span>
              </li>
            );
          })}
        </ul>
      )}

      <div className="mt-4 border-t border-line pt-3">
        <Button
          size="sm"
          variant="ghost"
          className="-ml-2 text-accent hover:text-accent"
          icon={showSuppressions ? <ChevronDown /> : <ChevronRight />}
          aria-expanded={showSuppressions}
          onClick={() => setShowSuppressions((v) => !v)}
        >
          {showSuppressions
            ? t('relationshipCard.hideSuppressions')
            : t('relationshipCard.showSuppressions')}
        </Button>
        {showSuppressions ? <SuppressionList ciId={ciId} types={relationshipTypes} /> : null}
      </div>

      <SuppressRelationshipModal
        relationship={suppressing}
        ciId={ciId}
        types={relationshipTypes}
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
        types={relationshipTypes}
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
        ciId={ciId}
        types={relationshipTypes}
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
function SuppressionList({ ciId, types }: { ciId: string; types: RelationshipType[] }) {
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
        <p role="alert" className="mb-2 text-sm text-danger">
          {(liftSuppression.error as Error).message}
        </p>
      ) : null}
      {suppressions.length === 0 ? (
        <p className="text-xs text-fg-muted">{t('relationshipCard.noSuppressions')}</p>
      ) : (
        <ul
          className="divide-y divide-line text-sm"
          aria-label={t('relationshipCard.suppressions')}
        >
          {suppressions.map((suppression) => {
            const isOutgoing = suppression.source_ci_id === ciId;
            const otherId = isOutgoing ? suppression.target_ci_id : suppression.source_ci_id;
            return (
              <li
                key={suppression.id}
                className="flex flex-wrap items-start justify-between gap-2 py-2.5"
              >
                <span className="flex flex-col gap-1">
                  <span className="flex flex-wrap items-center gap-2">
                    <Badge variant="neutral">
                      {relationshipTypeLabel(types, suppression.rel_type, isOutgoing)}
                    </Badge>
                    {isOutgoing ? (
                      <ArrowRight aria-hidden="true" className="h-4 w-4 text-fg-subtle" />
                    ) : (
                      <ArrowLeft aria-hidden="true" className="h-4 w-4 text-fg-subtle" />
                    )}
                    <Link
                      to={`/cmdb/${otherId}`}
                      className="rounded font-mono text-xs text-accent underline-offset-2 [overflow-wrap:anywhere] hover:underline"
                    >
                      {otherId}
                    </Link>
                  </span>
                  <span className="text-xs text-fg">
                    {suppression.reason
                      ? t('relationshipCard.suppressionReason', { reason: suppression.reason })
                      : t('relationshipCard.noReason')}
                  </span>
                  <span className="text-xs text-fg-muted">
                    {t('relationshipCard.suppressedBy', {
                      actor: suppression.created_by ?? t('relationshipCard.unknownActor'),
                      date: new Date(suppression.created_at).toLocaleString(i18n.language),
                    })}
                  </span>
                </span>
                <Button
                  size="sm"
                  variant="ghost"
                  className="px-2 text-accent hover:text-accent"
                  title={t('relationshipCard.liftHint')}
                  disabled={liftSuppression.isPending}
                  onClick={() => liftSuppression.mutate(suppression.id)}
                >
                  {t('relationshipCard.lift')}
                </Button>
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
  types: RelationshipType[];
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
  types,
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
          ? t('relationshipCard.suppressConfirm', {
              relType: relationshipTypeLabel(
                types,
                relationship.rel_type,
                relationship.source_ci_id === ciId,
              ),
              other,
            })
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
          <p role="alert" className="text-sm text-danger">
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
  types: RelationshipType[];
  pending: boolean;
  error: Error | null;
  onSubmit: (data: RelationshipCreateRequest) => void;
}

/**
 * endpointMessage words a 422 on source_ci_id or target_ci_id in the UI
 * language: a CI type outside allowed_source_types/allowed_target_types
 * (WP-294), else an end the caller cannot see, foreign or unknown (F-145).
 */
function endpointMessage(violations: Record<string, string>, t: TFunction): string | undefined {
  const detail = violations.source_ci_id ?? violations.target_ci_id;
  if (!detail) return undefined;
  return /allowed_(source|target)_types/.test(detail)
    ? t('relationship.typePairRefused')
    : t('relationship.ciNotFound');
}

/** relTypeMessage words a 422 on rel_type: the cardinality of the type (WP-294) or the server text. */
function relTypeMessage(violations: Record<string, string>, t: TFunction): string | undefined {
  const detail = violations.rel_type;
  if (!detail) return undefined;
  return detail.includes('cardinality') ? t('relationship.cardinalityRefused') : detail;
}

function CreateRelationshipModal({
  open,
  onOpenChange,
  ciId,
  types,
  pending,
  error,
  onSubmit,
}: CreateModalProps) {
  const { t, i18n } = useTranslation();
  // The type of this CI and the CI type tree decide which relationship
  // types and which other ends are offered; both are read while open.
  const ci = useCI(open ? ciId : '');
  const ciTypes = useAllCITypes({}, { enabled: open }).data ?? [];
  const [direction, setDirection] = useState<'outgoing' | 'incoming'>('outgoing');
  const [relType, setRelType] = useState('');
  const [target, setTarget] = useState<PickedObject[]>([]);
  const [notes, setNotes] = useState('');
  const [values, setValues] = useState<Record<string, unknown>>({});
  // The form starts empty whenever the dialog opens.
  const [shown, setShown] = useState(open);
  if (open !== shown) {
    setShown(open);
    if (open) {
      setDirection('outgoing');
      setTarget([]);
      setNotes('');
      setRelType('');
    }
  }

  const outgoing = direction === 'outgoing';
  const ownTypeId = ci.data?.ci_type_id;
  const offered = types.filter(
    (type) =>
      !STRUCTURAL_REL_TYPES.includes(type.key) &&
      matchesAllowedTypes(
        ownTypeId,
        outgoing ? type.allowed_source_types : type.allowed_target_types,
        ciTypes,
      ),
  );
  const chosen = offered.find((type) => type.key === relType) ?? offered[0];
  const fields = relationshipAttributeFields(chosen?.metadata_schema, i18n.language);
  // The attributes start with the defaults of the chosen type.
  const [valuesOf, setValuesOf] = useState<string | undefined>(undefined);
  const chosenKey = open ? chosen?.key : undefined;
  if (chosenKey !== valuesOf) {
    setValuesOf(chosenKey);
    setValues(relationshipAttributeDefaults(chosen?.metadata_schema));
  }
  const otherTypeIds = chosen
    ? allowedTypeIds(outgoing ? chosen.allowed_target_types : chosen.allowed_source_types, ciTypes)
    : undefined;

  const violations = error instanceof ApiError ? error.violationsByField() : {};
  const endpointError = endpointMessage(violations, t);
  const relTypeError = relTypeMessage(violations, t);
  const attrErrors = attributeErrors(error, fields);
  const shownElsewhere =
    !!endpointError || !!relTypeError || !!violations.notes || Object.keys(attrErrors).length > 0;
  const other = target[0];

  return (
    <Modal
      open={open}
      onOpenChange={onOpenChange}
      title={t('relationship.add', 'Beziehung hinzufügen')}
      size="lg"
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!chosen || !other) return;
          onSubmit({
            // A relationship created here is a human assertion, so it is
            // recorded as manual and pre-verified: the operator entering it
            // is the verification.
            source_ci_id: outgoing ? ciId : other.id,
            target_ci_id: outgoing ? other.id : ciId,
            rel_type: chosen.key,
            source: 'manual',
            verification_state: 'verified',
            notes: notes.trim() || undefined,
            attributes: fields.length > 0 ? attributePayload(fields, values) : undefined,
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
          value={chosen?.key ?? ''}
          onChange={(e) => setRelType(e.target.value)}
          options={offered.map((type) => ({
            value: type.key,
            label: (outgoing ? type.display_name : type.reverse_name) || type.key,
          }))}
          error={relTypeError}
        />
        {offered.length === 0 && !ci.isLoading ? (
          <p className="text-sm text-fg-muted">{t('relationship.noTypes')}</p>
        ) : null}
        <ObjectPicker
          // A new type or direction searches among the CIs of its types.
          key={`${chosen?.key ?? ''}:${direction}`}
          label={t('relationship.targetCi')}
          kinds={['ci']}
          value={target}
          onChange={setTarget}
          ciTypeIds={otherTypeIds}
          required
        />
        {endpointError ? (
          <p role="alert" className="text-sm text-danger">
            {endpointError}
          </p>
        ) : null}
        {fields.length > 0 ? (
          <fieldset className="min-w-0 space-y-3">
            <legend className="mb-2 text-sm font-semibold text-fg">
              {t('relationship.attributes')}
            </legend>
            <DynamicForm
              fields={fields}
              values={values}
              onChange={(name, value) => setValues((v) => ({ ...v, [name]: value }))}
              errors={attrErrors}
            />
          </fieldset>
        ) : null}
        <Input
          label={t('relationship.notes', 'Notiz')}
          value={notes}
          onChange={(e) => setNotes(e.target.value)}
          error={violations.notes}
        />
        {error && !shownElsewhere ? (
          <p role="alert" className="text-sm text-danger">
            {error.message}
          </p>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={() => onOpenChange(false)}>
            {t('common.cancel', 'Abbrechen')}
          </Button>
          <Button type="submit" loading={pending} disabled={!chosen || !other}>
            {t('common.create', 'Anlegen')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

interface EditModalProps {
  relationship: Relationship | null;
  ciId: string;
  types: RelationshipType[];
  onClose: () => void;
  pending: boolean;
  error: Error | null;
  onSubmit: (id: string, data: RelationshipUpdateRequest, done: () => void) => void;
}

function EditRelationshipModal({
  relationship,
  ciId,
  types,
  onClose,
  pending,
  error,
  onSubmit,
}: EditModalProps) {
  const { t, i18n } = useTranslation();
  const [state, setState] = useState('unverified');
  const [notes, setNotes] = useState('');
  const [values, setValues] = useState<Record<string, unknown>>({});
  // Re-seed the form whenever a different relationship is opened.
  const [seededId, setSeededId] = useState<string | null>(null);
  if (relationship && relationship.id !== seededId) {
    setSeededId(relationship.id);
    setState(relationship.verification_state ?? 'unverified');
    setNotes(relationship.notes ?? '');
    // provenance is recorded by the server and cannot be changed (REL-08).
    const { [PROVENANCE_KEY]: _provenance, ...attributes } = relationship.attributes ?? {};
    void _provenance;
    setValues(attributes);
  }

  const type = relationship ? types.find((k) => k.key === relationship.rel_type) : undefined;
  const fields = relationshipAttributeFields(type?.metadata_schema, i18n.language);
  const violations = error instanceof ApiError ? error.violationsByField() : {};
  const attrErrors = attributeErrors(error, fields);
  const shownElsewhere =
    !!violations.verification_state || !!violations.notes || Object.keys(attrErrors).length > 0;
  const outgoing = relationship?.source_ci_id === ciId;

  return (
    <Modal
      open={!!relationship}
      onOpenChange={(o) => !o && onClose()}
      title={t('relationship.edit', 'Beziehung bearbeiten')}
      description={
        relationship ? relationshipTypeLabel(types, relationship.rel_type, outgoing) : undefined
      }
      size="lg"
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!relationship) return;
          const data: RelationshipUpdateRequest = {
            verification_state: state,
            notes: notes.trim(),
          };
          if (fields.length > 0) data.attributes = attributePayload(fields, values);
          onSubmit(relationship.id, data, onClose);
        }}
      >
        <p className="text-xs text-fg-muted">
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
        {fields.length > 0 ? (
          <fieldset className="min-w-0 space-y-3">
            <legend className="mb-2 text-sm font-semibold text-fg">
              {t('relationship.attributes')}
            </legend>
            <DynamicForm
              fields={fields}
              values={values}
              onChange={(name, value) => setValues((v) => ({ ...v, [name]: value }))}
              errors={attrErrors}
            />
          </fieldset>
        ) : null}
        <Input
          label={t('relationship.notes', 'Notiz')}
          value={notes}
          onChange={(e) => setNotes(e.target.value)}
          error={violations.notes}
        />
        {error && !shownElsewhere ? (
          <p role="alert" className="text-sm text-danger">
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
      {relationship ? <RelationshipHistory id={relationship.id} /> : null}
    </Modal>
  );
}

/**
 * RelationshipHistory lists the changes of an edge, newest first
 * (GET /relationships/{id}/changes, WP-296): time, kind, changed fields,
 * actor and source.
 */
function RelationshipHistory({ id }: { id: string }) {
  const { t, i18n } = useTranslation();
  const headingId = useId();
  const changesQuery = useRelationshipChanges(id);
  const changes: RelationshipChange[] = [...(changesQuery.data?.data ?? [])].sort((a, b) =>
    b.observed_at.localeCompare(a.observed_at),
  );

  return (
    <section aria-labelledby={headingId} className="mt-6 border-t border-line pt-4">
      <h3 id={headingId} className="mb-2 text-sm font-semibold text-fg">
        {t('relationship.history.title')}
      </h3>
      {changesQuery.isLoading ? (
        <SkeletonList rows={2} label={t('app.loading')} />
      ) : changesQuery.error ? (
        <ErrorState
          title={t('relationship.history.loadError')}
          retryLabel={t('common.retry')}
          onRetry={() => void changesQuery.refetch()}
        />
      ) : changes.length === 0 ? (
        <p className="text-xs text-fg-muted">{t('relationship.history.empty')}</p>
      ) : (
        <ol aria-label={t('relationship.history.title')} className="divide-y divide-line text-sm">
          {changes.map((change) => (
            <li key={change.id} className="flex flex-col gap-1 py-2">
              <span className="flex flex-wrap items-center gap-2">
                <Badge variant="neutral">
                  {t(`relationship.changeKinds.${change.change_kind}`, change.change_kind)}
                </Badge>
                <time dateTime={change.observed_at} className="text-xs text-fg-muted">
                  {new Date(change.observed_at).toLocaleString(i18n.language)}
                </time>
              </span>
              {change.changed_fields.length > 0 ? (
                <span className="text-xs text-fg">
                  {t('relationship.history.fields', {
                    fields: change.changed_fields
                      .map((field) => t(`relationship.changedFields.${field}`, field))
                      .join(', '),
                  })}
                </span>
              ) : null}
              <span className="text-xs text-fg-muted">
                {t('relationship.history.by', {
                  actor:
                    change.actor_id ??
                    t(`relationship.actorTypes.${change.actor_type}`, change.actor_type),
                  source: t(`relationship.changeSources.${change.source}`, change.source),
                })}
              </span>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
