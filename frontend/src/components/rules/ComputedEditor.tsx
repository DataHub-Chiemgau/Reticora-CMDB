/**
 * ComputedEditor edits the expression of a computed attribute (MET-10
 * computed, MET-45, UI-11, WP-802) in the attribute dialog AttributeEditor:
 * the RuleBuilder in its expression mode, which emits only $defs/expr of
 * api/rules.schema.json. An expression reads the entered (not computed)
 * attributes of the type (its own, the global ones that apply to it and
 * the inheritable ones of its ancestors) and the asset fields of the
 * whitelist; a result
 * type that does not fit the data type shows a hint, the binding check is
 * the server's (422 on computed, shown at the expression or at the node
 * its path names). The preview evaluates with evalExpr in the time zone of
 * the organization (GET /api/v1/org, read as in ScopeSwitcher).
 */
import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { fetchAPI } from '../../api/client';
import type { CIType } from '../../api/cmdb';
import {
  useApplicableAttributeDefinitions,
  useTypeAttributeDefinitions,
} from '../../api/cmdbHooks';
import type { components } from '../../api/generated/schema';
import {
  EXPRESSION_MODE,
  exprErrorPath,
  exprFieldCatalog,
  type ExprCheck,
} from '../../rules/builderModes';
import { RuleBuilder } from './RuleBuilder';

type Organization = components['schemas']['Organization'];

/** The zone of the preview before the organization has answered (or without it). */
const FALLBACK_ZONE = 'UTC';

/** useOrgTimeZone reads the time zone of the organization with the query of ScopeSwitcher. */
export function useOrgTimeZone(): string {
  const org = useQuery({
    queryKey: ['scope', 'org'],
    queryFn: () => fetchAPI<Organization>('/org'),
    retry: false,
    staleTime: 5 * 60_000,
  });
  return org.data?.timezone || FALLBACK_ZONE;
}

/** ancestorIds returns the ancestors of a type from parent_type_id, nearest first. */
function ancestorIds(ciTypeId: string | undefined, types: readonly CIType[]): string[] {
  const out: string[] = [];
  let current = types.find((ty) => ty.id === ciTypeId);
  while (current?.parent_type_id && !out.includes(current.parent_type_id)) {
    out.push(current.parent_type_id);
    current = types.find((ty) => ty.id === current!.parent_type_id);
  }
  return out;
}

export interface ComputedEditorProps {
  /**
   * The CI type of the attribute: its attributes are what the expression
   * reads. Without one (a global or set attribute) the references are not
   * narrowed and the server checks them.
   */
  ciTypeId?: string;
  /** The CI types, for the inheritable attributes of the ancestors. */
  types?: readonly CIType[];
  /** The key of the computed attribute (it cannot read itself). */
  attributeKey: string;
  /** The data type of the attribute: the result type of the expression must fit it. */
  dataType: string;
  /** The stored expression (edit) or none (create). */
  initialValue?: unknown;
  onChange: (check: ExprCheck) => void;
  /** The detail of a 422 on computed (ApiError.field, WP-510). */
  serverError?: string;
  showIssues?: boolean;
  /** The instant of the preview (tests). */
  now?: Date;
}

export function ComputedEditor({
  ciTypeId,
  types = [],
  attributeKey,
  dataType,
  initialValue,
  onChange,
  serverError,
  showIssues,
  now,
}: ComputedEditorProps) {
  const timeZone = useOrgTimeZone();
  const applicable = useApplicableAttributeDefinitions(ciTypeId);
  const ancestors = ancestorIds(ciTypeId, types);
  const inherited = useTypeAttributeDefinitions(ancestors);
  const inheritedData = inherited.map((q) => q.data);
  const fields = useMemo(
    () =>
      exprFieldCatalog(
        [
          ...(applicable.data?.data ?? []),
          ...inheritedData.flatMap((page) => (page?.data ?? []).filter((d) => d.inheritable)),
        ],
        attributeKey.trim(),
      ),
    // inheritedData is a new array each render; its pages decide.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [applicable.data, attributeKey, ...inheritedData],
  );
  return (
    <RuleBuilder
      mode={EXPRESSION_MODE}
      initialValue={initialValue ?? null}
      fields={fields}
      restrictFields={!!ciTypeId}
      dataType={dataType}
      timeZone={timeZone}
      now={now}
      onChange={onChange}
      serverError={
        serverError ? { path: exprErrorPath(serverError), message: serverError } : undefined
      }
      showIssues={showIssues}
    />
  );
}
