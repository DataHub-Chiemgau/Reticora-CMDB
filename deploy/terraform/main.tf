# Reticora CMDB — Terraform Module
#
# This is an empty module placeholder for infrastructure provisioning.
# To be implemented when deploying to a cloud provider.

terraform {
  required_version = ">= 1.5.0"
}

variable "environment" {
  description = "Deployment environment (development, staging, production)"
  type        = string
  default     = "development"
}

variable "region" {
  description = "Cloud provider region"
  type        = string
  default     = "eu-central-1"
}

output "note" {
  value = "Terraform module placeholder — configure provider and resources for your target cloud."
}
