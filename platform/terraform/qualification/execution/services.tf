locals {
  cluster = format(local.regional, "ecs", "cluster/${local.run}")
  services = [
    for service in ["controller", "postgres"] : format(local.regional, "ecs", "service/${local.run}/${service}")
  ]
  task_definitions = [
    for family in ["controller", "postgres", "setup"] : format(local.regional, "ecs", "task-definition/${local.run}-${family}:*")
  ]
  log_group = format(local.regional, "logs", "log-group:/shiftbox/${local.run}/ecs")
  setup_doc = format(local.regional, "ssm", "document/${local.run}-SetupPort")
  secrets = [
    for purpose in ["Admin", "App", "Dex", "Tls"] : format(local.regional, "secretsmanager", "secret:${local.run}-Postgres${purpose}Secret-??????")
  ]
  service_statements = [
    {
      Effect    = "Allow"
      Action    = ["ecs:CreateCluster"]
      Resource  = [local.cluster]
      Condition = local.tagged_create
    },
    {
      Effect    = "Allow"
      Action    = ["ecs:DescribeClusters", "ecs:ListTagsForResource"]
      Resource  = [local.cluster]
      Condition = local.regional_reads
    },
    {
      Effect    = "Allow"
      Action    = ["ecs:DeleteCluster"]
      Resource  = [local.cluster]
      Condition = local.tagged_run
    },
    {
      Effect   = "Allow"
      Action   = ["ecs:RegisterTaskDefinition"]
      Resource = local.task_definitions
      Condition = {
        StringEquals         = merge(local.region_check, local.request_tags)
        StringEqualsIfExists = { "ecs:privileged" = "false" }
      }
    },
    {
      Effect    = "Allow"
      Action    = ["ecs:DescribeTaskDefinition", "ecs:ListTagsForResource"]
      Resource  = local.task_definitions
      Condition = local.regional_reads
    },
    {
      Effect    = "Allow"
      Action    = ["ecs:DeregisterTaskDefinition"]
      Resource  = local.task_definitions
      Condition = local.regional_reads
    },
    {
      Effect   = "Allow"
      Action   = ["ecs:CreateService"]
      Resource = local.services
      Condition = {
        StringEquals         = merge(local.region_check, local.request_tags)
        ArnEquals            = { "ecs:cluster" = local.cluster }
        ArnLike              = { "ecs:task-definition" = local.task_definitions }
        BoolIfExists         = { "ecs:auto-assign-public-ip" = "false" }
        StringEqualsIfExists = { "ecs:enable-execute-command" = "false" }
      }
    },
    {
      Effect   = "Allow"
      Action   = ["ecs:UpdateService"]
      Resource = local.services
      Condition = {
        StringEquals         = merge(local.region_check, local.resource_tags)
        ArnEquals            = { "ecs:cluster" = local.cluster }
        ArnLikeIfExists      = { "ecs:task-definition" = local.task_definitions }
        BoolIfExists         = { "ecs:auto-assign-public-ip" = "false" }
        StringEqualsIfExists = { "ecs:enable-execute-command" = "false" }
      }
    },
    {
      Effect    = "Allow"
      Action    = ["ecs:DescribeServices", "ecs:ListTagsForResource"]
      Resource  = local.services
      Condition = local.regional_reads
    },
    {
      Effect    = "Allow"
      Action    = ["ecs:DeleteService"]
      Resource  = local.services
      Condition = local.tagged_run
    },
    {
      Effect   = "Allow"
      Action   = ["ecs:TagResource"]
      Resource = concat([local.cluster], local.services, local.task_definitions)
      Condition = {
        StringEquals = merge(local.region_check, local.request_tags, {
          "ecs:CreateAction" = ["CreateCluster", "CreateService", "RegisterTaskDefinition"]
        })
      }
    },
    {
      Effect    = "Allow"
      Action    = ["logs:CreateLogGroup", "logs:TagResource"]
      Resource  = [local.log_group, "${local.log_group}:*"]
      Condition = local.tagged_create
    },
    {
      Effect    = "Allow"
      Action    = ["logs:DeleteLogGroup", "logs:PutRetentionPolicy"]
      Resource  = [local.log_group, "${local.log_group}:*"]
      Condition = local.tagged_run
    },
    {
      Effect    = "Allow"
      Action    = ["logs:DescribeLogGroups"]
      Resource  = ["*"]
      Condition = local.regional_reads
    },
    {
      Effect    = "Allow"
      Action    = ["logs:ListTagsForResource"]
      Resource  = [local.log_group]
      Condition = local.regional_reads
    },
    {
      Effect    = "Allow"
      Action    = ["secretsmanager:CreateSecret", "secretsmanager:TagResource"]
      Resource  = local.secrets
      Condition = local.tagged_create
    },
    {
      Effect    = "Allow"
      Action    = ["secretsmanager:DescribeSecret"]
      Resource  = local.secrets
      Condition = local.tagged_run
    },
    {
      Effect    = "Allow"
      Action    = ["secretsmanager:GetRandomPassword"]
      Resource  = ["*"]
      Condition = local.regional_reads
    },
    {
      Effect    = "Allow"
      Action    = ["ssm:CreateDocument", "ssm:AddTagsToResource"]
      Resource  = [local.setup_doc]
      Condition = local.tagged_create
    },
    {
      Effect    = "Allow"
      Action    = ["ssm:DescribeDocument", "ssm:ListTagsForResource"]
      Resource  = [local.setup_doc]
      Condition = local.regional_reads
    },
    {
      Effect    = "Allow"
      Action    = ["ssm:UpdateDocument", "ssm:UpdateDocumentDefaultVersion", "ssm:DeleteDocument"]
      Resource  = [local.setup_doc]
      Condition = local.tagged_run
    }
  ]
}
