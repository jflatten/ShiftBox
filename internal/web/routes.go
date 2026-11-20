package web

// ManagementRoute identifies a reviewed API entry point and its authorization rule.
// Resolver rules require controller-derived context; they are not permission keys.
type ManagementRoute struct {
	Method      string
	Path        string
	OperationID string
	Action      string
	Resolver    string
}

// WorkspaceSSHRoutes catalogs workstation access and its pairing endpoints.
// The SSH router enforces the credential rules for each endpoint.
func WorkspaceSSHRoutes() []ManagementRoute {
	return []ManagementRoute{
		{Method: "GET", Path: "/ssh/workspaces", OperationID: "listSSHWorkspaces", Resolver: "workspace_ssh"},
		{Method: "GET", Path: "/ssh/workspaces/{workspaceId}/connect", OperationID: "connectWorkspaceSSH", Resolver: "workspace_ssh"},
		{Method: "GET", Path: "/ssh/helper/download", OperationID: "downloadWorkspaceSSHHelper", Action: "workspace.list"},
		{Method: "POST", Path: "/ssh/helper/approve", OperationID: "approveWorkspaceSSHHelper", Resolver: "helper_pairing"},
		{Method: "POST", Path: "/ssh/helper/exchange", OperationID: "exchangeWorkspaceSSHHelper", Resolver: "helper_pairing"},
	}
}

// ManagementRoutes returns an independent catalog for guarded route registration.
// It declares policy requirements, not implemented handlers or granted authority.
func ManagementRoutes() []ManagementRoute {
	return []ManagementRoute{
		{Method: "GET", Path: "/setup/status", OperationID: "getSetupStatus", Action: "setup.view"},
		{Method: "POST", Path: "/setup/administrator", OperationID: "createInitialAdministrator", Action: "setup.mutate"},
		{Method: "PUT", Path: "/setup/data-profile", OperationID: "configureDataProfile", Action: "setup.mutate"},
		{Method: "PUT", Path: "/setup/certificate", OperationID: "configureBrowserCertificate", Action: "setup.mutate"},
		{Method: "POST", Path: "/setup/platform", OperationID: "validateSetupPlatform", Action: "setup.mutate"},
		{Method: "POST", Path: "/setup/activate", OperationID: "activateInstallation", Action: "setup.mutate"},
		{Method: "GET", Path: "/session", OperationID: "getSession", Action: "session.view"},
		{Method: "DELETE", Path: "/session", OperationID: "logout", Action: "session.logout"},
		{Method: "PUT", Path: "/session/password", OperationID: "changeOwnPassword", Action: "session.change_password"},
		{Method: "POST", Path: "/session/external-reauthorization", OperationID: "beginExternalReauthorization", Action: "session.external_reauthorize"},
		{Method: "GET", Path: "/workspaces", OperationID: "listWorkspaces", Action: "workspace.list"},
		{Method: "POST", Path: "/workspaces", OperationID: "launchWorkspace", Action: "workspace.launch"},
		{Method: "GET", Path: "/workspaces/{workspaceId}", OperationID: "getWorkspace", Action: "workspace.view"},
		{Method: "PATCH", Path: "/workspaces/{workspaceId}", OperationID: "updateWorkspacePolicy", Action: "workspace.update_policy"},
		{Method: "POST", Path: "/workspaces/{workspaceId}/actions/{action}", OperationID: "requestWorkspaceAction", Resolver: "workspace_action"},
		{Method: "POST", Path: "/workspaces/{workspaceId}/entry-grants", OperationID: "createWorkspaceEntryGrant", Resolver: "workspace_entry"},
		{Method: "GET", Path: "/admin/workspaces/{workspaceId}/connection-grants", OperationID: "listWorkspaceConnectionGrants", Action: "workspace.manage_connection_grants"},
		{Method: "DELETE", Path: "/admin/workspaces/{workspaceId}/connection-grants/{grantId}", OperationID: "revokeWorkspaceConnectionGrant", Action: "workspace.manage_connection_grants"},
		{Method: "GET", Path: "/operations/{operationId}", OperationID: "getOperation", Resolver: "operation_resource"},
		{Method: "GET", Path: "/operations/{operationId}/events", OperationID: "streamOperationEvents", Resolver: "operation_resource"},
		{Method: "GET", Path: "/templates", OperationID: "listTemplates", Action: "template.view"},
		{Method: "GET", Path: "/templates/catalog", OperationID: "listTemplateCatalog", Action: "template.create"},
		{Method: "POST", Path: "/templates", OperationID: "createTemplate", Action: "template.create"},
		{Method: "PATCH", Path: "/templates/{templateId}", OperationID: "updateTemplate", Resolver: "template_update"},
		{Method: "POST", Path: "/templates/{templateId}/revisions", OperationID: "createTemplateRevision", Action: "template.create_revision"},
		{Method: "POST", Path: "/templates/{templateId}/grants", OperationID: "grantTemplateLaunch", Action: "template.share"},
		{Method: "DELETE", Path: "/templates/{templateId}/grants/{userId}", OperationID: "revokeTemplateLaunch", Action: "template.share"},
		{Method: "GET", Path: "/secrets", OperationID: "listSecretMetadata", Resolver: "secret_scope"},
		{Method: "PUT", Path: "/secrets/{scope}/{scopeId}/{name}", OperationID: "setSecret", Resolver: "secret_scope"},
		{Method: "DELETE", Path: "/secrets/{scope}/{scopeId}/{name}", OperationID: "deleteSecretOverride", Resolver: "secret_scope"},
		{Method: "GET", Path: "/git/authorization", OperationID: "getGitAuthorization", Action: "git.manage"},
		{Method: "POST", Path: "/git/authorization", OperationID: "beginGitAuthorization", Action: "git.manage"},
		{Method: "DELETE", Path: "/git/authorization", OperationID: "revokeGitAuthorization", Action: "git.manage"},
		{Method: "GET", Path: "/git/authorization/callback", OperationID: "completeGitAuthorization", Action: "git.manage"},
		{Method: "GET", Path: "/admin/users", OperationID: "listUsers", Action: "user.list"},
		{Method: "POST", Path: "/admin/users", OperationID: "createLocalUser", Action: "user.create"},
		{Method: "PATCH", Path: "/admin/users/{userId}", OperationID: "updateMembership", Resolver: "membership_update"},
		{Method: "POST", Path: "/admin/users/{userId}/password-reset", OperationID: "resetLocalUserPassword", Action: "user.reset_password"},
		{Method: "PUT", Path: "/admin/identity/external", OperationID: "configureExternalIdentity", Action: "infrastructure.manage"},
		{Method: "POST", Path: "/admin/notices", OperationID: "publishNotice", Action: "notice.publish"},
		{Method: "GET", Path: "/notices", OperationID: "listNotices", Action: "notice.list"},
		{Method: "PATCH", Path: "/notices/{noticeId}/receipt", OperationID: "updateNoticeReceipt", Resolver: "notice_receipt"},
		{Method: "GET", Path: "/admin/audit", OperationID: "listAuditEvents", Action: "audit.list"},
		{Method: "GET", Path: "/admin/infrastructure", OperationID: "getInfrastructureStatus", Action: "infrastructure.view"},
		{Method: "GET", Path: "/admin/settings/session", OperationID: "getSessionTimeout", Action: "infrastructure.manage"},
		{Method: "PUT", Path: "/admin/settings/session", OperationID: "updateSessionTimeout", Action: "infrastructure.manage"},
		{Method: "GET", Path: "/admin/image-cache", OperationID: "getImageCache", Action: "infrastructure.view"},
		{Method: "PUT", Path: "/admin/image-cache", OperationID: "updateImageCache", Action: "infrastructure.manage"},
		{Method: "PUT", Path: "/admin/certificate", OperationID: "replaceBrowserCertificate", Action: "infrastructure.manage"},
		{Method: "GET", Path: "/admin/certificate", OperationID: "getBrowserCertificate", Action: "infrastructure.view"},
		{Method: "POST", Path: "/admin/platform-artifacts", OperationID: "importPlatformArtifact", Action: "infrastructure.manage"},
		{Method: "DELETE", Path: "/admin/platform-artifacts/{artifactId}", OperationID: "retireAMI", Action: "infrastructure.manage"},
		{Method: "GET", Path: "/admin/resource-profiles", OperationID: "listResourceProfiles", Action: "infrastructure.view"},
		{Method: "POST", Path: "/admin/resource-profiles", OperationID: "createResourceProfile", Action: "infrastructure.manage"},
		{Method: "PUT", Path: "/admin/resource-profiles/{profileId}", OperationID: "updateResourceProfile", Action: "infrastructure.manage"},
		{Method: "DELETE", Path: "/admin/resource-profiles/{profileId}", OperationID: "deleteResourceProfile", Action: "infrastructure.manage"},
		{Method: "PUT", Path: "/admin/capacity", OperationID: "updateCapacityBudget", Action: "infrastructure.manage"},
		{Method: "PUT", Path: "/admin/keep-alive", OperationID: "updateKeepAlivePolicy", Action: "infrastructure.manage"},
		{Method: "POST", Path: "/admin/network-policies", OperationID: "createNetworkPolicy", Action: "infrastructure.manage"},
		{Method: "GET", Path: "/admin/maintenance", OperationID: "listMaintenanceCampaigns", Action: "infrastructure.view"},
		{Method: "POST", Path: "/admin/maintenance", OperationID: "createMaintenanceCampaign", Action: "maintenance.manage"},
		{Method: "POST", Path: "/admin/migrations/preflight", OperationID: "runMigrationPreflight", Action: "migration.preflight"},
		{Method: "POST", Path: "/backups", OperationID: "createBackup", Action: "backup.create"},
		{Method: "POST", Path: "/restores/validate", OperationID: "validateRestore", Action: "restore.validate"},
		{Method: "GET", Path: "/recovery/{reviewId}", OperationID: "getRecoveryReview", Action: "recovery.review"},
		{Method: "PATCH", Path: "/recovery/{reviewId}", OperationID: "updateRecoveryReview", Action: "recovery.review"},
		{Method: "POST", Path: "/recovery/{reviewId}/activate", OperationID: "activateRecovery", Action: "recovery.activate"},
	}
}
