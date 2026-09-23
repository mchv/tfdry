locals {
  value = {
    base = true
  }
}

output "effective" {
  value = "prefix-${local.value}"
}
