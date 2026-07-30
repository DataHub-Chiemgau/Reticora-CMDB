export type StatusVariant = 'success' | 'warning' | 'danger' | 'neutral' | 'info';

export function getStatusBadgeVariant(status: string): StatusVariant {
  switch (status) {
    case 'active':
      return 'success';
    case 'maintenance':
      return 'warning';
    case 'decommissioned':
      return 'danger';
    case 'inactive':
      return 'neutral';
    default:
      return 'info';
  }
}

export function getStatusTranslationKey(status: string) {
  switch (status) {
    case 'active':
      return 'ci.statusActive';
    case 'inactive':
      return 'ci.statusInactive';
    case 'maintenance':
      return 'ci.statusMaintenance';
    case 'decommissioned':
      return 'ci.statusDecommissioned';
    default:
      return 'ci.status';
  }
}
