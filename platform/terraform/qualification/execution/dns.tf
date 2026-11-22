locals {
  private_suffix = "${local.run}.internal"
  app_zone       = "arn:aws:route53:::hostedzone/${coalesce(var.verified_private_zone_id, "unverified")}"
  discovery_zone = "arn:aws:route53:::hostedzone/${coalesce(var.verified_discovery_zone_id, "unverified")}"
  zone_arns      = concat(var.verified_private_zone_id == null ? [] : [local.app_zone], var.verified_discovery_zone_id == null ? [] : [local.discovery_zone])
  dns_record_keys = {
    "route53:ChangeResourceRecordSetsNormalizedRecordNames" = [
      "postgres.${local.private_suffix}", "shiftbox.${local.private_suffix}", "guest.${local.private_suffix}",
      "\\052.ide.${local.private_suffix}", "\\052.preview.${local.private_suffix}", "\\052.repair.${local.private_suffix}"
    ]
    "route53:ChangeResourceRecordSetsRecordTypes" = ["CNAME"]
    "route53:ChangeResourceRecordSetsActions"     = ["CREATE", "UPSERT", "DELETE"]
  }
  dns_statements = concat(
    [
      {
        Effect   = "Allow"
        Action   = ["route53:CreateHostedZone"]
        Resource = ["*"]
        Condition = {
          StringEquals = { "route53:VPCs" = "VPCId=${coalesce(var.verified_vpc_id, "unverified")},VPCRegion=${local.region}" }
        }
      },
      {
        Effect   = "Allow"
        Action   = ["route53:GetChange"]
        Resource = ["arn:aws:route53:::change/*"]
      }
    ],
    length(local.zone_arns) == 0 ? [] : [
      {
        Effect   = "Allow"
        Action   = ["route53:GetHostedZone", "route53:ListTagsForResource"]
        Resource = local.zone_arns
      }
    ],
    [for statement in [
      {
        Effect   = "Allow"
        Action   = ["route53:DeleteHostedZone", "route53:ChangeTagsForResource", "route53:ListResourceRecordSets"]
        Resource = [local.app_zone]
      },
      {
        Effect   = "Allow"
        Action   = ["route53:ChangeResourceRecordSets"]
        Resource = [local.app_zone]
        Condition = {
          "ForAllValues:StringEquals" = local.dns_record_keys
          Null                        = { for key in keys(local.dns_record_keys) : key => "false" }
        }
      }
    ] : statement if var.verified_private_zone_id != null],
    var.verified_discovery_zone_id == null ? [] : [
      {
        Effect    = "Allow"
        Action    = ["route53:DeleteHostedZone"]
        Resource  = [local.discovery_zone]
        Condition = { StringEquals = { "aws:CalledViaLast" = "servicediscovery.amazonaws.com" } }
      }
    ]
  )
  namespace_arn      = format(local.regional, "servicediscovery", "namespace/${coalesce(var.verified_namespace_id, "unverified")}")
  discovery_services = format(local.regional, "servicediscovery", "service/srv-*")
  verified_services = [
    for service in var.verified_discovery_service_ids : format(local.regional, "servicediscovery", "service/${service}")
  ]
  discovery_statements = concat(
    [
      {
        Effect    = "Allow"
        Action    = ["servicediscovery:CreatePrivateDnsNamespace"]
        Resource  = ["*"]
        Condition = local.tagged_create
      },
      {
        Effect    = "Allow"
        Action    = ["servicediscovery:GetOperation"]
        Resource  = ["*"]
        Condition = local.regional_reads
      }
    ],
    [for statement in [
      {
        Effect    = "Allow"
        Action    = ["servicediscovery:GetNamespace", "servicediscovery:DeleteNamespace"]
        Resource  = [local.namespace_arn]
        Condition = local.tagged_run
      },
      {
        Effect   = "Allow"
        Action   = ["servicediscovery:CreateService"]
        Resource = [local.namespace_arn, local.discovery_services]
        Condition = {
          StringEquals = merge(local.region_check, local.request_tags)
          ArnEquals    = { "servicediscovery:NamespaceArn" = local.namespace_arn }
        }
      },
      {
        Effect    = "Allow"
        Action    = ["servicediscovery:ListTagsForResource"]
        Resource  = concat([local.namespace_arn], local.verified_services)
        Condition = local.regional_reads
      },
      {
        Effect    = "Allow"
        Action    = ["servicediscovery:TagResource"]
        Resource  = concat([local.namespace_arn], local.verified_services)
        Condition = local.tagged_create
      }
    ] : statement if var.verified_namespace_id != null],
    length(local.verified_services) == 0 ? [] : [
      {
        Effect    = "Allow"
        Action    = ["servicediscovery:GetService", "servicediscovery:UpdateService", "servicediscovery:DeleteService"]
        Resource  = local.verified_services
        Condition = local.tagged_run
      }
    ]
  )
}
