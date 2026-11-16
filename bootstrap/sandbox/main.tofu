# Google KMS Crypto Key
# https://search.opentofu.org/provider/hashicorp/google/latest/docs/resources/kms_crypto_key

resource "google_kms_crypto_key" "state" {
  lifecycle {
    prevent_destroy = true
  }

  key_ring        = google_kms_key_ring.state.id
  labels          = module.core_helpers.labels
  name            = "default"
  rotation_period = "7776000s"
}

# Google KMS Key Ring
# https://search.opentofu.org/provider/hashicorp/google/latest/docs/resources/kms_key_ring

resource "google_kms_key_ring" "state" {
  depends_on = [
    google_project_service.bootstrap
  ]

  lifecycle {
    prevent_destroy = true
  }

  location = "us"
  name     = "state-encryption"
  project  = google_project.bootstrap.project_id
}

# Google Project
# https://search.opentofu.org/provider/hashicorp/google/latest/docs/resources/google_project

resource "google_project" "bootstrap" {
  lifecycle {
    prevent_destroy = true

    precondition {
      condition     = module.core_helpers.env == "sb"
      error_message = "The bootstrap root may run only in a sandbox workspace."
    }
  }

  auto_create_network = false
  billing_account     = var.billing_account_id
  deletion_policy     = "PREVENT"
  folder_id           = var.sandbox_folder_id
  labels              = module.core_helpers.labels
  name                = "Sandbox bootstrap"
  project_id          = local.project_id
}

# Google Project Service
# https://search.opentofu.org/provider/hashicorp/google/latest/docs/resources/google_project_service

resource "google_project_service" "bootstrap" {
  for_each = local.services

  disable_on_destroy = false
  project            = google_project.bootstrap.project_id
  service            = each.key
}

# Google Storage Bucket
# https://search.opentofu.org/provider/hashicorp/google/latest/docs/resources/storage_bucket

resource "google_storage_bucket" "state" {
  depends_on = [
    google_project_service.bootstrap
  ]

  lifecycle {
    prevent_destroy = true
  }

  force_destroy               = false
  labels                      = module.core_helpers.labels
  location                    = "US"
  name                        = local.state_bucket_name
  project                     = google_project.bootstrap.project_id
  public_access_prevention    = "enforced"
  uniform_bucket_level_access = true

  versioning {
    enabled = true
  }
}
