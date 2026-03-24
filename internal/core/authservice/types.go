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
type AuthState struct {
	Config           AccountConfig     `json:"config"`
	CachedCredential *CachedCredential `json:"cachedCredential,omitempty"`
	LastRefresh      string            `json:"lastRefresh,omitempty"`
	RefreshScheduled bool              `json:"refreshScheduled"`
	Error            string            `json:"error,omitempty"`
}

// CachedCredential holds the credential data cached in Restate state.
// ExpiresAt is empty for static/default credentials (which don't expire).
type CachedCredential struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken,omitempty"`
	ExpiresAt       string `json:"expiresAt,omitempty"`
}

// ConfigureRequest is the input for the Configure handler.
type ConfigureRequest struct {
	Config AccountConfig `json:"config"`
}
