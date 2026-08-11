package oidcruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	oidcauth "github.com/nowen-reader/nowen-reader/internal/auth/oidc"
	"github.com/nowen-reader/nowen-reader/internal/config"
)

type probeProvider interface {
	oidcauth.Provider
	Probe(ctx context.Context) error
}

type ProviderFactory func(oidcauth.RemoteProviderConfig) (probeProvider, error)

type unavailableProvider struct{}

func (unavailableProvider) Issuer() string { return "" }

func (unavailableProvider) Probe(context.Context) error { return oidcauth.ErrProviderUnavailable }

func (unavailableProvider) AuthorizationURL(context.Context, oidcauth.AuthorizationRequest) (string, error) {
	return "", oidcauth.ErrProviderUnavailable
}

func (unavailableProvider) Exchange(context.Context, string, string) (oidcauth.VerifiedIdentity, error) {
	return oidcauth.VerifiedIdentity{}, oidcauth.ErrProviderUnavailable
}

type Options struct {
	Source             ConfigSource
	EnvironmentConfig  config.OIDCConfig
	EnvironmentError   error
	Repository         ConfigRepository
	Safety             AccountSafetyChecker
	Transactions       oidcauth.TransactionStore
	Protector          SecretProtector
	BasePath           string
	ForcePasswordLogin bool
	ProviderFactory    ProviderFactory
	Now                func() time.Time
}

type runtimeSnapshot struct {
	state       State
	record      StoredConfig
	service     *oidcauth.Service
	fingerprint string
}

// Manager owns the complete OIDC configuration lifecycle and publishes only
// immutable runtime snapshots to authentication callers.
type Manager struct {
	source             ConfigSource
	repository         ConfigRepository
	safety             AccountSafetyChecker
	transactions       oidcauth.TransactionStore
	protector          SecretProtector
	basePath           string
	forcePasswordLogin bool
	providerFactory    ProviderFactory
	now                func() time.Time

	updateMu sync.RWMutex
	current  atomic.Pointer[runtimeSnapshot]
}

func NewManager(ctx context.Context, options Options) (*Manager, error) {
	if options.Source != ConfigSourceEnvironment && options.Source != ConfigSourceDatabase {
		return nil, errors.New("OIDC configuration source must be environment or database")
	}
	if options.Source == ConfigSourceDatabase && options.Repository == nil {
		return nil, errors.New("database-managed OIDC requires a configuration repository")
	}
	if options.Transactions == nil {
		return nil, errors.New("OIDC runtime requires transaction storage")
	}
	if options.ProviderFactory == nil {
		options.ProviderFactory = func(providerConfig oidcauth.RemoteProviderConfig) (probeProvider, error) {
			return oidcauth.NewRemoteProvider(providerConfig)
		}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	manager := &Manager{
		source: options.Source, repository: options.Repository, safety: options.Safety, transactions: options.Transactions,
		protector: options.Protector, basePath: options.BasePath,
		forcePasswordLogin: options.ForcePasswordLogin, providerFactory: options.ProviderFactory, now: options.Now,
	}

	var snapshot *runtimeSnapshot
	if options.Source == ConfigSourceEnvironment {
		cfg := cloneConfig(options.EnvironmentConfig)
		record := storedFromConfig(cfg)
		secretVersion, err := opaqueFingerprintToken()
		if err != nil {
			return nil, fmt.Errorf("generate OIDC runtime fingerprint token: %w", err)
		}
		record.SecretCiphertext = secretVersion
		snapshot = manager.buildSnapshot(cfg, record, options.EnvironmentError, true)
	} else {
		record, err := options.Repository.Load(ctx)
		if err != nil {
			snapshot = manager.buildSnapshot(config.OIDCConfig{}, StoredConfig{}, err, false)
		} else {
			cfg, resolveErr := manager.resolveStored(record, false)
			snapshot = manager.buildSnapshot(cfg, record, resolveErr, true)
		}
	}
	manager.current.Store(snapshot)
	return manager, nil
}

func (m *Manager) State() State {
	snapshot := m.current.Load()
	if snapshot == nil {
		return State{Source: m.source, ErrorCode: "configuration_unavailable"}
	}
	state := snapshot.state
	state.Config = cloneConfig(state.Config)
	return state
}

func (m *Manager) WithStateLease(run StateLease) error {
	if run == nil {
		return errors.New("OIDC state lease callback is required")
	}
	m.updateMu.RLock()
	defer m.updateMu.RUnlock()
	return run(m.State())
}

func (m *Manager) AdminConfig(ctx context.Context) (AdminConfig, error) {
	result := m.adminConfigForSnapshot(m.current.Load())
	if m.source != ConfigSourceDatabase || m.safety == nil || result.Config.IssuerURL == "" {
		return result, nil
	}
	count, err := m.safety.OIDCOnlyUserCount(ctx, result.Config.IssuerURL)
	if err != nil {
		return AdminConfig{}, err
	}
	result.OIDCOnlyUserCount = count
	return result, nil
}

func (m *Manager) Begin(ctx context.Context, request oidcauth.BeginRequest) (oidcauth.AuthorizationRedirect, error) {
	m.updateMu.RLock()
	defer m.updateMu.RUnlock()
	snapshot := m.current.Load()
	if snapshot == nil || !snapshot.state.Ready || snapshot.service == nil {
		return oidcauth.AuthorizationRedirect{}, oidcauth.ErrProviderUnavailable
	}
	result, err := snapshot.service.Begin(ctx, request)
	result.CookieSecure = snapshot.state.Config.SecureCookies
	return result, err
}

func (m *Manager) BeginConfigTest(ctx context.Context, actorUserID, actorSessionID, returnTo string) (oidcauth.AuthorizationRedirect, error) {
	m.updateMu.RLock()
	defer m.updateMu.RUnlock()
	if m.source != ConfigSourceDatabase {
		return oidcauth.AuthorizationRedirect{}, ErrEnvironmentManaged
	}
	snapshot := m.current.Load()
	if snapshot == nil || snapshot.service == nil || snapshot.fingerprint == "" {
		return oidcauth.AuthorizationRedirect{}, ErrConfigurationInvalid
	}
	result, err := snapshot.service.Begin(ctx, oidcauth.BeginRequest{
		Purpose: oidcauth.PurposeConfigTest, SessionUserID: actorUserID, SessionID: actorSessionID, ReturnTo: returnTo,
	})
	result.CookieSecure = snapshot.state.Config.SecureCookies
	return result, err
}

func (m *Manager) Complete(ctx context.Context, request oidcauth.CallbackRequest) (oidcauth.AuthenticatedIdentity, error) {
	m.updateMu.RLock()
	defer m.updateMu.RUnlock()
	snapshot := m.current.Load()
	if snapshot == nil || snapshot.service == nil {
		return oidcauth.AuthenticatedIdentity{}, oidcauth.ErrProviderUnavailable
	}
	return snapshot.service.Complete(ctx, request)
}

// CompleteAndFinalize keeps the runtime read lease through ordinary local
// login side effects, so Apply cannot publish a new policy between token
// validation and Session/identity persistence. Config tests finalize through
// CompleteConfigTest, which intentionally acquires the write lock instead.
func (m *Manager) CompleteAndFinalize(ctx context.Context, request oidcauth.CallbackRequest, finalize CompletionFinalizer) (oidcauth.AuthenticatedIdentity, error) {
	m.updateMu.RLock()
	defer m.updateMu.RUnlock()
	snapshot := m.current.Load()
	if snapshot == nil || snapshot.service == nil {
		return oidcauth.AuthenticatedIdentity{}, oidcauth.ErrProviderUnavailable
	}
	identity, err := snapshot.service.Complete(ctx, request)
	if err != nil || identity.Purpose == oidcauth.PurposeConfigTest {
		return identity, err
	}
	if finalize == nil {
		return identity, errors.New("OIDC completion finalizer is required")
	}
	state := snapshot.state
	state.Config = cloneConfig(state.Config)
	if err := finalize(state, identity); err != nil {
		return identity, err
	}
	return identity, nil
}

func (m *Manager) Cancel(ctx context.Context, request oidcauth.CancelRequest) (string, error) {
	m.updateMu.RLock()
	defer m.updateMu.RUnlock()
	snapshot := m.current.Load()
	if snapshot == nil || snapshot.service == nil {
		return "", oidcauth.ErrProviderUnavailable
	}
	return snapshot.service.Cancel(ctx, request)
}

func (m *Manager) Probe(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	if m.source != ConfigSourceDatabase {
		return ProbeResult{}, ErrEnvironmentManaged
	}
	current, err := m.repository.Load(ctx)
	if err != nil {
		return ProbeResult{}, err
	}
	candidate, err := m.recordFromFields(current, request.Fields, request.ClientSecret, false)
	if err != nil {
		return ProbeResult{}, err
	}
	cfg, err := m.resolveStored(candidate, true)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("%w: %v", ErrConfigurationInvalid, err)
	}
	provider, err := m.providerFactory(remoteProviderConfig(cfg))
	if err != nil {
		return ProbeResult{}, fmt.Errorf("%w: %v", ErrConfigurationInvalid, err)
	}
	probeStarted := time.Now()
	if err := provider.Probe(ctx); err != nil {
		return ProbeResult{}, fmt.Errorf("probe OIDC discovery: %w", err)
	}
	latency := time.Since(probeStarted).Milliseconds()
	return ProbeResult{
		IssuerURL: cfg.IssuerURL, CallbackURL: cfg.CallbackURL, DiscoveryLatencyMS: latency,
		Message: "Discovery and endpoint validation succeeded; client credentials still require a test login",
	}, nil
}

func (m *Manager) Apply(ctx context.Context, request UpdateRequest) (result AdminConfig, resultErr error) {
	auditRevision := request.ExpectedRevision
	if auditRevision < 0 {
		auditRevision = 0
	}
	defer func() {
		if resultErr != nil {
			_ = m.recordFailureAudit(ctx, auditRevision, request.ActorUserID, "update", request.RequestID, adminFailureCode(resultErr))
		}
	}()
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	if m.source != ConfigSourceDatabase {
		return AdminConfig{}, ErrEnvironmentManaged
	}
	current, err := m.repository.Load(ctx)
	if err != nil {
		return AdminConfig{}, err
	}
	auditRevision = current.Revision
	if current.Revision != request.ExpectedRevision {
		return AdminConfig{}, ErrConfigConflict
	}
	candidate, err := m.recordFromFields(current, request.Fields, request.ClientSecret, request.ClearSecret)
	if err != nil {
		return AdminConfig{}, err
	}
	candidate.UpdatedBy = request.ActorUserID
	candidate.UpdatedAt = m.now().UTC()

	fullConfig, fullErr := m.resolveStored(candidate, true)
	fingerprint := ""
	if fullErr == nil {
		fingerprint = protocolFingerprint(fullConfig, m.basePath, candidate.SecretCiphertext)
	}
	if fingerprint == "" || fingerprint != current.VerifiedFingerprint {
		candidate.VerifiedFingerprint = ""
		candidate.LastVerifiedAt = nil
	}
	if candidate.Enabled {
		if fullErr != nil {
			return AdminConfig{}, fmt.Errorf("%w: %v", ErrConfigurationInvalid, fullErr)
		}
		if candidate.VerifiedFingerprint == "" || candidate.VerifiedFingerprint != fingerprint {
			return AdminConfig{}, ErrConfigurationUnverified
		}
		if m.safety == nil {
			return AdminConfig{}, ErrAdminIdentityRequired
		}
		linked, safetyErr := m.safety.UserHasIdentityForIssuer(ctx, request.ActorUserID, fullConfig.IssuerURL)
		if safetyErr != nil {
			return AdminConfig{}, safetyErr
		}
		if !linked {
			return AdminConfig{}, ErrAdminIdentityRequired
		}
	}
	if current.Enabled && !candidate.Enabled {
		if m.safety == nil {
			return AdminConfig{}, ErrBreakGlassPasswordRequired
		}
		hasPassword, safetyErr := m.safety.UserHasPassword(ctx, request.ActorUserID)
		if safetyErr != nil {
			return AdminConfig{}, safetyErr
		}
		if !hasPassword {
			return AdminConfig{}, ErrBreakGlassPasswordRequired
		}
		affected, safetyErr := m.safety.OIDCOnlyUserCount(ctx, current.IssuerURL)
		if safetyErr != nil {
			return AdminConfig{}, safetyErr
		}
		if affected > 0 && !request.ConfirmOIDCOnlyUsers {
			return AdminConfig{}, ErrOIDCOnlyUsersConfirmation
		}
	}
	if candidate.DisablePasswordLogin {
		if !candidate.Enabled {
			return AdminConfig{}, fmt.Errorf("%w: OIDC must be enabled before password login can be disabled", ErrConfigurationInvalid)
		}
		if m.safety == nil {
			return AdminConfig{}, ErrBreakGlassPasswordRequired
		}
		hasPassword, safetyErr := m.safety.UserHasPassword(ctx, request.ActorUserID)
		if safetyErr != nil {
			return AdminConfig{}, safetyErr
		}
		if !hasPassword {
			return AdminConfig{}, ErrBreakGlassPasswordRequired
		}
		if !current.DisablePasswordLogin && !request.ConfirmDisablePasswordLogin {
			return AdminConfig{}, ErrDisablePasswordConfirmation
		}
	}

	resolved, resolveErr := m.resolveStored(candidate, candidate.Enabled)
	if resolveErr != nil {
		return AdminConfig{}, fmt.Errorf("%w: %v", ErrConfigurationInvalid, resolveErr)
	}
	prospective := m.buildSnapshot(resolved, candidate, nil, true)
	if candidate.Enabled && prospective.service == nil {
		return AdminConfig{}, fmt.Errorf("%w: provider could not be constructed", ErrConfigurationInvalid)
	}
	oidcOnlyUserCount := int64(0)
	if m.safety != nil && candidate.IssuerURL != "" {
		oidcOnlyUserCount, err = m.safety.OIDCOnlyUserCount(ctx, candidate.IssuerURL)
		if err != nil {
			return AdminConfig{}, err
		}
	}

	candidate.Revision = current.Revision
	saveRequest := SaveRequest{
		ExpectedRevision: request.ExpectedRevision,
		Next:             candidate,
		Audit: AuditEvent{
			ActorUserID: request.ActorUserID, Action: "update", Result: "success",
			ChangedFields: changedFields(current, candidate, request.ClientSecret != nil || request.ClearSecret), RequestID: request.RequestID,
		},
		RequireActorPassword: current.Enabled && !candidate.Enabled || candidate.DisablePasswordLogin,
	}
	if candidate.Enabled {
		saveRequest.RequireActorIdentityIssuer = candidate.IssuerURL
	}
	if current.Enabled && !candidate.Enabled && !request.ConfirmOIDCOnlyUsers {
		saveRequest.RequireNoOIDCOnlyUsersIssuer = current.IssuerURL
	}
	saved, err := m.repository.Save(ctx, saveRequest)
	if err != nil {
		return AdminConfig{}, err
	}
	resolved, resolveErr = m.resolveStored(saved, false)
	snapshot := m.buildSnapshot(resolved, saved, resolveErr, true)
	m.current.Store(snapshot)
	result = m.adminConfigForSnapshot(snapshot)
	result.OIDCOnlyUserCount = oidcOnlyUserCount
	return result, nil
}

func (m *Manager) CompleteConfigTest(ctx context.Context, actorUserID, actorSessionID, requestID string, identity oidcauth.AuthenticatedIdentity) (result AdminConfig, resultErr error) {
	auditRevision := identity.ConfigRevision
	if auditRevision < 0 {
		auditRevision = 0
	}
	defer func() {
		if resultErr != nil {
			_ = m.recordFailureAudit(ctx, auditRevision, actorUserID, "verify", requestID, adminFailureCode(resultErr))
		}
	}()
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	if m.source != ConfigSourceDatabase {
		return AdminConfig{}, ErrEnvironmentManaged
	}
	if identity.Purpose != oidcauth.PurposeConfigTest || identity.SessionUserID != actorUserID || identity.SessionID != actorSessionID ||
		identity.ConfigRevision <= 0 || identity.ConfigFingerprint == "" {
		return AdminConfig{}, oidcauth.ErrInvalidTransaction
	}
	record, err := m.repository.Load(ctx)
	if err != nil {
		return AdminConfig{}, err
	}
	auditRevision = record.Revision
	if identity.ConfigRevision > record.Revision {
		return AdminConfig{}, oidcauth.ErrConfigurationChanged
	}
	cfg, err := m.resolveStored(record, true)
	if err != nil {
		return AdminConfig{}, fmt.Errorf("%w: %v", ErrConfigurationInvalid, err)
	}
	fingerprint := protocolFingerprint(cfg, m.basePath, record.SecretCiphertext)
	if fingerprint != identity.ConfigFingerprint {
		return AdminConfig{}, oidcauth.ErrConfigurationChanged
	}
	if identity.Issuer != cfg.IssuerURL || identity.Subject == "" {
		return AdminConfig{}, oidcauth.ErrInvalidIdentity
	}
	oidcOnlyUserCount := int64(0)
	if m.safety != nil {
		oidcOnlyUserCount, err = m.safety.OIDCOnlyUserCount(ctx, cfg.IssuerURL)
		if err != nil {
			return AdminConfig{}, err
		}
	}
	saved, err := m.repository.CompleteTest(ctx, record.Revision, identity.ConfigFingerprint, m.now().UTC(), actorUserID, identity.VerifiedIdentity, AuditEvent{
		ActorUserID: actorUserID, Action: "verify", Result: "success",
		ChangedFields: []string{"verifiedFingerprint", "lastVerifiedAt"}, RequestID: requestID,
	})
	if err != nil {
		return AdminConfig{}, err
	}
	resolved, resolveErr := m.resolveStored(saved, false)
	snapshot := m.buildSnapshot(resolved, saved, resolveErr, true)
	m.current.Store(snapshot)
	result = m.adminConfigForSnapshot(snapshot)
	result.OIDCOnlyUserCount = oidcOnlyUserCount
	return result, nil
}

// RecordAdminFailure persists a stable, secret-free failure event for
// requests rejected before they reach the manager's typed validation path.
func (m *Manager) RecordAdminFailure(ctx context.Context, actorUserID, action, requestID, code string) error {
	if m == nil || m.source != ConfigSourceDatabase || m.repository == nil {
		return nil
	}
	m.updateMu.RLock()
	defer m.updateMu.RUnlock()
	record, err := m.repository.Load(ctx)
	if err != nil {
		return err
	}
	return m.recordFailureAudit(ctx, record.Revision, actorUserID, action, requestID, code)
}

func (m *Manager) recordFailureAudit(ctx context.Context, revision int64, actorUserID, action, requestID, code string) error {
	if m.source != ConfigSourceDatabase || m.repository == nil {
		return nil
	}
	if revision < 0 {
		revision = 0
	}
	if code == "" {
		code = "operation_failed"
	}
	return m.repository.RecordAudit(ctx, revision, AuditEvent{
		ActorUserID: actorUserID, Action: action, Result: "failure:" + code,
		ChangedFields: []string{}, RequestID: requestID,
	})
}

func adminFailureCode(err error) string {
	switch {
	case errors.Is(err, ErrAdministratorRequired):
		return "administrator_required"
	case errors.Is(err, ErrConfigConflict):
		return "config_revision_conflict"
	case errors.Is(err, ErrConfigurationInvalid):
		return "configuration_invalid"
	case errors.Is(err, ErrConfigurationUnverified):
		return "test_login_required"
	case errors.Is(err, ErrAdminIdentityRequired):
		return "admin_identity_required"
	case errors.Is(err, ErrBreakGlassPasswordRequired):
		return "recovery_password_required"
	case errors.Is(err, ErrOIDCOnlyUsersConfirmation):
		return "oidc_only_users_confirmation_required"
	case errors.Is(err, ErrDisablePasswordConfirmation):
		return "disable_password_confirmation_required"
	case errors.Is(err, oidcauth.ErrConfigurationChanged):
		return "oidc_configuration_changed"
	case errors.Is(err, oidcauth.ErrInvalidIdentity):
		return "identity_validation_failed"
	default:
		return "operation_failed"
	}
}

func (m *Manager) buildSnapshot(cfg config.OIDCConfig, record StoredConfig, configErr error, buildProvider bool) *runtimeSnapshot {
	passwordLoginPolicyDisabled := cfg.DisablePasswordLogin
	if m.source == ConfigSourceDatabase {
		passwordLoginPolicyDisabled = record.DisablePasswordLogin
	}
	if m.forcePasswordLogin {
		cfg.DisablePasswordLogin = false
	}
	snapshot := &runtimeSnapshot{record: record, state: State{
		Config: cloneConfig(cfg), Source: m.source,
		PasswordLoginPolicyDisabled: passwordLoginPolicyDisabled,
	}}
	if configErr != nil {
		snapshot.state.ErrorCode = configErrorCode(configErr)
		m.enablePasswordRecoveryForDatabaseError(snapshot)
		m.attachChangedConfigurationRejector(snapshot, buildProvider)
		return snapshot
	}
	fullConfig, fullErr := m.resolveConfigForFingerprint(cfg)
	if fullErr == nil {
		fullConfig.Enabled = cfg.Enabled
		fullConfig.DisablePasswordLogin = cfg.DisablePasswordLogin
		cfg = fullConfig
		snapshot.state.Config = cloneConfig(cfg)
		snapshot.fingerprint = protocolFingerprint(fullConfig, m.basePath, record.SecretCiphertext)
	}
	if !buildProvider || fullErr != nil {
		if cfg.Enabled && fullErr != nil {
			snapshot.state.ErrorCode = configErrorCode(fullErr)
			m.enablePasswordRecoveryForDatabaseError(snapshot)
		}
		m.attachChangedConfigurationRejector(snapshot, buildProvider)
		return snapshot
	}
	service, err := m.buildService(cfg, record.Revision, snapshot.fingerprint)
	if err != nil {
		snapshot.state.ErrorCode = configErrorCode(err)
		m.enablePasswordRecoveryForDatabaseError(snapshot)
		return snapshot
	}
	snapshot.service = service
	snapshot.state.Available = true
	snapshot.state.Ready = cfg.Enabled
	return snapshot
}

func (m *Manager) enablePasswordRecoveryForDatabaseError(snapshot *runtimeSnapshot) {
	if m.source == ConfigSourceDatabase && snapshot != nil && snapshot.state.ErrorCode != "" {
		snapshot.state.Config.DisablePasswordLogin = false
	}
}

// A database draft can become incomplete while an older browser transaction
// is in flight. Keep just enough completion machinery to consume that
// transaction and report configuration_changed before any token exchange.
func (m *Manager) attachChangedConfigurationRejector(snapshot *runtimeSnapshot, buildProvider bool) {
	if !buildProvider || m.source != ConfigSourceDatabase || snapshot.service != nil {
		return
	}
	snapshot.service = oidcauth.NewService(unavailableProvider{}, m.transactions, oidcauth.Options{
		BasePath: m.basePath, TransactionTTL: 5 * time.Minute, Now: m.now,
	})
	snapshot.state.Available = true
}

func (m *Manager) buildService(cfg config.OIDCConfig, revision int64, fingerprint string) (*oidcauth.Service, error) {
	provider, err := m.providerFactory(remoteProviderConfig(cfg))
	if err != nil {
		return nil, err
	}
	return oidcauth.NewService(provider, m.transactions, oidcauth.Options{
		BasePath: m.basePath, TransactionTTL: 5 * time.Minute, Now: m.now,
		ConfigRevision: revision, ConfigFingerprint: fingerprint,
	}), nil
}

func (m *Manager) resolveStored(record StoredConfig, requireProvider bool) (config.OIDCConfig, error) {
	cfg := config.OIDCConfig{
		Enabled: record.Enabled, IssuerURL: record.IssuerURL, ClientID: record.ClientID,
		ProviderName: record.ProviderName, Scopes: strings.Fields(record.Scopes), PublicURL: record.PublicURL,
		AutoProvision: record.AutoProvision, DisablePasswordLogin: record.DisablePasswordLogin,
	}
	ttl, err := config.OIDCSessionTTLFromSeconds(record.SessionTTLSeconds)
	if err != nil {
		return cfg, err
	}
	cfg.SessionAbsoluteTTL = ttl
	secret := ""
	if record.SecretCiphertext != "" {
		if m.protector == nil {
			return cfg, errors.New("OIDC client secret protector is unavailable")
		}
		plaintext, err := m.protector.Decrypt(record.SecretCiphertext)
		if err != nil {
			return cfg, err
		}
		secret = string(plaintext)
	}
	cfg.ClientSecret = secret
	if m.forcePasswordLogin {
		cfg.DisablePasswordLogin = false
	}
	return config.ValidateOIDCConfig(cfg, m.basePath, requireProvider)
}

func (m *Manager) resolveConfigForFingerprint(cfg config.OIDCConfig) (config.OIDCConfig, error) {
	copy := cloneConfig(cfg)
	copy.Enabled = true
	copy.DisablePasswordLogin = false
	return config.ValidateOIDCConfig(copy, m.basePath, true)
}

func (m *Manager) recordFromFields(current StoredConfig, fields AdminFields, secret *string, clearSecret bool) (StoredConfig, error) {
	if secret != nil && clearSecret {
		return StoredConfig{}, fmt.Errorf("%w: client secret replacement and clearing are mutually exclusive", ErrConfigurationInvalid)
	}
	normalizedScopes, err := config.NormalizeOIDCScopes(fields.Scopes)
	if err != nil {
		return StoredConfig{}, fmt.Errorf("%w: %v", ErrConfigurationInvalid, err)
	}
	ttl, err := config.OIDCSessionTTLFromSeconds(fields.SessionTTLSeconds)
	if err != nil {
		return StoredConfig{}, fmt.Errorf("%w: %v", ErrConfigurationInvalid, err)
	}
	next := current
	next.Enabled = fields.Enabled
	next.IssuerURL = strings.TrimSpace(fields.IssuerURL)
	next.ClientID = fields.ClientID
	next.ProviderName = strings.TrimSpace(fields.ProviderName)
	if next.ProviderName == "" {
		next.ProviderName = "OpenID Connect"
	}
	next.Scopes = strings.Join(normalizedScopes, " ")
	next.PublicURL = strings.TrimSpace(fields.PublicURL)
	next.AutoProvision = fields.AutoProvision
	next.SessionTTLSeconds = int64(ttl / time.Second)
	next.DisablePasswordLogin = fields.DisablePasswordLogin
	if clearSecret {
		next.SecretCiphertext = ""
		next.SecretKeyID = ""
	}
	if secret != nil {
		if m.protector == nil {
			return StoredConfig{}, errors.New("OIDC client secret protector is unavailable")
		}
		value := *secret
		if value == "" {
			return StoredConfig{}, fmt.Errorf("%w: OIDC client secret cannot be empty", ErrConfigurationInvalid)
		}
		ciphertext, keyID, err := m.protector.Encrypt([]byte(value))
		if err != nil {
			return StoredConfig{}, err
		}
		next.SecretCiphertext = ciphertext
		next.SecretKeyID = keyID
	}
	return next, nil
}

func (m *Manager) adminConfigForSnapshot(snapshot *runtimeSnapshot) AdminConfig {
	if snapshot == nil {
		return AdminConfig{ManagedBy: m.source, BasePath: m.basePath, ForcePasswordLogin: m.forcePasswordLogin, ErrorCode: "configuration_unavailable"}
	}
	if m.source == ConfigSourceEnvironment {
		cfg := snapshot.state.Config
		return AdminConfig{
			ManagedBy: ConfigSourceEnvironment, Editable: false, Status: "environment-managed",
			ClientSecretConfigured: cfg.ClientSecret != "", CallbackURL: cfg.CallbackURL,
			BasePath:           m.basePath,
			ForcePasswordLogin: m.forcePasswordLogin,
			ErrorCode:          snapshot.state.ErrorCode, Config: adminFieldsFromConfig(cfg),
		}
	}
	record := snapshot.record
	status := "disabled"
	configured := record.IssuerURL != "" || record.ClientID != "" || record.SecretCiphertext != "" || record.PublicURL != ""
	if configured {
		status = "draft"
	}
	if snapshot.state.ErrorCode != "" {
		status = "invalid"
	} else if snapshot.fingerprint != "" && record.VerifiedFingerprint == snapshot.fingerprint {
		status = "ready"
		if record.Enabled && snapshot.state.Ready {
			status = "active"
		}
	}
	protection := SecretProtection("")
	if m.protector != nil {
		protection = m.protector.Protection()
	}
	return AdminConfig{
		ManagedBy: ConfigSourceDatabase, Editable: true, Status: status, Revision: record.Revision,
		ClientSecretConfigured: record.SecretCiphertext != "", SecretProtection: protection,
		ForcePasswordLogin: m.forcePasswordLogin,
		BasePath:           m.basePath,
		CallbackURL:        snapshot.state.Config.CallbackURL, LastVerifiedAt: cloneTime(record.LastVerifiedAt),
		ErrorCode: snapshot.state.ErrorCode, Config: adminFieldsFromRecord(record),
	}
}

func remoteProviderConfig(cfg config.OIDCConfig) oidcauth.RemoteProviderConfig {
	return oidcauth.RemoteProviderConfig{
		IssuerURL: cfg.IssuerURL, ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
		RedirectURL: cfg.CallbackURL, Scopes: append([]string(nil), cfg.Scopes...),
	}
}

func storedFromConfig(cfg config.OIDCConfig) StoredConfig {
	return StoredConfig{
		Enabled: cfg.Enabled, IssuerURL: cfg.IssuerURL, ClientID: cfg.ClientID,
		ProviderName: cfg.ProviderName, Scopes: strings.Join(cfg.Scopes, " "), PublicURL: cfg.PublicURL,
		AutoProvision: cfg.AutoProvision, SessionTTLSeconds: int64(cfg.SessionAbsoluteTTL / time.Second),
		DisablePasswordLogin: cfg.DisablePasswordLogin,
	}
}

func adminFieldsFromConfig(cfg config.OIDCConfig) AdminFields {
	return AdminFields{
		Enabled: cfg.Enabled, IssuerURL: cfg.IssuerURL, ClientID: cfg.ClientID, ProviderName: cfg.ProviderName,
		Scopes: append([]string(nil), cfg.Scopes...), PublicURL: cfg.PublicURL, AutoProvision: cfg.AutoProvision,
		SessionTTLSeconds: int64(cfg.SessionAbsoluteTTL / time.Second), DisablePasswordLogin: cfg.DisablePasswordLogin,
	}
}

func adminFieldsFromRecord(record StoredConfig) AdminFields {
	return AdminFields{
		Enabled: record.Enabled, IssuerURL: record.IssuerURL, ClientID: record.ClientID, ProviderName: record.ProviderName,
		Scopes: strings.Fields(record.Scopes), PublicURL: record.PublicURL, AutoProvision: record.AutoProvision,
		SessionTTLSeconds: record.SessionTTLSeconds, DisablePasswordLogin: record.DisablePasswordLogin,
	}
}

func protocolFingerprint(cfg config.OIDCConfig, basePath, secretVersion string) string {
	payload, _ := json.Marshal(struct {
		IssuerURL     string   `json:"issuerURL"`
		ClientID      string   `json:"clientID"`
		SecretVersion string   `json:"secretVersion"`
		Scopes        []string `json:"scopes"`
		PublicURL     string   `json:"publicURL"`
		BasePath      string   `json:"basePath"`
	}{
		IssuerURL: cfg.IssuerURL, ClientID: cfg.ClientID, SecretVersion: secretVersion,
		Scopes: cfg.Scopes, PublicURL: cfg.PublicURL, BasePath: basePath,
	})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func opaqueFingerprintToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func changedFields(before, after StoredConfig, secretChanged bool) []string {
	fields := make([]string, 0, 10)
	if before.Enabled != after.Enabled {
		fields = append(fields, "enabled")
	}
	if before.IssuerURL != after.IssuerURL {
		fields = append(fields, "issuerURL")
	}
	if before.ClientID != after.ClientID {
		fields = append(fields, "clientID")
	}
	if secretChanged {
		fields = append(fields, "clientSecret")
	}
	if before.ProviderName != after.ProviderName {
		fields = append(fields, "providerName")
	}
	if before.Scopes != after.Scopes {
		fields = append(fields, "scopes")
	}
	if before.PublicURL != after.PublicURL {
		fields = append(fields, "publicURL")
	}
	if before.AutoProvision != after.AutoProvision {
		fields = append(fields, "autoProvision")
	}
	if before.SessionTTLSeconds != after.SessionTTLSeconds {
		fields = append(fields, "sessionTTLSeconds")
	}
	if before.DisablePasswordLogin != after.DisablePasswordLogin {
		fields = append(fields, "disablePasswordLogin")
	}
	return fields
}

func cloneConfig(cfg config.OIDCConfig) config.OIDCConfig {
	cfg.Scopes = append([]string(nil), cfg.Scopes...)
	if cfg.BootstrapAdminSubjects != nil {
		cfg.BootstrapAdminSubjects = make(map[string]struct{}, len(cfg.BootstrapAdminSubjects))
		for subject := range cfg.BootstrapAdminSubjects {
			cfg.BootstrapAdminSubjects[subject] = struct{}{}
		}
	}
	return cfg
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func configErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, oidcauth.ErrProviderUnavailable) {
		return "provider_unavailable"
	}
	return "configuration_invalid"
}
