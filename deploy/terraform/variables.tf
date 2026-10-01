# Input variables of the Reticora CMDB module.
#
# NFR-09/PRI-11 (WP-105): SaaS data including backups, logs and the search
# index stays in the EU. Every region variable is validated against EU
# regions of the supported providers; a non-EU region fails `terraform plan`.
# See docs/betrieb/referenzmodell.md.

variable "environment" {
  description = "Deployment environment (development, staging, production)"
  type        = string
  default     = "development"

  validation {
    condition     = contains(["development", "staging", "production"], var.environment)
    error_message = "environment must be development, staging or production."
  }
}

variable "operating_model" {
  description = "Operating model (PRI-11): saas_eu (reference), dedicated or air_gapped (optional, not gated for G1)"
  type        = string
  default     = "saas_eu"

  validation {
    condition     = contains(["saas_eu", "dedicated", "air_gapped"], var.operating_model)
    error_message = "operating_model must be saas_eu, dedicated or air_gapped."
  }
}

# EU regions (NFR-09) of the supported providers. Only locations inside the
# EU are listed: London (AWS eu-west-2, GCP europe-west2) and Zurich (AWS
# eu-central-2, GCP europe-west6) are deliberately missing, as are other
# EEA/third-country locations. Extend the list only with EU regions.
locals {
  eu_regions = [
    # AWS
    "eu-central-1", "eu-west-1", "eu-west-3", "eu-south-1", "eu-south-2", "eu-north-1",
    # Google Cloud
    "europe-west1", "europe-west3", "europe-west4", "europe-west8", "europe-west9",
    "europe-west10", "europe-west12", "europe-north1", "europe-north2", "europe-central2",
    "europe-southwest1",
    # Microsoft Azure
    "westeurope", "northeurope", "germanywestcentral", "germanynorth", "francecentral",
    "francesouth", "swedencentral", "swedensouth", "italynorth", "polandcentral", "spaincentral",
    # Hetzner Cloud
    "fsn1", "nbg1", "hel1",
    # STACKIT
    "eu01", "eu02",
  ]
}

variable "region" {
  description = "Primary region of the platform (database, application, object storage)"
  type        = string
  default     = "eu-central-1"

  validation {
    condition     = contains(local.eu_regions, var.region)
    error_message = "region must be an EU region (NFR-09 data residency)."
  }
}

variable "backup_region" {
  description = "Region of backups and WAL archives; defaults to the primary region"
  type        = string
  default     = null

  validation {
    condition     = var.backup_region == null || contains(local.eu_regions, var.backup_region)
    error_message = "backup_region must be an EU region (NFR-09: backups stay in the EU)."
  }
}

variable "log_region" {
  description = "Region of log and telemetry storage; defaults to the primary region"
  type        = string
  default     = null

  validation {
    condition     = var.log_region == null || contains(local.eu_regions, var.log_region)
    error_message = "log_region must be an EU region (NFR-09: logs stay in the EU)."
  }
}

variable "search_region" {
  description = "Region of the search index (OpenSearch); defaults to the primary region"
  type        = string
  default     = null

  validation {
    condition     = var.search_region == null || contains(local.eu_regions, var.search_region)
    error_message = "search_region must be an EU region (NFR-09: the search index stays in the EU)."
  }
}
