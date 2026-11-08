import {
  Activity,
  Archive,
  Armchair,
  Bookmark,
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
  FileSignature,
  Wrench,
} from 'lucide-react';
import type { LucideIcon } from 'lucide-react';
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
 * Pages the tenant's plan, the release or the user's rights exclude are
 * filtered out by App, and a group without a visible page disappears.
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
      { page: 'permissions', icon: UserCheck },
      { page: 'ci-types', icon: Shapes },
      { page: 'rules', icon: ListTodo },
      { page: 'relationship-types', icon: GitFork },
      { page: 'tags', icon: Tags },
      { page: 'webhooks', icon: Webhook },
      { page: 'import', icon: Upload },
      { page: 'export', icon: Download },
    ],
  },
];
