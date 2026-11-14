package auth

import "time"

// User is ShiftBox membership; Dex credentials remain a separate identity concern.
type User struct {
	ID                   string     `json:"id"`
	PrimaryEmail         string     `json:"primaryEmail"`
	DisplayName          *string    `json:"displayName"`
	Role                 string     `json:"role"`
	MembershipState      string     `json:"membershipState"`
	CredentialMode       string     `json:"credentialMode"`
	MustChangePassword   bool       `json:"mustChangePassword"`
	ExternalAuthorizedAt *time.Time `json:"-"`
	Version              int64      `json:"version"`
}

// IdentityBinding uses the exact issuer/subject/connector tuple, never email matching.
type IdentityBinding struct {
	ID          string
	UserID      string
	Issuer      string
	Subject     string
	ConnectorID string
	EmailAtLink string
	LinkedBy    string
	LinkedAt    time.Time
}

// Session stores only digests of browser credentials and explicit authority clocks.
type Session struct {
	ID                   string     `json:"id"`
	UserID               string     `json:"userId"`
	TokenDigest          string     `json:"-"`
	CSRFDigest           string     `json:"-"`
	Mode                 string     `json:"mode"`
	AuthenticatedAt      time.Time  `json:"authenticatedAt"`
	ExternalAuthorizedAt *time.Time `json:"externalAuthorizedAt"`
	ExpiresAt            time.Time  `json:"expiresAt"`
	RevokedAt            *time.Time `json:"revokedAt"`
	RevocationReason     string     `json:"-"`
}

// ResourceGrant is an audited exact-resource permission with explicit revocation.
type ResourceGrant struct {
	ID            string
	SubjectUserID string
	ResourceType  string
	ResourceID    string
	Action        string
	GrantedBy     string
	GrantedAt     time.Time
	RevokedAt     *time.Time
}

// Principal contains current controller records, not browser-supplied role claims.
type Principal struct {
	User                 User
	Session              Session
	InstallationState    string
	ExternalAuthInterval time.Duration
	LocalBootstrap       bool
	RecoveryReady        bool
}

// Resource contains controller-derived ownership and current admission gates.
type Resource struct {
	Type          string
	ID            string
	OwnerID       string
	State         string
	LaunchGranted bool
	Eligible      bool
}

// Decision keeps administrator access auditable and retains the workspace owner's context.
type Decision struct {
	Allowed       bool
	AuditRequired bool
	OwnerContext  string
}
