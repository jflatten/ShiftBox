mock_provider "aws" {}

variables {
  publisher_account_id = "111122223333"
  vpc_cidr             = "10.42.0.0/24"
  candidate_tag        = "v1.0.0-rc.1"
  availability_zone_id = "use2-az1"
  packer_public_key    = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA fixture"
  attempt_id           = "rel-00000000000001"
}

# A GitHub-owned attempt requires the exact account's pre-approved boundary.
run "missing_boundary" {
  command         = plan
  expect_failures = [var.expiry_permissions_boundary_arn]
}

# Both expiry roles stay confined by the one-time bootstrap for this account.
run "bounded_expiry_roles" {
  command = plan
  variables {
    expiry_permissions_boundary_arn = "arn:aws:iam::111122223333:policy/shiftbox-publisher-release-expiry-boundary"
  }

  assert {
    condition = (
      aws_iam_role.expiry.permissions_boundary == var.expiry_permissions_boundary_arn &&
      aws_iam_role.expiry_scheduler.permissions_boundary == var.expiry_permissions_boundary_arn
    )
    error_message = "Both automated expiry identities require the publisher-owned boundary."
  }
}

# A policy with the right name in another account cannot authorize a release.
run "cross_account_boundary" {
  command = plan
  variables {
    expiry_permissions_boundary_arn = "arn:aws:iam::111122223334:policy/shiftbox-publisher-release-expiry-boundary"
  }
  expect_failures = [var.expiry_permissions_boundary_arn]
}

# The rehearsal resources use the same expiry engine but require its reserved
# label and the production boundary. An ordinary RC cannot enter this namespace.
run "bounded_network_rehearsal" {
  command = plan
  variables {
    attempt_id                      = "rel-r0000000000001"
    candidate_tag                   = "v0.0.0-rc.1"
    expiry_permissions_boundary_arn = "arn:aws:iam::111122223333:policy/shiftbox-publisher-release-expiry-boundary"
  }

  assert {
    condition = (
      aws_iam_role.expiry.name == "shiftbox-publisher-rel-r0000000000001-expiry" &&
      aws_iam_role.expiry.permissions_boundary == var.expiry_permissions_boundary_arn &&
      local.tags.environment == "release-v0.0.0-rc.1"
    )
    error_message = "The nonpublishing attempt must stay in its own named namespace."
  }
}

# A candidate label cannot be used to create rehearsal resources.
run "reject_candidate_in_rehearsal_namespace" {
  command = plan
  variables {
    attempt_id                      = "rel-r0000000000001"
    expiry_permissions_boundary_arn = "arn:aws:iam::111122223333:policy/shiftbox-publisher-release-expiry-boundary"
  }
  expect_failures = [var.expiry_permissions_boundary_arn]
}
