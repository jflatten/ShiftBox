mock_provider "aws" {
  mock_resource "aws_instance" {
    defaults = { iam_instance_profile = "" }
  }
  mock_data "aws_caller_identity" {
    defaults = { account_id = "111122223333" }
  }

  mock_data "aws_ami" {
    defaults = {
      id                  = "ami-0123456789abcdef0"
      owner_id            = "999988887777"
      public              = true
      architecture        = "x86_64"
      root_device_type    = "ebs"
      virtualization_type = "hvm"
    }
  }
}

variables {
  customer_account_id  = "111122223333"
  publisher_account_id = "999988887777"
  aws_region           = "us-east-2"
  availability_zone    = "us-east-2a"
  ami_id               = "ami-0123456789abcdef0"
  name                 = "shiftbox-public-ami-fixture"
}

# A disposable customer launch must have an encrypted removable root, with no public IP or role.
run "private_encrypted_customer_launch" {
  # The mock provider fills the computed empty IAM profile after a fake apply.
  command = apply

  assert {
    condition     = aws_instance.validation.ami == var.ami_id && !aws_instance.validation.associate_public_ip_address
    error_message = "The probe must launch the specified AMI with only private addressing."
  }

  assert {
    condition     = aws_instance.validation.root_block_device[0].encrypted && aws_instance.validation.root_block_device[0].delete_on_termination
    error_message = "The probe root must be encrypted and deleted with the instance."
  }

  assert {
    condition     = aws_instance.validation.iam_instance_profile == "" && aws_instance.validation.metadata_options[0].http_tokens == "required"
    error_message = "The probe must use IMDSv2 and has no instance role."
  }
}

# Explicit customer and publisher account separation is necessary for public launch evidence.
run "reject_publisher_as_customer" {
  command = plan

  variables {
    customer_account_id  = "999988887777"
    publisher_account_id = "999988887777"
  }

  expect_failures = [aws_instance.validation]
}

# A private or foreign artifact cannot qualify for a public launch probe.
run "reject_ineligible_image" {
  command = plan

  override_data {
    target = data.aws_ami.official
    values = { public = false, owner_id = "222233334444" }
  }

  expect_failures = [aws_instance.validation]
}
