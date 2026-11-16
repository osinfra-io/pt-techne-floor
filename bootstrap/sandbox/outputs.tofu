# Output Values
# https://opentofu.org/docs/language/values/outputs

output "kms_encryption_key" {
  description = "KMS key for encrypted state in subsequent sandbox bootstrap stages"
  value       = google_kms_crypto_key.state.id
}

output "project_id" {
  description = "The sandbox seed project ID; this root remains its sole owner"
  value       = google_project.bootstrap.project_id
}

output "state_bucket_name" {
  description = "Remote state bucket for subsequent sandbox bootstrap stages"
  value       = google_storage_bucket.state.name
}
