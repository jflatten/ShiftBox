#!/bin/sh
set -eu
umask 077
export AWS_PAGER=''

fail() {
    printf '%s\n' "$1" >&2
    exit 1
}

if [ "$#" -ne 3 ]; then
    printf 'Usage: setup.sh <stack-id> <region> <account-id>\n' >&2
    exit 2
fi
stack_id=$1
region=$2
account=$3
case "$region" in
    us-east-1|us-east-2|us-west-1|us-west-2) ;;
    *) fail 'Choose a supported ShiftBox Region.' ;;
esac
case "$account" in ''|*[!0-9]*) fail 'Provide the reviewed AWS account ID.' ;; esac
[ "${#account}" -eq 12 ] || fail 'Provide the reviewed AWS account ID.'
case "$stack_id" in
    "arn:aws:cloudformation:$region:$account:stack/"*/*) ;;
    *) fail 'Provide the exact CloudFormation stack ID for the reviewed account and Region.' ;;
esac
for dependency in aws jq openssl session-manager-plugin; do
    command -v "$dependency" >/dev/null || fail "Install $dependency before starting protected setup."
done

observed_account=$(aws sts get-caller-identity --region "$region" --query Account --output text)
[ "$observed_account" = "$account" ] || fail 'The active AWS session is in a different account.'
stack=$(aws cloudformation describe-stacks --region "$region" --stack-name "$stack_id" --output json)
ecs_prefix="arn:aws:ecs:$region:$account"
settings=$(printf '%s\n' "$stack" | jq -ce --arg stack "$stack_id" --arg account "$account" \
    --arg region "$region" --arg ecs "$ecs_prefix" '
    .Stacks | select(length == 1) | .[0] |
    select(.StackId == $stack and (.StackStatus | IN("CREATE_COMPLETE", "UPDATE_COMPLETE", "UPDATE_ROLLBACK_COMPLETE"))) |
    (.Outputs | map({key:.OutputKey,value:.OutputValue}) | from_entries) |
    select(.Phase == "database") | .SetupConfig | fromjson |
    select(.account == $account and .region == $region and .stack_id == $stack) |
    select((.tag | test("\\A[a-z][a-z0-9-]{0,30}[a-z0-9]\\z")) and
           (.cluster == .tag or .cluster == ($ecs + ":cluster/" + .tag))) |
    .tag as $tag |
    select(.task_definition | startswith($ecs + ":task-definition/" + $tag + "-setup:")) |
    select((.task_definition | split(":") | last | test("\\A[1-9][0-9]*\\z")) and
           (.subnet | test("\\Asubnet-[a-f0-9]+\\z")) and
           (.security_group | test("\\Asg-[a-f0-9]+\\z")) and
           (.document | test("\\A[A-Za-z0-9_.-]+\\z")) and
           (.port | tostring) == "8444" and .log_group == ("/shiftbox/" + .tag + "/ecs"))
') || fail 'The stack must expose valid protected setup outputs in the database phase.'

tag=$(printf '%s\n' "$settings" | jq -r .tag)
cluster=$tag
definition=$(printf '%s\n' "$settings" | jq -r .task_definition)
family=${definition##*/}
family=${family%:*}
subnet=$(printf '%s\n' "$settings" | jq -r .subnet)
group=$(printf '%s\n' "$settings" | jq -r .security_group)
document=$(printf '%s\n' "$settings" | jq -r .document)
logs=$(printf '%s\n' "$settings" | jq -r .log_group)
task_prefix="$ecs_prefix:task/$cluster/"
receipt='PostgreSQL installation activated. Start the controller and sign in again.'

services=$(aws ecs describe-services --region "$region" --cluster "$cluster" --services postgres controller --output json)
printf '%s\n' "$services" | jq -e '
    (.failures | length) == 0 and
    any(.services[]; .serviceName == "postgres" and .desiredCount == 1 and .runningCount == 1 and .pendingCount == 0) and
    any(.services[]; .serviceName == "controller" and .desiredCount == 0 and .runningCount == 0 and .pendingCount == 0)
' >/dev/null || fail 'Protected setup requires PostgreSQL ready and the normal controller stopped.'

list_setup_tasks() {
    aws ecs list-tasks --region "$region" --cluster "$cluster" --family "$family" --desired-status "$1" --output json |
        jq -ec --arg prefix "$task_prefix" '
            .taskArns | select(type == "array" and length <= 100 and all(.[]; startswith($prefix)
            and (ltrimstr($prefix) | test("\\A[a-f0-9]{32}\\z")))) | sort | unique'
}

current_task() {
    running=$(list_setup_tasks RUNNING) || return 1
    printf '%s\n' "$running" | jq -er '
        if length == 0 then "" elif length == 1 then .[0] else error("multiple setup tasks") end'
}

task=$(current_task) || fail 'Setup task discovery failed or found multiple tasks. Reconcile the cluster before retrying.'
if [ -z "$task" ]; then
    history=$(list_setup_tasks STOPPED) || fail 'Cannot reconcile previous setup tasks.'
    if [ "$history" != '[]' ]; then
        # Describe/wait arguments are space-separated only after strict ARN validation above.
        # shellcheck disable=SC2046
        aws ecs wait tasks-stopped --region "$region" --cluster "$cluster" \
            --tasks $(printf '%s\n' "$history" | jq -r '.[]') ||
            fail 'A previous setup task has not reached STOPPED.'
    fi
    # Concurrent attempts with the same configuration and task history share an ECS idempotency key.
    hash=$(printf '%s\n' "$stack_id" "$settings" "$history" | openssl dgst -sha256 -r)
    token=${hash%% *}
    if launched=$(aws ecs run-task --region "$region" --cluster "$cluster" --launch-type EC2 \
        --task-definition "$definition" --enable-execute-command --propagate-tags TASK_DEFINITION \
        --network-configuration "awsvpcConfiguration={subnets=[$subnet],securityGroups=[$group],assignPublicIp=DISABLED}" \
        --tags "key=shiftbox-bootstrap,value=$tag" --client-token "$token" --output json); then
        task=$(printf '%s\n' "$launched" | jq -er --arg prefix "$task_prefix" '
            select((.failures | length) == 0 and (.tasks | length) == 1) |
            .tasks[0].taskArn | select(startswith($prefix) and (ltrimstr($prefix) | test("\\A[a-f0-9]{32}\\z")))') ||
            fail 'ECS did not return one setup task. Check the private service host and retry to reconcile.'
    else
        task=$(current_task) || fail 'The launch result is uncertain. Reconcile setup tasks before retrying.'
        [ -n "$task" ] || fail 'The launch result is uncertain. Retry setup to reconcile the same task.'
    fi
fi

task_id=${task##*/}

task_state() {
    aws ecs describe-tasks --region "$region" --cluster "$cluster" --tasks "$task" --include TAGS --output json |
        jq -ec --arg task "$task" --arg definition "$definition" --arg tag "$tag" --arg cluster "$ecs_prefix:cluster/$cluster" '
            if (.tasks | length) == 0 and all(.failures[]?; .reason == "MISSING") then {} else
                .tasks | select(length == 1) | .[0] |
                select(.taskArn == $task and .taskDefinitionArn == $definition and .clusterArn == $cluster) |
                select([.tags[]? | select(.key == "shiftbox-bootstrap") | .value] == [$tag])
            end'
}

setup_events() {
    aws logs get-log-events --region "$region" --log-group-name "$logs" --log-stream-name "ecs/setup/$task_id" \
        --limit 100 --no-paginate --output json 2>/dev/null
}

has_receipt() {
    printf '%s\n' "$1" | jq -e --arg receipt "$receipt" 'any(.events[]?; .message == $receipt)' >/dev/null
}

finish_setup() {
    if [ "$1" != STOPPED ]; then
        aws ecs stop-task --region "$region" --cluster "$cluster" --task "$task" >/dev/null ||
            fail 'Setup is active, but its protected task could not be stopped. Keep the controller stopped.'
        aws ecs wait tasks-stopped --region "$region" --cluster "$cluster" --tasks "$task" ||
            fail 'Setup is active, but the task has not reached STOPPED. Keep the controller stopped.'
    fi
    printf '%s\n' "$receipt" 'The protected setup task is stopped. Review the controller startup change set.'
    exit 0
}

for _ in $(seq 1 45); do
    description=$(task_state) || fail 'Could not verify the exact tagged setup task for this stack.'
    last_status=$(printf '%s\n' "$description" | jq -r '.lastStatus // ""')
    if [ -n "$last_status" ]; then
        events=$(setup_events || :)
        if has_receipt "$events"; then
            finish_setup "$last_status"
        fi
        fingerprint=$(printf '%s\n' "$events" | jq -r '
            [.events[]? | .message | select(test("\\AVerify the setup certificate SHA-256 fingerprint: [a-f0-9]{64}\\z"))] | last // ""')
        runtime=$(printf '%s\n' "$description" | jq -r '
            [.containers[]? | select(.name == "setup") |
             select(any(.managedAgents[]?; .name == "ExecuteCommandAgent" and .lastStatus == "RUNNING")) |
             .runtimeId | select(type == "string" and test("\\A[A-Za-z0-9_-]+\\z"))] |
            if length == 1 then .[0] else "" end')
        if [ "$last_status" = RUNNING ] && [ -n "$runtime" ] && [ -n "$fingerprint" ]; then
            printf '%s\n' "$fingerprint" 'Open: https://127.0.0.1:8444/setup/' \
                'Complete the browser wizard, then close the tunnel.'
            trap ':' INT
            aws ssm start-session --region "$region" --target "ecs:${cluster}_${task_id}_${runtime}" --document-name "$document" || :
            trap - INT
            for _ in $(seq 1 10); do
                events=$(setup_events || :)
                if has_receipt "$events"; then
                    description=$(task_state) || fail 'Cannot verify task ownership after activation. Keep the controller stopped.'
                    last_status=$(printf '%s\n' "$description" | jq -er .lastStatus)
                    finish_setup "$last_status"
                fi
                sleep 2
            done
            fail 'The tunnel closed without a verified activation. Resume setup on the same stack before starting the controller.'
        fi
    fi
    sleep 2
done
fail 'The protected setup listener or activation receipt did not become available. Review the setup task and its logs.'
