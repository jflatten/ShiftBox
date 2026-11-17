#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
if [ -n "${SHIFTBOX_TERRAFORM_STATE:-}" ]; then
    settings=$(terraform -chdir="$root" output -state="$SHIFTBOX_TERRAFORM_STATE" -json setup)
else
    settings=$(terraform -chdir="$root" output -json setup)
fi
cluster=$(printf '%s' "$settings" | jq -er .cluster)
region=$(printf '%s' "$settings" | jq -er .region)
definition=$(printf '%s' "$settings" | jq -er .task_definition)
subnet=$(printf '%s' "$settings" | jq -er .subnet)
group=$(printf '%s' "$settings" | jq -er .security_group)
document=$(printf '%s' "$settings" | jq -er .document)
logs=$(printf '%s' "$settings" | jq -er .log_group)
tag=$(printf '%s' "$settings" | jq -er .tag)

services=$(aws ecs describe-services --region "$region" --cluster "$cluster" --services postgres controller)
if ! printf '%s' "$services" | jq -e '
    any(.services[]; .serviceName == "postgres" and .runningCount == 1) and
    any(.services[]; .serviceName == "controller" and .desiredCount == 0 and .runningCount == 0 and .pendingCount == 0)
' >/dev/null; then
    echo 'Setup requires PostgreSQL running and the normal controller stopped.' >&2
    exit 1
fi

task=$(aws ecs run-task --region "$region" --cluster "$cluster" --launch-type EC2 \
    --task-definition "$definition" --enable-execute-command --propagate-tags TASK_DEFINITION \
    --network-configuration "awsvpcConfiguration={subnets=[$subnet],securityGroups=[$group],assignPublicIp=DISABLED}" \
    --tags "key=ShiftBoxBootstrap,value=$tag" --query 'tasks[0].taskArn' --output text)
if [ -z "$task" ] || [ "$task" = None ]; then
    echo 'ECS could not place the setup task. Check the host and its mounted storage.' >&2
    exit 1
fi
task_id=${task##*/}
# shellcheck disable=SC2329 # Called by the exit trap.
stop_setup() {
    aws ecs stop-task --region "$region" --cluster "$cluster" --task "$task" >/dev/null ||
        printf 'Stop setup task %s before enabling the controller.\n' "$task_id" >&2
}
trap 'stop_setup' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
aws ecs wait tasks-running --region "$region" --cluster "$cluster" --tasks "$task"

# Wait for both the ECS session agent and the HTTPS listener fingerprint.
for _ in $(seq 1 45); do
    tasks=$(aws ecs describe-tasks --region "$region" --cluster "$cluster" --tasks "$task")
    runtime=$(printf '%s' "$tasks" | jq -r '
      .tasks[0].containers[] | select(.name == "setup")
      | select(any(.managedAgents[]?; .name == "ExecuteCommandAgent" and .lastStatus == "RUNNING"))
      | .runtimeId
    ')
    # shellcheck disable=SC2016 # JMESPath uses literal backticks.
    status=$(aws logs get-log-events --region "$region" --log-group-name "$logs" \
        --log-stream-name "ecs/setup/$task_id" --query 'events[?starts_with(message, `Verify the setup certificate SHA-256 fingerprint:`) || starts_with(message, `Open:`)].message' \
        --output text 2>/dev/null || true)
    if [ -n "$runtime" ] && [ -n "$status" ] && [ "$status" != None ]; then
        printf '%s\n' "$status"
        printf '%s\n' 'Complete the wizard in your browser, then close this tunnel to stop the setup task.'
        aws ssm start-session --region "$region" --target "ecs:${cluster}_${task_id}_${runtime}" --document-name "$document"
        exit
    fi
    if printf '%s' "$tasks" | jq -e '.tasks[0].lastStatus == "STOPPED"' >/dev/null; then
        echo "The setup task stopped. Check $logs / ecs/setup/$task_id." >&2
        exit 1
    fi
    sleep 2
done
echo 'The protected setup listener did not become ready.' >&2
exit 1
