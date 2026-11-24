mock_provider "aws" {}

# An empty private testing repository accepts unique iteration tags without granting access.
run "private_testing_registry" {
  command = plan

  assert {
    condition = (
      aws_ecr_repository.development.name == "shiftbox-development" &&
      aws_ecr_repository.development.image_tag_mutability == "IMMUTABLE" &&
      aws_ecr_repository.development.force_delete == false &&
      length(aws_ecr_repository.development.encryption_configuration) == 1
    )
    error_message = "The development repository must be encrypted with immutable tags and refuse forced deletion."
  }
}
