# State lives in Cloudflare R2, not in git (ADR 0002).
#
# Credentials are NOT here. Export before any terraform command:
#   AWS_ACCESS_KEY_ID      — R2 access key id
#   AWS_SECRET_ACCESS_KEY  — R2 secret access key
#
terraform {
  backend "s3" {
    bucket = "hetzner-cloud-infra"
    key    = "tfstate/workers.tfstate"
    region = "auto"

    endpoints = {
      s3 = "https://5621ce9e64f6881e8e6229573adf3b36.r2.cloudflarestorage.com"
    }

    # R2 is S3-compatible, but not AWS. These switch off the AWS-only checks.
    skip_credentials_validation = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    skip_s3_checksum            = true
    use_path_style              = true

    # Native S3 locking: a .tflock object written with a conditional PUT.
    # Replaces the DynamoDB table AWS would use. Needs Terraform >= 1.10.
    use_lockfile = true
  }
}
