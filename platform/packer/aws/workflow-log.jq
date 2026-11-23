# Public Actions logs contain selected progress and error identifiers only.
# Terraform's messages, output values, resource IDs, and Packer's raw guest
# output can contain private data even when the command exits successfully.
def aws_diagnostics:
  (
    (scan("operation error (EC2|IAM|Lambda|CloudWatch Logs|Scheduler|S3|STS): ([A-Z][A-Za-z0-9]{1,63})[, ]") | join(" ")),
    (scan("not authorized to perform: ((?:ec2|iam|lambda|logs|scheduler|s3|sts):[A-Z][A-Za-z0-9]{1,63}) ") | .[0]),
    (scan("api error ([A-Za-z][A-Za-z0-9.]{1,63}):") | .[0]),
    (scan("An error occurred \\(([A-Za-z][A-Za-z0-9.]{1,63})\\) when calling the ([A-Z][A-Za-z0-9]{1,63}) operation") | reverse | join(" "))
  ) | if $tool=="AWS" then "AWS diagnostic: \(.)" else "\($tool) AWS diagnostic: \(.)" end;

def resource_progress($stage; $entry):
  ($entry.resource.addr | strings | select(test("\\A(aws_[a-z0-9_]+|terraform_data)\\.[a-z][a-z0-9_]*(\\[\"(first|second|third|fourth|final)\"\\])?\\z"))) as $address |
  ($entry.action | select(IN("create", "read", "update", "replace", "delete", "move", "import"))) as $action |
  ($entry.elapsed_seconds | if type=="number" and .>=0 and .<=86400 then " (\(floor)s)" else "" end) as $time |
  "Terraform \($stage) \($action) \($address)\($time)";

def terraform_log:
  if .type=="planned_change" then resource_progress("planned"; .change)
  elif .type=="apply_start" then resource_progress("started"; .hook)
  elif .type=="apply_progress" then resource_progress("running"; .hook)
  elif .type=="apply_complete" then resource_progress("finished"; .hook)
  elif .type=="apply_errored" then resource_progress("failed"; .hook)
  elif .type=="change_summary" then
    .changes | select(.operation|IN("plan", "apply", "destroy")) |
    select(all(.add, .change, .remove; type=="number" and .>=0 and .<=1000 and .==floor)) |
    "Terraform \(.operation) summary: \(.add) add, \(.change) change, \(.remove) destroy"
  elif .type=="diagnostic" and (.diagnostic.severity|IN("error", "warning")) then
    .diagnostic |
    ("Terraform \(.severity)\(.summary | if IN("Error acquiring the state lock", "Failed to install provider",
      "Failed to query available provider packages", "Invalid value for input variable", "Backend initialization required",
      "Missing required argument", "Unsupported argument", "Saved plan is stale", "Resource precondition failed",
      "Resource postcondition failed", "No valid credential sources found") then ": " + . else "" end)"),
    ([.summary, .detail] | map(strings) | join("\n") | aws_diagnostics)
  else empty end;

if $tool=="Terraform" then
  . as $line | (try fromjson catch null) |
  if type=="object" then try terraform_log catch empty else $line | aws_diagnostics end
elif $tool=="Packer" then
  (capture("^((==>[ \t]+)?[ \t]*amazon-ebs[.]shiftbox: )?ShiftBox Packer checkpoint: (?<checkpoint>publisher-preflight|guest-binary|recipe-validation|packer-build|guest-bootstrap|guest-toolchain|guest-agent|guest-seal|ami-creation|ami-verification)$") |
   "ShiftBox Packer checkpoint: \(.checkpoint)"),
  aws_diagnostics
elif $tool=="AWS" then aws_diagnostics
else empty end
