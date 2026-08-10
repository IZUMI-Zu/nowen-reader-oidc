package oidcruntime

import (
	"context"
	"errors"
	"time"

	oidcauth "github.com/nowen-reader/nowen-reader/internal/auth/oidc"
	"github.com/nowen-reader/nowen-reader/internal/config"
)

var ErrConfigConflict = errors.New("OIDC configuration revision conflict")

var (
	ErrEnvironmentManaged          = errors.New("OIDC configuration is managed by the environment")
	ErrConfigurationInvalid        = errors.New("OIDC configuration is invalid")
	ErrConfigurationUnverified     = errors.New("OIDC configuration has not completed a test login")
	ErrAdminIdentityRequired       = errors.New("an administrator identity must be linked to the OIDC issuer")
	ErrBreakGlassPasswordRequired  = errors.New("the acting administrator must have a local recovery password")
	ErrOIDCOnlyUsersConfirmation   = errors.New("disabling OIDC requires confirmation for OIDC-only users")
	ErrDisablePasswordConfirmation = errors.New("disabling password login requires explicit confirmation")
)

type ConfigSource string

const (
	ConfigSourceEnvironment ConfigSource = "environment"
	ConfigSourceDatabase    ConfigSource = "database"
)

// StoredConfig is the persistence shape for the single Web-managed OIDC
// provider. SecretCiphertext is never exposed by the manager's public views.
type StoredConfig struct {
	Enabled              bool
	IssuerURL            string
	ClientID             string
	SecretCiphertext     string
	SecretKeyID          string
	ProviderName         string
	Scopes               string
	PublicURL            string
	AutoProvision        bool
	SessionTTLSeconds    int64
	DisablePasswordLogin bool
	Revision             int64
	VerifiedFingerprint  string
	LastVerifiedAt       *time.Time
	UpdatedBy            string
	UpdatedAt            time.Time
}

type AuditEvent struct {
	ActorUserID   string
	Action        string
	Result        string
	ChangedFields []string
	RequestID     string
}

// ConfigRepository is the local-substitutable persistence seam. SQLite is the
// production adapter; manager tests use an in-memory adapter.
type ConfigRepository interface {
	Load(ctx context.Context) (StoredConfig, error)
	Save(ctx context.Context, expectedRevision int64, next StoredConfig, audit AuditEvent) (StoredConfig, error)
	CompleteTest(ctx context.Context, expectedRevision int64, fingerprint string, verifiedAt time.Time, actorUserID string, identity oidcauth.VerifiedIdentity, audit AuditEvent) (StoredConfig, error)
	RecordAudit(ctx context.Context, revision int64, audit AuditEvent) error
}

type AccountSafetyChecker interface {
	UserHasIdentityForIssuer(ctx context.Context, userID, issuer string) (bool, error)
	UserHasPassword(ctx context.Context, userID string) (bool, error)
	OIDCOnlyUserCount(ctx context.Context, issuer string) (int64, error)
}

type SecretProtection string

const (
	SecretProtectionExternalKey SecretProtection = "external-key-file"
	SecretProtectionLocalKey    SecretProtection = "local-key-file"
)

// SecretProtector is the key-management seam. Production uses AES-GCM with a
// file-backed key; tests can use a deterministic adapter.
type SecretProtector interface {
	Encrypt(plaintext []byte) (ciphertext string, keyID string, err error)
	Decrypt(ciphertext string) ([]byte, error)
	KeyID() string
	Protection() SecretProtection
}

type State struct {
	Config    config.OIDCConfig
	Source    ConfigSource
	Available bool
	Ready     bool
	ErrorCode string
}

type AdminConfig struct {
	ManagedBy              ConfigSource     `json:"managedBy"`
	Editable               bool             `json:"editable"`
	Status                 string           `json:"status"`
	Revision               int64            `json:"revision"`
	ClientSecretConfigured bool             `json:"clientSecretConfigured"`
	SecretProtection       SecretProtection `json:"secretProtection,omitempty"`
	ForcePasswordLogin     bool             `json:"forcePasswordLogin"`
	CallbackURL            string           `json:"callbackURL"`
	BasePath               string           `json:"basePath"`
	LastVerifiedAt         *time.Time       `json:"lastVerifiedAt"`
	ErrorCode              string           `json:"errorCode,omitempty"`
	OIDCOnlyUserCount      int64            `json:"oidcOnlyUserCount"`
	Config                 AdminFields      `json:"config"`
}

type AdminFields struct {
	Enabled              bool     `json:"enabled"`
	IssuerURL            string   `json:"issuerURL"`
	ClientID             string   `json:"clientID"`
	ProviderName         string   `json:"providerName"`
	Scopes               []string `json:"scopes"`
	PublicURL            string   `json:"publicURL"`
	AutoProvision        bool     `json:"autoProvision"`
	SessionTTLSeconds    int64    `json:"sessionTTLSeconds"`
	DisablePasswordLogin bool     `json:"disablePasswordLogin"`
}

type UpdateRequest struct {
	ExpectedRevision            int64
	ActorUserID                 string
	RequestID                   string
	Fields                      AdminFields
	ClientSecret                *string
	ClearSecret                 bool
	ConfirmOIDCOnlyUsers        bool
	ConfirmDisablePasswordLogin bool
}

type ProbeRequest struct {
	Fields       AdminFields
	ClientSecret *string
}

type ProbeResult struct {
	IssuerURL          string `json:"issuerURL"`
	CallbackURL        string `json:"callbackURL"`
	DiscoveryLatencyMS int64  `json:"discoveryLatencyMs"`
	Message            string `json:"message"`
}

type CompletionFinalizer func(State, oidcauth.AuthenticatedIdentity) error
