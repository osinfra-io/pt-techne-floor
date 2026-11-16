# Local Values
# https://opentofu.org/docs/language/values/locals

locals {
  project_id = "pt-bootstrap-${var.sandbox_folder_id}-${module.core_helpers.env}"

  services = toset([
    "billingbudgets.googleapis.com",
    "cloudbilling.googleapis.com",
    "cloudidentity.googleapis.com",
    "cloudkms.googleapis.com",
    "cloudresourcemanager.googleapis.com",
    "serviceusage.googleapis.com",
    "storage.googleapis.com"
  ])

  state_bucket_name = "${local.project_id}-state"
}
