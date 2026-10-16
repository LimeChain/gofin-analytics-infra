locals {
  project_id   = "gofin-analytics-511817"
  region       = "europe-west1"
  service_name = "gofin-analytics-proxy"
}

variable "image" {
  description = "Immutable Artifact Registry image reference (including sha256 digest)."
  type        = string
  validation {
    condition     = can(regex("^europe-west1-docker\\.pkg\\.dev/gofin-analytics-511817/analytics/proxy@sha256:[a-f0-9]{64}$", var.image))
    error_message = "Use a sha256 digest from this project's proxy image repository."
  }
}

resource "google_logging_project_exclusion" "requests" {
  name        = "gofin-analytics-request-privacy"
  description = "Do not retain proxy request metadata (including client IP) in the project's default log sink."
  filter      = <<-EOT
    resource.type="cloud_run_revision"
    resource.labels.service_name="${local.service_name}"
    logName="projects/${local.project_id}/logs/run.googleapis.com%2Frequests"
  EOT
}

resource "google_cloud_run_v2_service" "proxy" {
  name                 = local.service_name
  location             = local.region
  description          = "Forward-only GoFin analytics proxy to PostHog EU"
  deletion_protection  = true
  ingress              = "INGRESS_TRAFFIC_ALL"
  invoker_iam_disabled = true

  labels = {
    application = "gofin"
    component   = "analytics"
    managed_by  = "terraform"
  }

  scaling {
    min_instance_count = 0
    max_instance_count = 3
  }

  template {
    service_account                  = "analytics-runtime@${local.project_id}.iam.gserviceaccount.com"
    execution_environment            = "EXECUTION_ENVIRONMENT_GEN2"
    max_instance_request_concurrency = 80
    timeout                          = "10s"

    containers {
      image = var.image
      ports {
        container_port = 8080
      }
      env {
        name  = "POSTHOG_UPSTREAM"
        value = "https://eu.i.posthog.com"
      }
      resources {
        limits = {
          cpu    = "1"
          memory = "512Mi"
        }
        cpu_idle          = true
        startup_cpu_boost = false
      }
      startup_probe {
        initial_delay_seconds = 0
        timeout_seconds       = 1
        period_seconds        = 3
        failure_threshold     = 10
        http_get {
          path = "/healthz"
          port = 8080
        }
      }
    }
  }

  traffic {
    type    = "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST"
    percent = 100
  }

  depends_on = [google_logging_project_exclusion.requests]
}

output "service_url" {
  value = google_cloud_run_v2_service.proxy.uri
}

output "deployed_image" {
  value = var.image
}
