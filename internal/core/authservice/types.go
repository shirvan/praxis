package authservice

// CredentialResponse is the data returned by GetCredentials to callers.
// It contains raw credential strings (not aws.Config) because Restate handler
// I/O must be JSON-serializable.
type CredentialResponse struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken,omitempty"`
	Region          string `json:"region"`
	EndpointURL     string `json:"endpointUrl,omitempty"`
	ExpiresAt       string `json:"expiresAt,omitempty"`

	// Source identifies how the credentials were resolved. When it is
	// SourceDefaultChain the key fields are empty and callers must use the
	// AWS default credential chain instead of a static provider.
	Source string `json:"source,omitempty"`
}

// SourceDefaultChain marks credentials that come from the AWS default
// credential chain (env, IMDS, shared config) rather than resolved strings.
const SourceDefaultChain = "default-chain"

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
	SessionToken    string `json:"sessionToken,omitempty"`
	ExpiresAt       string `json:"expiresAt,omitempty"`
	Source          string `json:"source,omitempty"`
}

// ConfigureRequest is the input for the Configure handler.
type ConfigureRequest struct {
	Config AccountConfig `json:"config"`
}
