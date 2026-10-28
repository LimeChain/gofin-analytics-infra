# Provider proxy infrastructure is isolated from the analytics runtime and state.
resource "google_project_service" "provider_apis" {
  for_each = toset([
    "cloudbuild.googleapis.com",
    "cloudfunctions.googleapis.com",
    "eventarc.googleapis.com",
    "secretmanager.googleapis.com",
  ])
  service            = each.value
  disable_on_destroy = false
}

resource "google_storage_bucket" "functions_state" {
  name                        = "${local.project_id}-functions-tfstate"
  location                    = local.region
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = false
  versioning { enabled = true }
  lifecycle { prevent_destroy = true }
}

resource "google_storage_bucket" "functions_source" {
  name                        = "${local.project_id}-functions-source"
  location                    = local.region
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = false
  versioning { enabled = true }
  lifecycle { prevent_destroy = true }
}

resource "google_artifact_registry_repository" "functions" {
  location      = local.region
  repository_id = "gcf-artifacts"
  description   = "Cloud Functions build images"
  format        = "DOCKER"
}

resource "google_service_account" "provider" {
  for_each     = toset(["uniswap", "relay", "across", "build"])
  account_id   = "provider-${each.key}"
  display_name = "GoFin provider ${each.key}"
}

resource "google_secret_manager_secret" "provider" {
  for_each  = toset(["uniswap-api-key", "relay-api-key", "across-api-key", "across-integrator-id"])
  secret_id = "provider-${each.key}"
  replication {
    auto {}
  }
  lifecycle { prevent_destroy = true }
  depends_on = [google_project_service.provider_apis]
}

resource "google_secret_manager_secret_iam_member" "runtime" {
  for_each = {
    uniswap   = "uniswap-api-key"
    relay     = "relay-api-key"
    across    = "across-api-key"
    across_id = "across-integrator-id"
  }
  secret_id = google_secret_manager_secret.provider[each.value].id
  role      = "roles/secretmanager.secretAccessor"
  member    = google_service_account.provider[each.key == "across_id" ? "across" : each.key].member
}

resource "google_storage_bucket_iam_member" "functions_state" {
  for_each = { plan = "roles/storage.objectViewer", deploy = "roles/storage.objectAdmin" }
  bucket   = google_storage_bucket.functions_state.name
  role     = each.value
  member   = google_service_account.identity[each.key].member
}

resource "google_storage_bucket_iam_member" "functions_source" {
  for_each = { plan = "roles/storage.objectViewer", deploy = "roles/storage.objectAdmin", build = "roles/storage.objectViewer" }
  bucket   = google_storage_bucket.functions_source.name
  role     = each.value
  member   = each.key == "build" ? google_service_account.provider["build"].member : google_service_account.identity[each.key].member
}

resource "google_service_account_iam_member" "provider_act_as" {
  for_each           = google_service_account.provider
  service_account_id = each.value.name
  role               = "roles/iam.serviceAccountUser"
  member             = google_service_account.identity["deploy"].member
}

resource "google_project_iam_member" "functions_plan" {
  for_each = toset(["roles/cloudfunctions.viewer", "roles/serviceusage.serviceUsageViewer"])
  project  = local.project_id
  role     = each.value
  member   = google_service_account.identity["plan"].member
}

resource "google_project_iam_member" "functions_deploy" {
  project = local.project_id
  role    = "roles/cloudfunctions.developer"
  member  = google_service_account.identity["deploy"].member
}

resource "google_project_iam_member" "functions_build" {
  project = local.project_id
  role    = "roles/logging.logWriter"
  member  = google_service_account.provider["build"].member
}

# Functions copies source into this service-managed bucket before Cloud Build reads it.
resource "google_project_iam_member" "functions_build_staging" {
  project = local.project_id
  role    = "roles/storage.objectViewer"
  member  = google_service_account.provider["build"].member
  condition {
    title       = "functions-source-staging-only"
    expression  = "resource.name.startsWith('projects/_/buckets/gcf-v2-sources-722904627079-europe-west1/')"
    description = "Read only the regional Cloud Functions source staging bucket"
  }
}

resource "google_artifact_registry_repository_iam_member" "functions_build" {
  location   = google_artifact_registry_repository.functions.location
  repository = google_artifact_registry_repository.functions.name
  role       = "roles/artifactregistry.writer"
  member     = google_service_account.provider["build"].member
}
