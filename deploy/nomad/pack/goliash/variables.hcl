variable "job_name" {
  description = "The name of the Nomad job."
  type        = string
  default     = "goliash"
}

variable "namespace" {
  description = "The Nomad namespace to deploy into."
  type        = string
  default     = "default"
}

variable "datacenters" {
  description = "The datacenters to deploy to."
  type        = list(string)
  default     = ["*"]
}

variable "image" {
  description = "The Goliash server image. Pin a tag in production."
  type        = string
  default     = "ghcr.io/pipozzz/goliash:1.0.0"
}

variable "port" {
  description = "Host port for the web UI, API and agent endpoint."
  type        = number
  default     = 8070
}

variable "public_url" {
  description = "The address people use, e.g. https://goliash.example.com. Sign-in links and cookies depend on it. Empty = http://<node-ip>:<port>."
  type        = string
  default     = ""
}

variable "owner_email" {
  description = "E-mail of the first owner. Until they sign in, every start logs a one-time sign-in link in the task logs."
  type        = string
  default     = "admin@example.com"
}

variable "recovery_email" {
  description = "Locked out? Set to your e-mail and redeploy: every start logs a one-time sign-in link for you in the task logs. Empty it again afterwards."
  type        = string
  default     = ""
}

variable "environment" {
  description = "Environment the Nomad cluster belongs to in Goliash (e.g. prod or staging)."
  type        = string
  default     = "prod"
}

variable "watch_nomad" {
  description = "Watch the Nomad cluster this job runs on (read-only), without an agent."
  type        = bool
  default     = true
}

variable "nomad_address" {
  description = "Nomad API address for watching the cluster. Empty = http://<node-ip>:4646."
  type        = string
  default     = ""
}

variable "nomad_token" {
  description = "Nomad ACL token with the list-jobs and read-job capabilities, when ACLs are enabled. Empty = no token."
  type        = string
  default     = ""
}

variable "secret_key" {
  description = "Key that encrypts notification channel secrets (openssl rand -base64 32). Empty = generated on the data volume on first start."
  type        = string
  default     = ""
}

variable "github_token" {
  description = "GitHub token for release notes lookups (raises GitHub's rate limit). Optional."
  type        = string
  default     = ""
}

variable "data_volume" {
  description = "Named volume for the SQLite database and the secret key (/data)."
  type        = string
  default     = "goliash_data"
}

variable "constraints" {
  description = "Constraints to pin the job to the node holding the data volume."
  type = list(object({
    attribute = string
    operator  = string
    value     = string
  }))
  default = []
}

variable "resources" {
  description = "Task resources."
  type = object({
    cpu    = number
    memory = number
  })
  default = {
    cpu    = 200
    memory = 256
  }
}
