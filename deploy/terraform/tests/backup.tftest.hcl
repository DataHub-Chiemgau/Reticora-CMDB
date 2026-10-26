# terraform test of the module (run in deploy/terraform: terraform init
# -backend=false && terraform test; CI job terraform).
#
# - Backup replication (NFR-05, WP-443, F-129): a production plan without
#   backup_replica_region, or with the replica in the backup region, fails;
#   a distinct EU replica passes.
# - Data residency (NFR-09, WP-105): EU regions are accepted for every data
#   location, non-EU regions are rejected.
#
# Every run is a plan of the provider-free module: no provider is downloaded
# or called, no credentials are needed.

# ─── Backup replication for production (NFR-05) ────────────────────────────

run "terraform_replica_missing" {
  command = plan

  variables {
    environment = "production"
    region      = "eu-central-1"
  }

  expect_failures = [var.backup_replica_region]
}

run "terraform_replica_same" {
  command = plan

  variables {
    environment           = "production"
    region                = "eu-central-1"
    backup_replica_region = "eu-central-1"
  }

  expect_failures = [var.backup_replica_region]
}

# The replica must differ from the backup region, not only from the primary.
run "terraform_replica_same_as_backup_region" {
  command = plan

  variables {
    environment           = "production"
    region                = "eu-central-1"
    backup_region         = "europe-west3"
    backup_replica_region = "europe-west3"
  }

  expect_failures = [var.backup_replica_region]
}

run "terraform_replica_eu" {
  command = plan

  variables {
    environment           = "production"
    region                = "eu-central-1"
    backup_replica_region = "eu-west-1"
  }

  assert {
    condition     = output.backup_policy.bucket_region == "eu-central-1" && output.backup_policy.replica_region == "eu-west-1"
    error_message = "backup_policy must name the backup region and the distinct EU replica region."
  }
}

run "terraform_replica_in_primary_region_with_separate_backup_region" {
  command = plan

  variables {
    environment           = "production"
    region                = "eu-central-1"
    backup_region         = "europe-west3"
    backup_replica_region = "eu-central-1"
  }

  assert {
    condition     = output.backup_policy.bucket_region == "europe-west3" && output.backup_policy.replica_region == "eu-central-1"
    error_message = "a replica outside the backup region must be accepted."
  }
}

run "replica_optional_outside_production" {
  command = plan

  variables {
    environment = "staging"
  }

  assert {
    condition     = output.backup_policy.replica_region == null
    error_message = "staging must plan without a replica region."
  }
}

# ─── Data residency of all data locations (NFR-09) ─────────────────────────

run "eu_regions_accepted" {
  command = plan

  variables {
    region        = "eu-central-1"
    backup_region = "europe-west3"
    log_region    = "westeurope"
    search_region = "fsn1"
  }

  assert {
    condition = output.data_locations == {
      primary = "eu-central-1"
      backups = "europe-west3"
      logs    = "westeurope"
      search  = "fsn1"
    }
    error_message = "every EU data location must be planned as given."
  }
}

run "eu_region_defaults_follow_the_primary_region" {
  command = plan

  variables {
    region = "hel1"
  }

  assert {
    condition     = output.data_locations.backups == "hel1" && output.data_locations.logs == "hel1" && output.data_locations.search == "hel1"
    error_message = "backups, logs and the search index must default to the primary region."
  }
}

run "non_eu_region_rejected_us_east_1" {
  command = plan

  variables {
    region = "us-east-1"
  }

  expect_failures = [var.region]
}

# London and Zurich are outside the EU (AWS and Google Cloud).
run "non_eu_region_rejected_london" {
  command = plan

  variables {
    region = "eu-west-2"
  }

  expect_failures = [var.region]
}

run "non_eu_region_rejected_zurich" {
  command = plan

  variables {
    region = "eu-central-2"
  }

  expect_failures = [var.region]
}

run "non_eu_region_rejected_zurich_gcp" {
  command = plan

  variables {
    region = "europe-west6"
  }

  expect_failures = [var.region]
}

run "non_eu_backup_region_rejected" {
  command = plan

  variables {
    backup_region = "us-west-2"
  }

  expect_failures = [var.backup_region]
}

run "non_eu_log_region_rejected" {
  command = plan

  variables {
    log_region = "eastus"
  }

  expect_failures = [var.log_region]
}

run "non_eu_search_region_rejected" {
  command = plan

  variables {
    search_region = "switzerlandnorth"
  }

  expect_failures = [var.search_region]
}

run "non_eu_backup_replica_region_rejected" {
  command = plan

  variables {
    backup_replica_region = "us-east-1"
  }

  expect_failures = [var.backup_replica_region]
}

run "backup_retention_below_35_days_rejected" {
  command = plan

  variables {
    backup_retention_days = 30
  }

  expect_failures = [var.backup_retention_days]
}
