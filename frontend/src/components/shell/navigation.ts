import {
  Binary,
  Activity,
  Archive,
  Armchair,
  BadgeCheck,
  Bookmark,
  Building,
  Boxes,
  Bot,
  ClipboardCheck,
  Download,
  FileText,
  FolderTree,
  GitFork,
  GraduationCap,
  Inbox,
  KeyRound,
  LayoutDashboard,
  ListChecks,
  ListTodo,
  Lock,
  Map,
  Network,
  Package,
  Radar,
  Rows3,
  ScrollText,
  Server,
  Scale,
  Shapes,
  ShieldCheck,
  ShoppingCart,
  Siren,
  Sparkles,
  Tags,
  Ticket,
  Timer,
  Trash2,
  Upload,
  UserCheck,
  UserCog,
  Users,
  Warehouse,
  Webhook,
  Workflow,
  Fingerprint,
  Handshake,
  Truck,
  FileSignature,
  Wrench,
} from 'lucide-react';
import type { LucideIcon } from 'lucide-react';
import type { TFunction } from 'i18next';
import { createContext } from 'react';
import type { AppPage } from '../CommandPalette';

export interface NavItemDef {
  page: AppPage;
  icon: LucideIcon;
}

export interface NavGroupDef {
  id: string;
  /** Catalog key of the group heading; the first group has none. */
  labelKey?: string;
  /** Whether the group is open until the user decides otherwise. */
  defaultOpen: boolean;
  items: NavItemDef[];
}

/**
 * The information architecture of the product. Groups follow the work, not
 * the modules: find and understand infrastructure, keep its data right,
 * run operations, manage physical goods, prove compliance, administer.
 * Every page of the router appears exactly once (navigation.test.ts).
 * Pages the release or the user's rights exclude are filtered out by App,
 * pages the tenant's plan excludes are shown locked (navPageState), and a
 * group without a shown page disappears.
 */
export const NAV_GROUPS: NavGroupDef[] = [
  {
    id: 'main',
    defaultOpen: true,
    items: [
      { page: 'dashboard', icon: LayoutDashboard },
      { page: 'assistant', icon: Sparkles },
    ],
  },
  {
    id: 'infrastructure',
    labelKey: 'navGroup.infrastructure',
    defaultOpen: true,
    items: [
      { page: 'cmdb', icon: Server },
      { page: 'topology', icon: Network },
      { page: 'ipam', icon: Binary },
      { page: 'racks', icon: Rows3 },
      { page: 'roomplan', icon: Boxes },
      { page: 'map', icon: Map },
      { page: 'locations', icon: FolderTree },
      { page: 'saved-views', icon: Bookmark },
    ],
  },
  {
    id: 'capture',
    labelKey: 'navGroup.capture',
    defaultOpen: true,
    items: [
      { page: 'reviews', icon: Inbox },
      { page: 'discovery', icon: Radar },
      { page: 'agents', icon: Bot },
      { page: 'jobs', icon: ListChecks },
    ],
  },
  {
    id: 'operations',
    labelKey: 'navGroup.operations',
    defaultOpen: true,
    items: [
      { page: 'tickets', icon: Ticket },
      { page: 'slas', icon: Timer },
      { page: 'maintenance', icon: Wrench },
      { page: 'monitoring', icon: Activity },
      { page: 'workflows', icon: Workflow },
      { page: 'forms', icon: FileSignature },
    ],
  },
  {
    id: 'assets',
    labelKey: 'navGroup.assets',
    defaultOpen: false,
    items: [
      { page: 'assets', icon: Archive },
      { page: 'assignments', icon: UserCog },
      { page: 'contracts', icon: Handshake },
      { page: 'suppliers', icon: Truck },
      { page: 'documents', icon: FileText },
      { page: 'disposal', icon: Trash2 },
    ],
  },
  {
    id: 'stock',
    labelKey: 'navGroup.stock',
    defaultOpen: false,
    items: [
      { page: 'consumables', icon: Package },
      { page: 'inventory', icon: Warehouse },
      { page: 'stocktake', icon: ClipboardCheck },
      { page: 'orders', icon: ShoppingCart },
    ],
  },
  {
    id: 'workplace',
    labelKey: 'navGroup.workplace',
    defaultOpen: false,
    items: [
      { page: 'desks', icon: Armchair },
      { page: 'keys', icon: KeyRound },
      { page: 'trainings', icon: GraduationCap },
    ],
  },
  {
    id: 'governance',
    labelKey: 'navGroup.governance',
    defaultOpen: false,
    items: [
      { page: 'compliance', icon: ShieldCheck },
      { page: 'findings', icon: Siren },
      { page: 'iga', icon: Fingerprint },
      { page: 'audit', icon: ScrollText },
      { page: 'security', icon: Lock },
    ],
  },
  {
    id: 'admin',
    labelKey: 'navGroup.admin',
    defaultOpen: false,
    items: [
      { page: 'users', icon: Users },
      { page: 'clients', icon: Building },
      { page: 'plan', icon: BadgeCheck },
      { page: 'permissions', icon: UserCheck },
      { page: 'ci-types', icon: Shapes },
      { page: 'rules', icon: ListTodo },
      { page: 'source-ranks', icon: Scale },
      { page: 'relationship-types', icon: GitFork },
      { page: 'tags', icon: Tags },
      { page: 'webhooks', icon: Webhook },
      { page: 'import', icon: Upload },
      { page: 'export', icon: Download },
    ],
  },
];

/**
 * NavPageState is what the navigation does with a page (WP-857): show it,
 * show it locked (the plan lacks its feature) or leave it out.
 */
export type NavPageState = 'visible' | 'locked' | 'hidden';

/** The three gates of a page, each a predicate on the page. */
export interface NavAccess {
  /** The tenant's plan includes the page's feature (navFeatureFor). */
  isEntitled: (page: AppPage) => boolean;
  /** The release serves the page's module (WP-214). */
  isPageEnabled: (page: AppPage) => boolean;
  /** The user holds the page's permission (navPermissionFor). */
  isPermitted: (page: AppPage) => boolean;
}

/**
 * navPageState is the one visibility rule of the navigation, the command
 * palette and the dashboard's quick access (WP-857). A switched-off release
 * module (P3, P4, add-ons) and a missing right hide the page: neither is
 * something the organization could book. A page that fails only on the
 * plan is locked: shown with a lock and a hint page instead of 403 errors.
 */
export function navPageState(page: AppPage, access: NavAccess): NavPageState {
  if (!access.isPageEnabled(page) || !access.isPermitted(page)) {
    return 'hidden';
  }
  return access.isEntitled(page) ? 'visible' : 'locked';
}

/** navPageStates applies navPageState to every page of NAV_GROUPS. */
export function navPageStates(access: NavAccess): Map<AppPage, NavPageState> {
  const states = new globalThis.Map<AppPage, NavPageState>();
  for (const group of NAV_GROUPS) {
    for (const { page } of group.items) {
      states.set(page, navPageState(page, access));
    }
  }
  return states;
}

/**
 * NavLock tells the sidebar which pages are locked and the organization's
 * plan (from /entitlements) for the hint beside the lock.
 */
export interface NavLock {
  pages: ReadonlySet<AppPage>;
  plan?: string;
}

/** NavLockContext carries the locked pages from the signed-in shell to the sidebar. */
export const NavLockContext = createContext<NavLock>({ pages: new Set() });

const PLANS = ['essential', 'standard', 'pro', 'enterprise'];

/**
 * lockedHint is the sentence beside a locked page: "Nicht im Paket
 * Essential enthalten", without a known plan "Nicht in Ihrem Paket enthalten".
 */
export function lockedHint(t: TFunction, plan?: string): string {
  return plan && PLANS.includes(plan)
    ? t('discoverability.lockedInPlan', { plan: t(`discoverability.plans.${plan}`) })
    : t('discoverability.lockedNoPlan');
}
