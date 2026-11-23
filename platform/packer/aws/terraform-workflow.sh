#!/usr/bin/env bash
# Callers supply tf() for their attempt's Terraform directory. Keep stdout
# available for machine outputs such as the provisioned AMI role ARN.
tf_logged() {
    local command=$1 status
    shift
    case "$command" in
        init|plan|apply) set -- -json "$@" ;;
        import) set -- -no-color "$@" ;;
    esac
    printf 'Terraform %s: starting\n' "$command" >&2
    if tf "$command" "$@" 2>&1 |
        jq --unbuffered -Rr --arg tool Terraform -f "${BASH_SOURCE[0]%/*}/workflow-log.jq" >&2; then
        printf 'Terraform %s: complete\n' "$command" >&2
    else
        status=$?
        printf 'Terraform %s: failed (exit %s)\n' "$command" "$status" >&2
        return "$status"
    fi
}
