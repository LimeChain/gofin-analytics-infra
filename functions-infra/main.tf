locals {
  project_id = "gofin-analytics-511817"
  region     = "europe-west1"
  providers = {
    uniswap = {
      upstream          = "https://trade-api.gateway.uniswap.org/v1"
      credential_header = "x-api-key"
      credential_prefix = ""
      forward_headers   = ["X-Universal-Router-Version", "X-Permit2-Disabled", "X-Erc20eth-Enabled", "X-Universal-Router-Swapsteps", "X-Api-Experimental"]
      routes = concat(
        [for path in ["/check_approval", "/quote", "/swap"] : { method = "POST", path = path }],
        [{ method = "GET", path = "/swaps" }],
      )
      query_injections = {}
    }
    relay = {
      upstream          = "https://api.relay.link"
      credential_header = "x-api-key"
      credential_prefix = ""
      forward_headers   = []
      routes = concat(
        [for path in ["/quote", "/quote/v2", "/currencies/v2"] : { method = "POST", path = path }],
        [for path in ["/chains", "/intents/status", "/intents/status/v3"] : { method = "GET", path = path }],
      )
      query_injections = {}
    }
    across = {
      upstream          = "https://app.across.to/api"
      credential_header = "Authorization"
      credential_prefix = "Bearer "
      forward_headers   = []
      routes            = [for path in ["/swap/approval", "/swap/chains", "/swap/tokens", "/swap/sources", "/suggested-fees", "/limits", "/available-routes", "/deposit/status"] : { method = "GET", path = path }]
      query_injections  = { integratorId = "/var/secrets/integrator/id" }
    }
  }
}

# One content-addressed archive is shared by all three deployments. The archive
# provider normalizes timestamps and permissions to make repeated plans stable.
data "archive_file" "source" {
  type             = "zip"
  source_dir       = "${path.module}/../function"
  output_path      = "${path.module}/.terraform/provider-source.zip"
  output_file_mode = "0644"
  excludes         = [".DS_Store"]
}
resource "google_storage_bucket_object" "source" {
  name   = "provider-${data.archive_file.source.output_sha256}.zip"
  bucket = "${local.project_id}-functions-source"
  source = data.archive_file.source.output_path
  lifecycle {
    create_before_destroy = true
  }
}

resource "google_logging_project_exclusion" "requests" {
  name        = "gofin-provider-request-privacy"
  description = "Exclude provider function request logs, including URLs and client IPs"
  filter      = <<-EOT
    resource.type="cloud_run_revision"
    (resource.labels.service_name="gofin-provider-uniswap" OR resource.labels.service_name="gofin-provider-relay" OR resource.labels.service_name="gofin-provider-across")
    logName="projects/${local.project_id}/logs/run.googleapis.com%2Frequests"
  EOT
}

module "provider" {
  for_each      = local.providers
  source        = "./modules/provider"
  project_id    = local.project_id
  region        = local.region
  name          = each.key
  config        = each.value
  source_bucket = google_storage_bucket_object.source.bucket
  source_object = google_storage_bucket_object.source.name
  depends_on    = [google_logging_project_exclusion.requests]
}

output "service_urls" { value = { for name, instance in module.provider : name => instance.url } }
output "source_sha256" { value = data.archive_file.source.output_sha256 }
