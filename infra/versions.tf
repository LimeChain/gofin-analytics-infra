terraform {
  required_version = ">= 1.10, < 2.0"
  backend "gcs" {
    bucket = "gofin-analytics-511817-proxy-tfstate"
    prefix = "proxy"
  }
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.7.0"
    }
  }
}

provider "google" {
  project = "gofin-analytics-511817"
  region  = "europe-west1"
}
