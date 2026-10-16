locals {
  project_id = "gofin-analytics-511817"
  region     = "europe-west1"
  services = toset([
    "artifactregistry.googleapis.com",
    "cloudresourcemanager.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "logging.googleapis.com",
    "run.googleapis.com",
    "sts.googleapis.com",
    "storage.googleapis.com",
  ])
}

resource "google_project_service" "required" {
  for_each           = local.services
  service            = each.value
  disable_on_destroy = false
}

resource "google_storage_bucket" "state" {
  for_each                    = toset(["bootstrap", "proxy"])
  name                        = "${local.project_id}-${each.key}-tfstate"
  location                    = local.region
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = false

  versioning {
    enabled = true
  }

  lifecycle_rule {
    action {
      type = "Delete"
    }
    condition {
      days_since_noncurrent_time = 30
      num_newer_versions         = 10
    }
  }

  lifecycle {
    prevent_destroy = true
  }

  depends_on = [google_project_service.required]
}

resource "google_artifact_registry_repository" "proxy" {
  location      = local.region
  repository_id = "analytics"
  description   = "GoFin analytics proxy images, retained for rollback"
  format        = "DOCKER"
  depends_on    = [google_project_service.required]
}

resource "google_service_account" "identity" {
  for_each     = toset(["runtime", "plan", "deploy"])
  account_id   = "analytics-${each.key}"
  display_name = "GoFin analytics ${each.key}"
  depends_on   = [google_project_service.required]
}

resource "google_iam_workload_identity_pool" "github" {
  workload_identity_pool_id = "gofin-github"
  display_name              = "GoFin analytics GitHub Actions"
  depends_on                = [google_project_service.required]
}

resource "google_iam_workload_identity_pool_provider" "github" {
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "github"
  attribute_mapping = {
    "google.subject"          = "assertion.sub"
    "attribute.repository_id" = "assertion.repository_id"
    "attribute.access"        = "assertion.ref == 'refs/heads/main' && assertion.event_name != 'pull_request' ? 'deploy' : 'plan'"
  }
  # Numeric IDs keep a renamed/deleted repository from transferring trust.
  attribute_condition = <<-EOT
    assertion.repository_owner_id == '37794962' &&
    assertion.repository_id == '1422479412' &&
    (assertion.event_name == 'pull_request' ||
      (assertion.ref == 'refs/heads/main' &&
       assertion.event_name in ['push', 'workflow_dispatch']))
  EOT
  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }
}

resource "google_service_account_iam_member" "github_plan" {
  service_account_id = google_service_account.identity["plan"].name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.repository_id/1422479412"
}

resource "google_service_account_iam_member" "github_deploy" {
  service_account_id = google_service_account.identity["deploy"].name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.access/deploy"
}

resource "google_project_iam_member" "plan_read" {
  for_each = toset(["roles/run.viewer", "roles/logging.viewer"])
  role     = each.value
  member   = google_service_account.identity["plan"].member
  project  = local.project_id
}

resource "google_project_iam_member" "deployment" {
  for_each = toset(["roles/run.admin", "roles/logging.configWriter"])
  project  = local.project_id
  role     = each.value
  member   = google_service_account.identity["deploy"].member
}

resource "google_service_account_iam_member" "act_as_runtime" {
  service_account_id = google_service_account.identity["runtime"].name
  role               = "roles/iam.serviceAccountUser"
  member             = google_service_account.identity["deploy"].member
}

resource "google_storage_bucket_iam_member" "proxy_state" {
  for_each = {
    plan   = "roles/storage.objectViewer"
    deploy = "roles/storage.objectAdmin"
  }
  bucket = google_storage_bucket.state["proxy"].name
  role   = each.value
  member = google_service_account.identity[each.key].member
}

resource "google_artifact_registry_repository_iam_member" "push" {
  location   = google_artifact_registry_repository.proxy.location
  repository = google_artifact_registry_repository.proxy.name
  role       = "roles/artifactregistry.writer"
  member     = google_service_account.identity["deploy"].member
}
