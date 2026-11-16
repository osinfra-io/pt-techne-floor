# Input Variables
# https://opentofu.org/docs/language/values/variables

variable "billing_account_id" {
  default     = "01C550-A2C86B-B8F16B"
  description = "Existing billing account used by Logos and Corpus"
  type        = string

  validation {
    condition     = can(regex("^[A-Z0-9]{6}-[A-Z0-9]{6}-[A-Z0-9]{6}$", var.billing_account_id))
    error_message = "Provide a billing account ID in XXXXXX-XXXXXX-XXXXXX format."
  }
}

variable "sandbox_folder_id" {
  default     = "648523111758"
  description = "Existing sandbox environment folder; this root never creates or owns it"
  type        = string

  validation {
    condition     = var.sandbox_folder_id == "648523111758"
    error_message = "This bootstrap configuration is restricted to sandbox folder 648523111758."
  }
}
