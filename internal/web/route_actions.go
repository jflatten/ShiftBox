package web

// AcceptsAction bounds a resolver to the reviewed route's permission vocabulary.
// It does not select the action: the resolver must still derive the exact action
// from requested fields, ownership, or the persisted operation being retried.
func (route ManagementRoute) AcceptsAction(action string) bool {
	if action == "" || (route.Action == "") == (route.Resolver == "") {
		return false
	}
	if route.Action != "" {
		return action == route.Action
	}
	switch route.Resolver {
	case "workspace_action":
		switch action {
		case "workspace.start", "workspace.stop", "workspace.rebuild", "workspace.repair", "workspace.delete", "workspace.update", "workspace.launch", "workspace.connect_owner", "workspace.connect_admin":
			// Retry of provision/open_ide keeps its original launch/connection gates.
			return true
		}
	case "workspace_entry":
		return action == "workspace.connect_owner" || action == "workspace.connect_admin"
	case "operation_resource":
		switch action {
		case "workspace.view", "template.view", "setup.mutate", "infrastructure.manage", "maintenance.manage", "backup.create", "restore.validate", "restore.activate", "recovery.activate", "migration.preflight":
			return true
		}
	case "template_update":
		return action == "template.edit" || action == "template.archive"
	case "secret_scope":
		return action == "secret.manage_user" || action == "secret.manage_template" || action == "secret.manage_organization"
	case "membership_update":
		return action == "user.approve" || action == "user.disable" || action == "user.change_role"
	case "notice_receipt":
		return action == "notice.read" || action == "notice.dismiss"
	}
	return false
}
