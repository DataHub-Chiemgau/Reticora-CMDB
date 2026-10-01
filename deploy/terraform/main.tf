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
