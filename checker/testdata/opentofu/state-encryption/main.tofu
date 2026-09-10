variable "passphrase" {
  type      = string
  default   = "correct-horse-battery-staple"
  sensitive = true
}

terraform {
  encryption {
    key_provider "pbkdf2" "main" {
      passphrase = var.passphrase
    }

    method "aes_gcm" "main" {
      keys = key_provider.pbkdf2.main
    }

    method "aes_gcm" "previous" {
      keys = key_provider.pbkdf2.main
    }

    state {
      method = method.aes_gcm.main

      fallback {
        method = method.aes_gcm.previous
      }
    }

    plan {
      method = method.aes_gcm.main

      fallback {
        method = method.aes_gcm.previous
      }
    }

    remote_state_data_sources {
      default {
        method = method.aes_gcm.main
      }

      remote_state_data_source "archive" {
        method = method.aes_gcm.previous
      }
    }
  }
}
