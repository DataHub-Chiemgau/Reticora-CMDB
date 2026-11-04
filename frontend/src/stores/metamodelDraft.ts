import { create } from 'zustand';

/**
 * The metamodel draft the administrator works in (MET-53, WP-798). While a
 * draft is active, withDraft (api/client.ts) adds `draft=<id>` to every
 * metamodel write, so the backend captures the change as an item of the
 * draft (202) instead of applying it; without one every editor writes
 * directly (MET-04). The draft lives in sessionStorage: it belongs to the
 * browser tab, not to every tab of the user.
 */

/** The kinds of draft items (WP-260); labels metamodelDrafts.itemKinds.*. */
export const DRAFT_ITEM_KINDS = [
  'ci_type',
  'ci_type_field',
  'global_field',
  'attribute_definition',
  'attribute_set',
  'template',
  'template_version',
  'rule',
  'layout',
] as const;
export type DraftItemKind = (typeof DRAFT_ITEM_KINDS)[number];

/** The operations of draft items (WP-260); labels metamodelDrafts.ops.*. */
export const DRAFT_OPS = [
  'create',
  'update',
  'upsert',
  'delete',
  'clone',
  'activate',
  'deactivate',
  'assign',
  'publish',
] as const;
export type DraftOp = (typeof DRAFT_OPS)[number];

/** The states of a draft; labels metamodelDrafts.statuses.*. */
export const DRAFT_STATUSES = ['draft', 'tested', 'published', 'discarded'] as const;

/** The active draft: its id for ?draft and its name for the banner. */
export interface ActiveDraft {
  id: string;
  name: string;
}

interface MetamodelDraftState {
  draft: ActiveDraft | null;
  /** Rises with every captured write (202); the item list reads again. */
  itemRevision: number;
  setDraft: (draft: ActiveDraft) => void;
  clear: () => void;
  bumpItemRevision: () => void;
}

const STORAGE_KEY = 'reticora-metamodel-draft';

function load(): ActiveDraft | null {
  try {
    const raw = window.sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<ActiveDraft>;
    if (typeof parsed.id !== 'string' || parsed.id === '') return null;
    return { id: parsed.id, name: typeof parsed.name === 'string' ? parsed.name : '' };
  } catch {
    return null;
  }
}

function persist(draft: ActiveDraft | null) {
  try {
    if (draft) window.sessionStorage.setItem(STORAGE_KEY, JSON.stringify(draft));
    else window.sessionStorage.removeItem(STORAGE_KEY);
  } catch {
    // The draft still applies for this page view.
  }
}

export const useMetamodelDraftStore = create<MetamodelDraftState>((set) => ({
  draft: load(),
  itemRevision: 0,
  setDraft: (draft) => {
    const next = { id: draft.id, name: draft.name };
    persist(next);
    set({ draft: next });
  },
  clear: () => {
    persist(null);
    set({ draft: null });
  },
  bumpItemRevision: () => set((state) => ({ itemRevision: state.itemRevision + 1 })),
}));
