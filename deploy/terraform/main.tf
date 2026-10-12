# Reticora CMDB — Terraform Module
#
# Provider-neutral module skeleton: it fixes the operating model and the EU
# data residency of all data locations (PRI-11, NFR-09) before provider and
# resources are added for a target cloud. Variables: variables.tf.

terraform {
  # 1.9: variable validations may refer to locals (EU region list).
  required_version = ">= 1.9.0"
}

locals {
  # Every data location falls back to the validated primary region.
  data_locations = {
    primary = var.region
    backups = coalesce(var.backup_region, var.region)
    logs    = coalesce(var.log_region, var.region)
    search  = coalesce(var.search_region, var.region)
  }
}

output "operating_model" {
  description = "Operating model of this deployment (PRI-11)"
  value       = var.operating_model
}

output "data_locations" {
  description = "Regions of all data locations; validated to be in the EU (NFR-09)"
  value       = local.data_locations
}

# Backup policy every provider implementation applies to the backup bucket
# (pgBackRest repository) and the document buckets (WP-100, NFR-05/06).
output "backup_policy" {
  description = "Versioning, replication and retention of the backup and document buckets"
  value = {
    tool           = "pgbackrest"
    bucket_region  = local.data_locations.backups
    versioning     = true
    replica_region = var.backup_replica_region
    retention_days = var.backup_retention_days
    wal_archive    = true
    base_backup    = "daily"
    rpo_minutes    = 15
    rto_hours      = 4
  }
}

# Replication is required for production (NFR-05); a missing or identical
# replica region is reported on every plan.
check "backup_replication" {
  assert {
    condition     = var.environment != "production" ? true : (var.backup_replica_region == null ? false : var.backup_replica_region != local.data_locations.backups)
    error_message = "production needs backup_replica_region: an EU region other than the backup region (NFR-05)."
  }
}
