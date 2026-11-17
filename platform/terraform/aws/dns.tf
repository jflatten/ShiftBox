variable "dns_suffix" {
  description = "Private DNS suffix for the browser, workspace guest, and database names."
  type        = string
  default     = null
  validation {
    condition     = var.dns_suffix == null || can(regex("^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$", var.dns_suffix))
    error_message = "Use a lowercase DNS suffix with at least two labels and no trailing dot."
  }
}

resource "aws_service_discovery_private_dns_namespace" "deployment" {
  count = local.services_enabled ? 1 : 0
  name  = "services.${var.dns_suffix}"
  vpc   = aws_vpc.shiftbox.id

  lifecycle {
    create_before_destroy = true
  }
}

# The Route 53 zone created by Cloud Map does not inherit namespace tags.
resource "terraform_data" "discovery_zone_tags" {
  count = local.services_enabled ? 1 : 0
  triggers_replace = {
    zone    = aws_service_discovery_private_dns_namespace.deployment[0].hosted_zone
    cluster = var.name
  }

  provisioner "local-exec" {
    interpreter = ["/bin/sh", "-c"]
    command     = "aws route53 change-tags-for-resource --resource-type hostedzone --resource-id \"$SHIFTBOX_DISCOVERY_ZONE\" --add-tags Key=project,Value=shiftbox \"Key=cluster_name,Value=$SHIFTBOX_CLUSTER\""
    environment = {
      SHIFTBOX_DISCOVERY_ZONE = self.triggers_replace.zone
      SHIFTBOX_CLUSTER        = self.triggers_replace.cluster
    }
  }
}

resource "aws_service_discovery_service" "service" {
  for_each = local.services_enabled ? toset(["shiftbox", "postgres"]) : toset([])
  name     = each.key
  dns_config {
    namespace_id   = aws_service_discovery_private_dns_namespace.deployment[0].id
    routing_policy = "MULTIVALUE"
    dns_records {
      type = "A"
      ttl  = 10
    }
  }
  lifecycle {
    create_before_destroy = true
  }
}

# Cloud Map owns its discovery zone and does not allow direct record changes.
resource "aws_route53_zone" "deployment" {
  count = local.services_enabled ? 1 : 0
  name  = var.dns_suffix

  vpc {
    vpc_id = aws_vpc.shiftbox.id
  }
}

resource "aws_route53_record" "controller_names" {
  for_each = local.services_enabled ? {
    postgres    = "postgres"
    shiftbox    = "shiftbox"
    guest       = "shiftbox"
    "*.ide"     = "shiftbox"
    "*.preview" = "shiftbox"
    "*.repair"  = "shiftbox"
  } : {}
  zone_id = aws_route53_zone.deployment[0].zone_id
  name    = "${each.key}.${var.dns_suffix}"
  type    = "CNAME"
  ttl     = 10
  records = ["${each.value}.${aws_service_discovery_private_dns_namespace.deployment[0].name}"]
}

output "management_origin" {
  description = "Private browser origin to use with the deployer-managed connection."
  value       = local.services_enabled ? "https://shiftbox.${var.dns_suffix}:${local.browser_port}" : null
}
