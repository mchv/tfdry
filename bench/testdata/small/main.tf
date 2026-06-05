terraform {
  required_version = ">= 1.0"
  required_providers {
    null = {
      source  = "hashicorp/null"
      version = "~> 3.2"
    }
  }
}

locals {
  env = "prod"
  tags = {
    Application = "demo"
    Environment = local.env
    Team        = "platform"
  }
}

resource "null_resource" "main" {
  triggers = merge(local.tags, {
    env  = local.env
    name = "demo-${local.env}"
  })
}

output "id" {
  value = null_resource.main.id
}
