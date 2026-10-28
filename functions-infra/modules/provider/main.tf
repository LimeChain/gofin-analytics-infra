variable "project_id" { type = string }
variable "region" { type = string }
variable "name" { type = string }
variable "source_bucket" { type = string }
variable "source_object" { type = string }
variable "config" {
  type = object({
    upstream          = string
    credential_header = string
    credential_prefix = string
    forward_headers   = optional(list(string), [])
    routes            = list(object({ method = string, path = string }))
    query_injections  = map(string)
  })
}

resource "google_cloudfunctions2_function" "provider" {
  name        = "gofin-provider-${var.name}"
  location    = var.region
  description = "GoFin ${var.name} provider proxy"
  build_config {
    runtime         = "go126"
    entry_point     = "Proxy"
    service_account = "projects/${var.project_id}/serviceAccounts/provider-build@${var.project_id}.iam.gserviceaccount.com"
    source {
      storage_source {
        bucket = var.source_bucket
        object = var.source_object
      }
    }
  }
  service_config {
    available_memory                 = "256M"
    available_cpu                    = "1"
    timeout_seconds                  = 30
    min_instance_count               = 0
    max_instance_count               = 3
    max_instance_request_concurrency = 80
    service_account_email            = "provider-${var.name}@${var.project_id}.iam.gserviceaccount.com"
    ingress_settings                 = "ALLOW_ALL"
    environment_variables = {
      PROXY_CONFIG = jsonencode(merge(var.config, { credential_file = "/var/secrets/provider/api-key" }))
    }
    secret_volumes {
      mount_path = "/var/secrets/provider"
      project_id = var.project_id
      secret     = "provider-${var.name}-api-key"
      versions {
        version = "latest"
        path    = "api-key"
      }
    }
    dynamic "secret_volumes" {
      for_each = var.name == "across" ? [1] : []
      content {
        mount_path = "/var/secrets/integrator"
        project_id = var.project_id
        secret     = "provider-across-integrator-id"
        versions {
          version = "latest"
          path    = "id"
        }
      }
    }
  }
}

resource "google_cloud_run_v2_service_iam_member" "public" {
  project  = var.project_id
  location = var.region
  name     = google_cloudfunctions2_function.provider.name
  role     = "roles/run.invoker"
  member   = "allUsers"
}
output "url" { value = google_cloudfunctions2_function.provider.service_config[0].uri }
