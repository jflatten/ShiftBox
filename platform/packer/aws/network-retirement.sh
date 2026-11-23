#!/usr/bin/env bash
# Retire only a validated attempt's Terraform state. The caller supplies the
# verified lease inputs, state directory, Terraform and error functions.

verify_attempt_state() {
    jq -e --rawfile expected "$scratch/addresses" --arg attempt "$attempt" --arg tag "$tag" '
      ($expected|split("\n")) as $expected |
      (.values.root_module // {}) | (.child_modules // [])==[] and
      all(.resources[]?; .address as $address | .mode=="managed" and ($expected|index($address)!=null) and
        (.values.id|type=="string" and length>0) and
        (.values.tags_all==null or .values.tags_all=={} or
         (.values.tags_all|.project=="shiftbox" and .cluster_name=="shiftbox-publisher" and
         .publisher_attempt==$attempt and .environment==("release-"+$tag))))
    ' "$scratch/prior.json" >/dev/null || fail 'the state contains resources outside the exact attempt'
}

state_id() {
    jq -r --arg address "$1" '
      [.values.root_module.resources[]?|select(.address==$address)|.values.id] | first // ""
    ' "$scratch/prior.json"
}

apply_network_destroy_plan() {
    local stage=$1 plan="$scratch/$1.tfplan"
    shift
    tf_logged plan -destroy -input=false -lock-timeout=60s -var-file="$inputs" -out="$plan" "$@" ||
        fail 'could not plan network retirement'
    tf show -json "$plan" > "$scratch/plan.json"
    jq -e --arg stage "$stage" --slurpfile prior "$scratch/prior.json" --slurpfile vars "$inputs" '
      (.variables | map_values(.value))==$vars[0] and
      all(.resource_changes[]; . as $change | .mode=="managed" and .change.actions==["delete"] and
        any($prior[0].values.root_module.resources[];
          .address==$change.address and .type==$change.type and .values.id==$change.change.before.id) and
        (if .address=="aws_eip.nat" then $stage=="destroy" and
          (.change.before.association_id // "")=="" and (.change.before.instance // "")==""
         else true end))
    ' "$scratch/plan.json" >/dev/null || fail 'retirement plan includes unowned resources or an attached Elastic IP'
    tf_logged apply -input=false -lock-timeout=60s "$plan" ||
        fail 'network retirement failed, retain the lease for recovery'
    tf show -json > "$scratch/prior.json"
    verify_attempt_state
}

wait_address_detached() {
    local allocation=$1 addresses retry
    [[ "$allocation" =~ ^eipalloc-[0-9a-f]{17}$ ]] || fail 'the saved Elastic IP allocation is invalid'
    for ((retry=0; retry<15; retry++)); do
        addresses=$(aws ec2 describe-addresses --filters "Name=allocation-id,Values=$allocation") ||
            fail 'could not verify the exact Elastic IP association'
        if jq -e --arg id "$allocation" '
          .Addresses | type=="array" and length<=1 and
          all(.[]; .AllocationId==$id and (.AssociationId // "")=="" and
            (.InstanceId // "")=="" and (.NetworkInterfaceId // "")=="")
        ' <<< "$addresses" >/dev/null; then
            return 0
        fi
        if ((retry<14)); then sleep 2; fi
    done
    fail 'the Elastic IP is still attached, keep the lease for recovery'
}

retire_network_state() {
    local allocation
    allocation=$(state_id aws_eip.nat)
    if [[ -n "$allocation" && -n "$(state_id aws_nat_gateway.build)" ]]; then
        # The provider reads the EIP's association while planning. Delete the
        # NAT and its dependents first so a fresh plan can release the address
        # without attempting to disassociate a stale association ID.
        apply_network_destroy_plan nat -target=aws_nat_gateway.build
        [[ -z "$(state_id aws_nat_gateway.build)" ]] || fail 'the NAT gateway remains in state'
    fi
    if [[ -n "$allocation" ]]; then wait_address_detached "$allocation"; fi
    if jq -e '(.values.root_module.resources // [])|length>0' "$scratch/prior.json" >/dev/null; then
        apply_network_destroy_plan destroy
    fi
    jq -e '(.values.root_module.resources // [])==[]' "$scratch/prior.json" >/dev/null ||
        fail 'network state still contains resources'
}
