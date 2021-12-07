# Copyright 2021 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Terraform configuration for GCP components from this repo.

terraform {
  required_version = ">= 1.0.9, < 2.0.0"
  # Store terraform state in a GCS bucket, so all team members share it.
  backend "gcs" {
    bucket = "go-discovery-exp"
    prefix = "vuln"
  }
  required_providers {
    google = {
      version = "~> 3.86.0"
      source  = "hashicorp/google"
    }
  }
}

locals {
  region            = "us-central1"
}

provider "google" {
#  project = local.project
  region  = local.region
}


# Deployment environments

module "dev" {
  source                    = "./environment"
  env                       = "dev"
  project                   = "go-discovery-exp"
  region                    = local.region
  use_profiler              = false
  min_frontend_instances    = 0
}

module "prod" {
  source                    = "./environment"
  env                       = "prod"
  project                   = "golang-org"
  region                    = local.region
  use_profiler              = true
  min_frontend_instances    = 1
}

