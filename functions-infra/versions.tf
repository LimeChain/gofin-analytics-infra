terraform {
  required_version = ">= 1.10, < 2.0"
  backend "gcs" {
    bucket = "gofin-analytics-511817-functions-tfstate"
    prefix = "functions"
  }
  required_providers {
    google  = { source = "hashicorp/google", version = "~> 8.7.0" }
    archive = { source = "hashicorp/archive", version = "~> 2.7" }
  }
}
provider "google" {
  project = "gofin-analytics-511817"
  region  = "europe-west1"
}
