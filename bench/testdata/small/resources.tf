resource "null_resource" "secondary" {
  triggers = {
    env = local.env
  }
}

resource "null_resource" "tertiary" {
  triggers = {
    name = "${local.env}-tertiary"
  }
}

output "secondary_id" {
  value = null_resource.secondary.id
}

output "tertiary_id" {
  value = null_resource.tertiary.id
}
