#!/bin/sh
set -eu

recipe=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT
mkdir "$test_dir/bin"
export FIXTURE_REGION=us-east-2 FIXTURE_ACCOUNT=111111111111
export FIXTURE_STACK="arn:aws:cloudformation:$FIXTURE_REGION:$FIXTURE_ACCOUNT:stack/shiftbox-fixture/00000000-0000-0000-0000-000000000001"
export FIXTURE_TASK="arn:aws:ecs:$FIXTURE_REGION:$FIXTURE_ACCOUNT:task/shiftbox-fixture/00000000000000000000000000000001"
export FIXTURE_DEFINITION="arn:aws:ecs:$FIXTURE_REGION:$FIXTURE_ACCOUNT:task-definition/shiftbox-fixture-setup:1"
export FIXTURE_RECEIPT='PostgreSQL installation activated. Start the controller and sign in again.'
export FIXTURE_TRACE="$test_dir/trace" FIXTURE_STATE="$test_dir/state"
export PATH="$test_dir/bin:$PATH"

cat > "$test_dir/bin/aws" <<'AWS'
#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$FIXTURE_TRACE"
case "$1 $2" in
    'sts get-caller-identity')
        if [ "$FIXTURE_CASE" = wrong-account ]; then echo 222222222222; else echo "$FIXTURE_ACCOUNT"; fi
        ;;
    'cloudformation describe-stacks')
        stack=$FIXTURE_STACK
        output_account=$FIXTURE_ACCOUNT
        phase=database
        case "$FIXTURE_CASE" in
            wrong-stack) stack=other ;;
            wrong-output) output_account=222222222222 ;;
            foundation) phase=foundation ;;
        esac
        jq -cn --arg stack "$stack" --arg requested "$FIXTURE_STACK" --arg phase "$phase" \
            --arg account "$output_account" --arg region "$FIXTURE_REGION" --arg definition "$FIXTURE_DEFINITION" '
            {account:$account,stack_id:$requested,region:$region,cluster:"shiftbox-fixture",task_definition:$definition,
             subnet:"subnet-0123456789abcdef0",security_group:"sg-0123456789abcdef0",document:"shiftbox-fixture-SetupPort",
             port:8444,log_group:"/shiftbox/shiftbox-fixture/ecs",tag:"shiftbox-fixture"} as $config |
            {Stacks:[{StackId:$stack,StackStatus:"UPDATE_COMPLETE",Outputs:[
              {OutputKey:"Phase",OutputValue:$phase},
              {OutputKey:"SetupConfig",OutputValue:($config|tojson)}]}]}'
        ;;
    'ecs describe-services')
        printf '%s\n' '{"services":[{"serviceName":"postgres","desiredCount":1,"runningCount":1,"pendingCount":0},{"serviceName":"controller","desiredCount":0,"runningCount":0,"pendingCount":0}],"failures":[]}'
        ;;
    'ecs list-tasks')
        case "$*" in *'--desired-status STOPPED'*) printf '%s\n' '{"taskArns":[]}'; exit 0 ;; esac
        case "$FIXTURE_CASE" in
            denied) exit 254 ;;
            ambiguous)
                jq -cn --arg task "$FIXTURE_TASK" '{taskArns:[$task,($task|sub("1$";"2"))]}'
                ;;
            lost-run)
                if [ -f "$FIXTURE_STATE/started" ]; then
                    jq -cn --arg task "$FIXTURE_TASK" '{taskArns:[$task]}'
                else
                    printf '%s\n' '{"taskArns":[]}'
                fi
                ;;
            start)
                printf '%s\n' '{"taskArns":[]}'
                ;;
            *) jq -cn --arg task "$FIXTURE_TASK" '{taskArns:[$task]}' ;;
        esac
        ;;
    'ecs run-task')
        touch "$FIXTURE_STATE/started"
        if [ "$FIXTURE_CASE" = lost-run ]; then exit 255; fi
        jq -cn --arg task "$FIXTURE_TASK" '{tasks:[{taskArn:$task}],failures:[]}'
        ;;
    'ecs describe-tasks')
        tag=shiftbox-fixture
        status=RUNNING
        if [ "$FIXTURE_CASE" = foreign-task ]; then tag=other; fi
        if [ "$FIXTURE_CASE" = already-activated ]; then status=STOPPED; fi
        jq -cn --arg task "$FIXTURE_TASK" --arg definition "$FIXTURE_DEFINITION" \
            --arg account "$FIXTURE_ACCOUNT" --arg region "$FIXTURE_REGION" --arg tag "$tag" --arg status "$status" '
            {tasks:[{taskArn:$task,clusterArn:("arn:aws:ecs:"+$region+":"+$account+":cluster/shiftbox-fixture"),
                     taskDefinitionArn:$definition, desiredStatus:$status, lastStatus:$status,
                     tags:[{key:"ShiftBoxBootstrap",value:$tag}],
                     containers:[{name:"setup",runtimeId:"fixture-runtime",
                                  managedAgents:[{name:"ExecuteCommandAgent",lastStatus:"RUNNING"}]}]}],failures:[]}'
        ;;
    'logs get-log-events')
        active=false
        if [ -f "$FIXTURE_STATE/activated" ] || [ "$FIXTURE_CASE" = already-activated ]; then active=true; fi
        jq -cn --arg receipt "$FIXTURE_RECEIPT" --argjson active "$active" '
            {events:([{message:"Verify the setup certificate SHA-256 fingerprint: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}]
                     + (if $active then [{message:$receipt}] else [] end))}'
        ;;
    'ssm start-session')
        case "$FIXTURE_CASE" in interrupted) exit 130 ;; ssm-denied) exit 254 ;; esac
        touch "$FIXTURE_STATE/activated"
        ;;
    'ecs stop-task')
        if [ "$FIXTURE_CASE" = stop-failed ]; then exit 254; fi
        ;;
    'ecs wait')
        if [ "$FIXTURE_CASE" = wait-failed ]; then exit 255; fi
        ;;
    *) printf 'Unexpected fixture AWS call: %s\n' "$*" >&2; exit 2 ;;
esac
AWS
for command in sleep session-manager-plugin; do
    printf '#!/bin/sh\nexit 0\n' > "$test_dir/bin/$command"
done
chmod +x "$test_dir/bin/"*

run_case() {
    FIXTURE_CASE=$1
    export FIXTURE_CASE
    : > "$FIXTURE_TRACE"
    rm -rf -- "$FIXTURE_STATE"
    mkdir "$FIXTURE_STATE"
    exit_code=0
    sh "$recipe/setup.sh" "$FIXTURE_STACK" "$FIXTURE_REGION" "$FIXTURE_ACCOUNT" > "$test_dir/out" 2>&1 || exit_code=$?
    if [ "$exit_code" -eq 127 ] || { [ "$2" = pass ] && [ "$exit_code" -ne 0 ]; } ||
        { [ "$2" = fail ] && [ "$exit_code" -eq 0 ]; }; then
        printf '%s: setup exited %s, expected %s\n' "$FIXTURE_CASE" "$exit_code" "$2" >&2
        cat "$test_dir/out" >&2
        exit 1
    fi
}

expect_call() {
    grep -Eq -- "$1" "$FIXTURE_TRACE" || {
        printf '%s: missing AWS call matching %s\n' "$FIXTURE_CASE" "$1" >&2
        exit 1
    }
}

reject_call() {
    if grep -Eq -- "$1" "$FIXTURE_TRACE"; then
        printf '%s: unexpected AWS call matching %s\n' "$FIXTURE_CASE" "$1" >&2
        exit 1
    fi
}

# A new reviewed setup requests one idempotent task and waits for verified stop.
run_case start pass
expect_call '^ecs run-task .*--client-token [a-f0-9]{64}([[:space:]]|$)'
expect_call '^ssm start-session '
expect_call '^ecs stop-task '
expect_call '^ecs wait tasks-stopped '

# A lost launch response is reconciled to the one existing task without launching again.
run_case lost-run pass
test "$(grep -c '^ecs run-task ' "$FIXTURE_TRACE")" -eq 1
expect_call '^ssm start-session '
expect_call '^ecs wait tasks-stopped '

# A resume of a durably active task needs no wizard and no additional start or stop.
run_case already-activated pass
reject_call '^(ecs (run-task|stop-task)|ssm start-session) '

# Unrelated stacks, absent prerequisites and ambiguous or foreign tasks fail before writes.
for scenario in wrong-account wrong-stack wrong-output foundation ambiguous foreign-task denied; do
    run_case "$scenario" fail
    reject_call '^(ecs (run-task|stop-task)|ssm start-session) '
done

# Interrupted or denied wizard sessions keep the verified task available for later resume.
for scenario in interrupted ssm-denied; do
    run_case "$scenario" fail
    expect_call '^ssm start-session '
    reject_call '^ecs (run-task|stop-task) '
done

# Activation still requires confirmed task stop before the helper reports success.
run_case stop-failed fail
expect_call '^ecs stop-task '
run_case wait-failed fail
expect_call '^ecs wait tasks-stopped '
