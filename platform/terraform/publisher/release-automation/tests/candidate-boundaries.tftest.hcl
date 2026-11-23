mock_provider "aws" {}

variables {
  publisher_account_id = "111122223333"
}

# The delegated role creator can manage only the reserved phase identities.
# Only a phase-specific permissions boundary can be attached on creation.
run "candidate_phase_identity_scope" {
  command = plan
  assert {
    condition = alltrue([
      for phase in ["build", "copy", "share"] :
      anytrue([for grant in local.candidate_provision_access :
        contains(grant.Action, "iam:CreateRole") &&
        grant.Resource == "arn:aws:iam::111122223333:role/shiftbox-release/shiftbox-publisher-release-${phase}-*" &&
        try(grant.Condition.StringEquals["iam:PermissionsBoundary"], "") ==
        "arn:aws:iam::111122223333:policy/shiftbox-publisher-release-${phase}-boundary"
      ])
    ])
    error_message = "Candidate IAM creation must require the exact permissions boundary."
  }
  assert {
    condition = alltrue([
      for grant in local.candidate_cleanup_access :
      alltrue([for action in grant.Action : !can(regex("iam:(Create|Put|Update|Attach|Pass)", action))])
    ])
    error_message = "Independent cleanup must not grant phase permissions."
  }
}

# IAM resolves a name-only lookup before the phase role and its path exist.
# Only GetRole may use those pathless names, and the names must stay reserved.
run "phase_absence_lookup_scope" {
  command = plan
  assert {
    condition = alltrue([
      for grants in [local.candidate_provision_access, local.candidate_cleanup_access] :
      toset(try(one([for grant in grants : grant if grant.Sid == "CheckUncreatedPhaseRoles"]).Action, [])) == toset(["iam:GetRole"]) &&
      toset(try(one([for grant in grants : grant if grant.Sid == "CheckUncreatedPhaseRoles"]).Resource, [])) == toset([
        for phase in ["build", "copy", "share"] :
        "arn:aws:iam::111122223333:role/shiftbox-publisher-release-${phase}-????????????????"
      ])
    ])
    error_message = "Only GetRole can check for the absence of a reserved phase role."
  }
}

# IAM supplies the incoming role tags during the CreateRole TagRole check.
# Existing tags must match the request so a later request cannot retag ownership.
run "phase_tag_on_create_uses_the_incoming_identity" {
  command = plan
  assert {
    condition = alltrue([
      for grant in local.candidate_provision_access :
      !contains(grant.Action, "iam:TagRole") || try(
        grant.Condition.StringEqualsIfExists["aws:ResourceTag/release_phase"] == "$${aws:RequestTag/release_phase}" &&
        grant.Condition.StringEqualsIfExists["aws:ResourceTag/release_run_id"] == "$${aws:RequestTag/release_run_id}" &&
        !contains(keys(try(grant.Condition.Null, {})), "aws:ResourceTag/release_phase"),
      false)
    ])
    error_message = "Tag-on-create must work without granting ownership retagging."
  }
}

# Candidate phase boundaries separately limit build, private copy and share.
run "no_exposure_outside_share_boundary" {
  command = plan
  assert {
    condition = (
      alltrue([for boundary in aws_iam_policy.candidate_boundary : length(boundary.policy) <= 6144]) &&
      !strcontains(aws_iam_policy.candidate_boundary["build"].policy, "launchPermission") &&
      !strcontains(aws_iam_policy.candidate_boundary["copy"].policy, "ModifyImageAttribute") &&
      !strcontains(aws_iam_policy.candidate_boundary["share"].policy, "CopyImage") &&
      !strcontains(aws_iam_policy.candidate_boundary["share"].policy, "DisableImageBlockPublicAccess") &&
      strcontains(aws_iam_policy.candidate_boundary["share"].policy, "launchPermission")
    )
    error_message = "Phase boundaries must fit IAM limits and protect all publication control changes."
  }
}
