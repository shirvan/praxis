package authservice

// CredentialResponse is the data returned by GetCredentials to callers.
// It contains raw credential strings (not aws.Config) because Restate handler
// I/O must be JSON-serializable.
type CredentialResponse struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken,omitempty"` //nolint:gosec // G117: field name matches AWS API contract, not a hardcoded secret
	Region          string `json:"region"`
	EndpointURL     string `json:"endpointUrl,omitempty"`
	ExpiresAt       string `json:"expiresAt,omitempty"`
}

// CredentialStatus is the read-only view returned by GetStatus.
type CredentialStatus struct {
	AccountAlias     string `json:"accountAlias"`
	CredentialSource string `json:"credentialSource"`
	Region           string `json:"region"`
	Valid            bool   `json:"valid"`
	ExpiresAt        string `json:"expiresAt,omitempty"`
	LastRefresh      string `json:"lastRefresh,omitempty"`
	Error            string `json:"error,omitempty"`
}

// AuthState is the durable state stored per Virtual Object key (per account-alias).
// This is the single Restate state entry for each AuthService instance.
// It combines configuration, cached credentials, and operational metadata.
type AuthState struct {
	// Config is the account's credential configuration (source, region, role ARN, etc.).
	Config AccountConfig `json:"config"`

	// CachedCredential holds the most recently resolved credentials.
	// Nil when no credentials have been resolved yet.
	CachedCredential *CachedCredential `json:"cachedCredential,omitempty"`

	// LastRefresh is the RFC 3339 timestamp of the most recent credential resolution.
	LastRefresh string `json:"lastRefresh,omitempty"`

	// RefreshScheduled is true when a durable Restate timer has been set to
	// proactively refresh credentials before expiry. Prevents duplicate timers.
	RefreshScheduled bool `json:"refreshScheduled"`

	// Error holds the last error message from a failed credential resolution.
	// Cleared on the next successful resolution.
	Error string `json:"error,omitempty"`
}

// CachedCredential holds the credential data cached in Restate state.
// ExpiresAt is empty for static/default credentials (which don't expire).
type CachedCredential struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken,omitempty"` //nolint:gosec // G117: field name matches AWS API contract, not a hardcoded secret
	ExpiresAt       string `json:"expiresAt,omitempty"`
}

// ConfigureRequest is the input for the Configure handler.
type ConfigureRequest struct {
	Config AccountConfig `json:"config"`
}
