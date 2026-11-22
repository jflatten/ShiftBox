package auth

// recoveryActionAllowed limits recovery administrators to review and explicit
// activation. Session inspection and sign-out are handled by Authorize.
func recoveryActionAllowed(p Principal, action string) bool {
	return p.User.Role == "administrator" && p.InstallationState == "recovery" &&
		(action == "recovery.review" || (action == "recovery.activate" && p.RecoveryReady))
}
