mock_provider "google" {}

override_module {
  target = module.core_helpers
  outputs = {
    env    = "sb"
    labels = {}
  }
}

run "sandbox_seed" {
  command = plan

  assert {
    condition     = google_project.bootstrap.folder_id == "648523111758"
    error_message = "The seed project must remain inside the existing sandbox folder."
  }

  assert {
    condition     = google_project.bootstrap.auto_create_network == false
    error_message = "The seed must not create an implicit default network."
  }

  assert {
    condition     = google_storage_bucket.state.uniform_bucket_level_access && google_storage_bucket.state.public_access_prevention == "enforced"
    error_message = "The remote state bucket must not allow public or object-ACL access."
  }
}

run "reject_other_folder" {
  command = plan

  variables {
    sandbox_folder_id = "123456789012"
  }

  expect_failures = [var.sandbox_folder_id]
}

run "reject_production_workspace" {
  command = plan

  override_module {
    target = module.core_helpers
    outputs = {
      env    = "prod"
      labels = {}
    }
  }

  expect_failures = [google_project.bootstrap]
}
