output "workload_identity_provider" {
  value = google_iam_workload_identity_pool_provider.github.name
}

output "image_repository" {
  value = "${local.region}-docker.pkg.dev/${local.project_id}/${google_artifact_registry_repository.proxy.repository_id}/proxy"
}
