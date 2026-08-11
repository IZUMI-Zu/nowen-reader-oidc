package oidcruntime

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	oidcauth "github.com/nowen-reader/nowen-reader/internal/auth/oidc"
	"github.com/nowen-reader/nowen-reader/internal/config"
)

type memoryConfigRepository struct {
	mu               sync.Mutex
	record           StoredConfig
	actorIsAdmin     bool
	adminLinked      bool
	actorHasPassword bool
	oidcOnlyUsers    int64
	audits           []AuditEvent
	beforeSave       func(*memoryConfigRepository)
}

func TestProtocolFingerprintUsesOpaqueSecretVersionNotPlaintextSecret(t *testing.T) {
	cfg := config.OIDCConfig{
		IssuerURL: "https://identity.example.com", ClientID: "client", ClientSecret: "guessable-secret",
		Scopes: []string{"openid"}, PublicURL: "https://reader.example.com",
	}
	first := protocolFingerprint(cfg, "/reader", "opaque-ciphertext-version-1")
	cfg.ClientSecret = "different-secret"
	if got := protocolFingerprint(cfg, "/reader", "opaque-ciphertext-version-1"); got != first {
		t.Fatal("fingerprint depends on plaintext Client Secret")
	}
	if got := protocolFingerprint(cfg, "/reader", "opaque-ciphertext-version-2"); got == first {
		t.Fatal("fingerprint ignored opaque secret version change")
	}
}

func newMemoryConfigRepository() *memoryConfigRepository {
	return &memoryConfigRepository{record: StoredConfig{
		ProviderName: "OpenID Connect", Scopes: "openid profile email", SessionTTLSeconds: 43200,
	}, actorIsAdmin: true}
}

func (r *memoryConfigRepository) Load(context.Context) (StoredConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneStoredConfig(r.record), nil
}

func (r *memoryConfigRepository) Save(_ context.Context, request SaveRequest) (StoredConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if beforeSave := r.beforeSave; beforeSave != nil {
		r.beforeSave = nil
		beforeSave(r)
	}
	if r.record.Revision != request.ExpectedRevision {
		return StoredConfig{}, ErrConfigConflict
	}
	if !r.actorIsAdmin {
		return StoredConfig{}, ErrAdministratorRequired
	}
	if request.RequireActorIdentityIssuer != "" && !r.adminLinked {
		return StoredConfig{}, ErrAdminIdentityRequired
	}
	if request.RequireActorPassword && !r.actorHasPassword {
		return StoredConfig{}, ErrBreakGlassPasswordRequired
	}
	if request.RequireNoOIDCOnlyUsersIssuer != "" && r.oidcOnlyUsers > 0 {
		return StoredConfig{}, ErrOIDCOnlyUsersConfirmation
	}
	next := request.Next
	next.UpdatedBy = request.Audit.ActorUserID
	next.Revision = request.ExpectedRevision + 1
	r.record = cloneStoredConfig(next)
	r.audits = append(r.audits, request.Audit)
	return cloneStoredConfig(r.record), nil
}

func TestApplyRechecksAccountSafetyAtSaveBoundary(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*memoryConfigRepository, *AdminFields, *UpdateRequest)
		wantErr error
	}{
		{
			name: "administrator identity was unlinked after preflight",
			prepare: func(repository *memoryConfigRepository, _ *AdminFields, _ *UpdateRequest) {
				repository.beforeSave = func(repository *memoryConfigRepository) { repository.adminLinked = false }
			},
			wantErr: ErrAdminIdentityRequired,
		},
		{
			name: "recovery password was removed after preflight",
			prepare: func(repository *memoryConfigRepository, fields *AdminFields, request *UpdateRequest) {
				fields.DisablePasswordLogin = true
				request.ConfirmDisablePasswordLogin = true
				repository.beforeSave = func(repository *memoryConfigRepository) { repository.actorHasPassword = false }
			},
			wantErr: ErrBreakGlassPasswordRequired,
		},
		{
			name: "OIDC-only user appeared after preflight",
			prepare: func(repository *memoryConfigRepository, fields *AdminFields, _ *UpdateRequest) {
				fields.Enabled = false
				repository.beforeSave = func(repository *memoryConfigRepository) { repository.oidcOnlyUsers = 1 }
			},
			wantErr: ErrOIDCOnlyUsersConfirmation,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			protector, err := NewAESGCMSecretProtector(
				[]byte("0123456789abcdef0123456789abcdef"), SecretProtectionExternalKey,
			)
			if err != nil {
				t.Fatal(err)
			}
			const secret = "client-secret"
			ciphertext, keyID, err := protector.Encrypt([]byte(secret))
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.OIDCConfig{
				Enabled: true, IssuerURL: "https://identity.example.com", ClientID: "reader", ClientSecret: secret,
				ProviderName: "Company Login", Scopes: []string{"openid", "profile", "email"},
				PublicURL: "https://reader.example.com", SessionAbsoluteTTL: 12 * time.Hour,
			}
			repository := newMemoryConfigRepository()
			repository.record = StoredConfig{
				Enabled: true, IssuerURL: cfg.IssuerURL, ClientID: cfg.ClientID,
				SecretCiphertext: ciphertext, SecretKeyID: keyID, ProviderName: cfg.ProviderName,
				Scopes: strings.Join(cfg.Scopes, " "), PublicURL: cfg.PublicURL, SessionTTLSeconds: 43200,
				Revision: 7, VerifiedFingerprint: protocolFingerprint(cfg, "/reader", ciphertext),
			}
			repository.adminLinked = true
			repository.actorHasPassword = true
			manager, err := NewManager(context.Background(), Options{
				Source: ConfigSourceDatabase, Repository: repository, Safety: repository,
				Transactions: &memoryTransactionStore{}, Protector: protector, BasePath: "/reader",
				ProviderFactory: func(cfg oidcauth.RemoteProviderConfig) (probeProvider, error) {
					return &fakeProbeProvider{issuer: cfg.IssuerURL}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			fields := adminFieldsFromRecord(repository.record)
			request := UpdateRequest{ExpectedRevision: repository.record.Revision, ActorUserID: "admin"}
			tt.prepare(repository, &fields, &request)
			request.Fields = fields

			if _, err := manager.Apply(context.Background(), request); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Apply() error = %v, want %v", err, tt.wantErr)
			}
			if repository.record.Revision != 7 {
				t.Fatalf("rejected Apply() changed revision to %d", repository.record.Revision)
			}
		})
	}
}

func (r *memoryConfigRepository) CompleteTest(_ context.Context, expected int64, fingerprint string, at time.Time, actorUserID string, identity oidcauth.VerifiedIdentity, audit AuditEvent) (StoredConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record.Revision != expected || actorUserID == "" || identity.Issuer == "" || identity.Subject == "" {
		return StoredConfig{}, ErrConfigConflict
	}
	r.record.Revision++
	r.record.VerifiedFingerprint = fingerprint
	r.record.LastVerifiedAt = &at
	r.adminLinked = true
	r.audits = append(r.audits, audit)
	return cloneStoredConfig(r.record), nil
}

func (r *memoryConfigRepository) RecordAudit(_ context.Context, _ int64, audit AuditEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.audits = append(r.audits, audit)
	return nil
}

func (r *memoryConfigRepository) UserHasIdentityForIssuer(_ context.Context, userID, _ string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.adminLinked && userID == "admin", nil
}

func (r *memoryConfigRepository) OIDCOnlyUserCount(context.Context, string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.oidcOnlyUsers, nil
}

func (r *memoryConfigRepository) UserHasPassword(context.Context, string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.actorHasPassword, nil
}

func TestInvalidDatabaseConfigurationReopensPasswordLoginOnStartup(t *testing.T) {
	repository := newMemoryConfigRepository()
	repository.record = StoredConfig{
		Enabled: true, DisablePasswordLogin: true,
		IssuerURL: "http://invalid.example.com", ClientID: "client", ProviderName: "Company Login",
		Scopes: "openid", PublicURL: "https://reader.example.com", SessionTTLSeconds: 43200,
	}
	manager, err := NewManager(context.Background(), Options{
		Source: ConfigSourceDatabase, Repository: repository, Transactions: &memoryTransactionStore{}, BasePath: "/reader",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := manager.State()
	if state.Ready || state.ErrorCode == "" {
		t.Fatalf("invalid database configuration was treated as ready: %+v", state)
	}
	if state.Config.DisablePasswordLogin || !state.PasswordLoginPolicyDisabled {
		t.Fatalf("invalid database configuration lost effective recovery or persisted policy: %+v", state)
	}
}

func TestUnavailableDatabaseProviderReopensPasswordLoginOnStartup(t *testing.T) {
	repository := newMemoryConfigRepository()
	protector, err := NewAESGCMSecretProtector([]byte("0123456789abcdef0123456789abcdef"), SecretProtectionExternalKey)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, keyID, err := protector.Encrypt([]byte("client-secret"))
	if err != nil {
		t.Fatal(err)
	}
	repository.record = StoredConfig{
		Enabled: true, DisablePasswordLogin: true,
		IssuerURL: "https://identity.example.com", ClientID: "client", SecretCiphertext: ciphertext, SecretKeyID: keyID,
		ProviderName: "Company Login", Scopes: "openid", PublicURL: "https://reader.example.com", SessionTTLSeconds: 43200,
	}
	manager, err := NewManager(context.Background(), Options{
		Source: ConfigSourceDatabase, Repository: repository, Transactions: &memoryTransactionStore{}, Protector: protector, BasePath: "/reader",
		ProviderFactory: func(oidcauth.RemoteProviderConfig) (probeProvider, error) {
			return nil, oidcauth.ErrProviderUnavailable
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := manager.State()
	if state.Ready || state.ErrorCode == "" {
		t.Fatalf("unavailable database provider was treated as ready: %+v", state)
	}
	if state.Config.DisablePasswordLogin || !state.PasswordLoginPolicyDisabled {
		t.Fatalf("unavailable database provider lost effective recovery or persisted policy: %+v", state)
	}
}

func TestForcePasswordLoginPreservesManagedLockoutPolicy(t *testing.T) {
	manager, err := NewManager(context.Background(), Options{
		Source: ConfigSourceEnvironment,
		EnvironmentConfig: config.OIDCConfig{
			Enabled: true, DisablePasswordLogin: true,
			IssuerURL: "https://identity.example.com", ClientID: "client", ClientSecret: "secret",
			ProviderName: "Company Login", Scopes: []string{"openid"}, PublicURL: "https://reader.example.com",
			SessionAbsoluteTTL: 12 * time.Hour,
		},
		Transactions: &memoryTransactionStore{}, BasePath: "/reader", ForcePasswordLogin: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := manager.State()
	if state.Config.DisablePasswordLogin || !state.PasswordLoginPolicyDisabled {
		t.Fatalf("recovery override lost effective recovery or managed policy: %+v", state)
	}
}

func TestInvalidEnvironmentConfigurationRemainsFailClosedOnStartup(t *testing.T) {
	manager, err := NewManager(context.Background(), Options{
		Source: ConfigSourceEnvironment,
		EnvironmentConfig: config.OIDCConfig{
			Enabled: true, DisablePasswordLogin: true,
		},
		EnvironmentError: errors.New("invalid environment configuration"),
		Transactions:     &memoryTransactionStore{},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := manager.State()
	if state.Ready || state.ErrorCode == "" || !state.Config.DisablePasswordLogin {
		t.Fatalf("invalid environment configuration lost fail-closed policy: %+v", state)
	}
}

func TestManagerAuditsRejectedUpdatesWithStableSecretFreeResult(t *testing.T) {
	repository := newMemoryConfigRepository()
	manager, err := NewManager(context.Background(), Options{
		Source: ConfigSourceDatabase, Repository: repository, Safety: repository, Transactions: &memoryTransactionStore{},
	})
	if err != nil {
		t.Fatal(err)
	}
	secret := "must-never-appear-in-audit"
	_, err = manager.Apply(context.Background(), UpdateRequest{
		ExpectedRevision: 0, ActorUserID: "admin", RequestID: "request-invalid",
		Fields:       AdminFields{IssuerURL: "https://identity.example.com", ClientID: "client", Scopes: []string{"openid"}, SessionTTLSeconds: 1},
		ClientSecret: &secret,
	})
	if !errors.Is(err, ErrConfigurationInvalid) {
		t.Fatalf("Apply() error = %v, want ErrConfigurationInvalid", err)
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.audits) != 1 {
		t.Fatalf("failure audits = %+v", repository.audits)
	}
	audit := repository.audits[0]
	if audit.ActorUserID != "admin" || audit.Action != "update" || audit.Result != "failure:configuration_invalid" ||
		audit.RequestID != "request-invalid" || strings.Contains(audit.Result, secret) {
		t.Fatalf("unsafe failure audit = %+v", audit)
	}
}

type memoryTransactionStore struct {
	mu    sync.Mutex
	items map[string]oidcauth.LoginTransaction
}

func (s *memoryTransactionStore) Create(_ context.Context, transaction oidcauth.LoginTransaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = make(map[string]oidcauth.LoginTransaction)
	}
	s.items[transaction.StateHash+":"+transaction.BindingHash] = transaction
	return nil
}

func (s *memoryTransactionStore) Consume(_ context.Context, stateHash, bindingHash string, now time.Time) (oidcauth.LoginTransaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := stateHash + ":" + bindingHash
	transaction, ok := s.items[key]
	if !ok || !transaction.ExpiresAt.After(now) {
		return oidcauth.LoginTransaction{}, oidcauth.ErrInvalidTransaction
	}
	delete(s.items, key)
	return transaction, nil
}

type fakeProbeProvider struct {
	issuer   string
	clientID string
	mu       sync.Mutex
	nonce    string
	probes   int
}

func (p *fakeProbeProvider) Issuer() string { return p.issuer }

func (p *fakeProbeProvider) Probe(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probes++
	return nil
}

func (p *fakeProbeProvider) AuthorizationURL(_ context.Context, request oidcauth.AuthorizationRequest) (string, error) {
	p.mu.Lock()
	p.nonce = request.Nonce
	p.mu.Unlock()
	return "https://identity.example.com/authorize?state=" + url.QueryEscape(request.State) + "&client_id=" + url.QueryEscape(p.clientID), nil
}

func (p *fakeProbeProvider) Exchange(context.Context, string, string) (oidcauth.VerifiedIdentity, error) {
	p.mu.Lock()
	nonce := p.nonce
	p.mu.Unlock()
	return oidcauth.VerifiedIdentity{
		Issuer: p.issuer, Subject: "admin-subject", Nonce: nonce, AuthTime: time.Unix(1_700_000_000, 0).UTC(), PreferredUsername: "admin",
	}, nil
}

func TestManagerDraftTestVerifyActivateAndHotReload(t *testing.T) {
	repository := newMemoryConfigRepository()
	transactions := &memoryTransactionStore{}
	protector, err := NewAESGCMSecretProtector([]byte("0123456789abcdef0123456789abcdef"), SecretProtectionExternalKey)
	if err != nil {
		t.Fatal(err)
	}
	var provider *fakeProbeProvider
	manager, err := NewManager(context.Background(), Options{
		Source: ConfigSourceDatabase, Repository: repository, Safety: repository, Transactions: transactions,
		Protector: protector, BasePath: "/reader", Now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		ProviderFactory: func(cfg oidcauth.RemoteProviderConfig) (probeProvider, error) {
			provider = &fakeProbeProvider{issuer: cfg.IssuerURL}
			return provider, nil
		},
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	got, err := manager.AdminConfig(context.Background())
	if err != nil || got.Status != "disabled" || !got.Editable {
		t.Fatalf("initial admin config = %+v", got)
	}

	secret := "client-secret"
	fields := AdminFields{
		IssuerURL: "https://identity.example.com", ClientID: "nowen-reader", ProviderName: "Company Login",
		Scopes: []string{"openid", "profile", "email", "profile"}, PublicURL: "https://reader.example.com",
		AutoProvision: true, SessionTTLSeconds: 28800,
	}
	draft, err := manager.Apply(context.Background(), UpdateRequest{
		ExpectedRevision: 0, ActorUserID: "admin", Fields: fields, ClientSecret: &secret,
	})
	if err != nil {
		t.Fatalf("save draft error = %v", err)
	}
	if draft.Status != "draft" || draft.Revision != 1 || !draft.ClientSecretConfigured {
		t.Fatalf("saved draft = %+v", draft)
	}
	wantFields := fields
	wantFields.Scopes = []string{"openid", "profile", "email"}
	if !reflect.DeepEqual(draft.Config, wantFields) {
		t.Fatalf("UI/API fields round trip = %+v, want %+v", draft.Config, wantFields)
	}
	stored, err := repository.Load(context.Background())
	if err != nil || stored.IssuerURL != wantFields.IssuerURL || stored.ClientID != wantFields.ClientID || stored.ProviderName != wantFields.ProviderName ||
		stored.Scopes != "openid profile email" || stored.PublicURL != wantFields.PublicURL || !stored.AutoProvision || stored.SessionTTLSeconds != wantFields.SessionTTLSeconds {
		t.Fatalf("stored field mapping = %+v, %v", stored, err)
	}

	if _, err := manager.Probe(context.Background(), ProbeRequest{Fields: fields}); err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if provider == nil || provider.probes != 1 {
		t.Fatalf("provider probes = %+v", provider)
	}

	begin, err := manager.BeginConfigTest(context.Background(), "admin", "admin-session", "/reader/settings?tab=authentication")
	if err != nil {
		t.Fatalf("BeginConfigTest() error = %v", err)
	}
	authorizationURL, _ := url.Parse(begin.URL)
	identity, err := manager.Complete(context.Background(), oidcauth.CallbackRequest{
		State: authorizationURL.Query().Get("state"), Code: "valid-code", BindingToken: begin.BindingToken,
	})
	if err != nil {
		t.Fatalf("Complete(config test) error = %v", err)
	}
	if identity.Purpose != oidcauth.PurposeConfigTest || identity.SessionUserID != "admin" || identity.SessionID != "admin-session" || identity.Subject != "admin-subject" {
		t.Fatalf("test identity = %+v", identity)
	}

	repository.actorHasPassword = true
	if _, err := manager.CompleteConfigTest(context.Background(), "admin", "other-session", "request-wrong-session", identity); !errors.Is(err, oidcauth.ErrInvalidTransaction) {
		t.Fatalf("CompleteConfigTest(wrong session) error = %v, want ErrInvalidTransaction", err)
	}
	if repository.adminLinked {
		t.Fatal("wrong administrator session verified or bound the configuration")
	}
	verified, err := manager.CompleteConfigTest(context.Background(), "admin", "admin-session", "request-verify", identity)
	if err != nil {
		t.Fatalf("CompleteConfigTest() error = %v", err)
	}
	if verified.Status != "ready" || verified.Revision != 2 || verified.LastVerifiedAt == nil {
		t.Fatalf("verified config = %+v", verified)
	}

	fields.Enabled = true
	active, err := manager.Apply(context.Background(), UpdateRequest{
		ExpectedRevision: 2, ActorUserID: "admin", Fields: fields,
	})
	if err != nil {
		t.Fatalf("activate error = %v", err)
	}
	if active.Status != "active" || !manager.State().Ready || !manager.State().Config.Enabled {
		t.Fatalf("active config = %+v state=%+v", active, manager.State())
	}

	fields.ProviderName = "Renamed Login"
	fields.DisablePasswordLogin = true
	if _, err := manager.Apply(context.Background(), UpdateRequest{
		ExpectedRevision: 3, ActorUserID: "admin", Fields: fields,
	}); !errors.Is(err, ErrDisablePasswordConfirmation) {
		t.Fatalf("disable password without explicit confirmation error = %v", err)
	}
	policyOnly, err := manager.Apply(context.Background(), UpdateRequest{
		ExpectedRevision: 3, ActorUserID: "admin", Fields: fields, ConfirmDisablePasswordLogin: true,
	})
	if err != nil {
		t.Fatalf("policy-only update error = %v", err)
	}
	if policyOnly.Status != "active" || !manager.State().Config.DisablePasswordLogin {
		t.Fatalf("policy-only config = %+v", policyOnly)
	}

	fields.ClientID = "replacement-client"
	if _, err := manager.Apply(context.Background(), UpdateRequest{
		ExpectedRevision: 4, ActorUserID: "admin", Fields: fields,
	}); err != ErrConfigurationUnverified {
		t.Fatalf("protocol update error = %v, want ErrConfigurationUnverified", err)
	}

	fields.ClientID = "nowen-reader"
	fields.Enabled = false
	fields.DisablePasswordLogin = false
	repository.actorHasPassword = false
	if _, err := manager.Apply(context.Background(), UpdateRequest{ExpectedRevision: 4, ActorUserID: "admin", Fields: fields}); !errors.Is(err, ErrBreakGlassPasswordRequired) {
		t.Fatalf("disable without recovery password error = %v", err)
	}
	repository.actorHasPassword = true
	repository.oidcOnlyUsers = 2
	if _, err := manager.Apply(context.Background(), UpdateRequest{ExpectedRevision: 4, ActorUserID: "admin", Fields: fields}); !errors.Is(err, ErrOIDCOnlyUsersConfirmation) {
		t.Fatalf("disable without affected-user confirmation error = %v", err)
	}
	disabled, err := manager.Apply(context.Background(), UpdateRequest{
		ExpectedRevision: 4, ActorUserID: "admin", Fields: fields, ConfirmOIDCOnlyUsers: true,
	})
	if err != nil || disabled.Status != "ready" || disabled.Config.Enabled || disabled.OIDCOnlyUserCount != 2 {
		t.Fatalf("confirmed disable = %+v, %v", disabled, err)
	}
}

func TestCompleteConfigTestCannotVerifyOrBindAChangedProtocolConfig(t *testing.T) {
	repository := newMemoryConfigRepository()
	protector, err := NewAESGCMSecretProtector([]byte("0123456789abcdef0123456789abcdef"), SecretProtectionExternalKey)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(context.Background(), Options{
		Source: ConfigSourceDatabase, Repository: repository, Safety: repository, Transactions: &memoryTransactionStore{},
		Protector: protector, BasePath: "/reader", Now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		ProviderFactory: func(cfg oidcauth.RemoteProviderConfig) (probeProvider, error) {
			return &fakeProbeProvider{issuer: cfg.IssuerURL}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	secret := "client-secret"
	fields := AdminFields{
		IssuerURL: "https://identity.example.com", ClientID: "client-a", Scopes: []string{"openid"},
		PublicURL: "https://reader.example.com", SessionTTLSeconds: 43200,
	}
	if _, err := manager.Apply(context.Background(), UpdateRequest{
		ExpectedRevision: 0, ActorUserID: "admin", Fields: fields, ClientSecret: &secret,
	}); err != nil {
		t.Fatal(err)
	}
	begin, err := manager.BeginConfigTest(context.Background(), "admin", "admin-session", "/reader/settings?tab=authentication")
	if err != nil {
		t.Fatal(err)
	}
	authorizationURL, _ := url.Parse(begin.URL)
	identity, err := manager.Complete(context.Background(), oidcauth.CallbackRequest{
		State: authorizationURL.Query().Get("state"), Code: "valid-code", BindingToken: begin.BindingToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	fields.ClientID = "client-b"
	if _, err := manager.Apply(context.Background(), UpdateRequest{ExpectedRevision: 1, ActorUserID: "admin", Fields: fields}); err != nil {
		t.Fatalf("save replacement draft: %v", err)
	}
	if _, err := manager.CompleteConfigTest(context.Background(), "admin", "admin-session", "request", identity); !errors.Is(err, oidcauth.ErrConfigurationChanged) {
		t.Fatalf("stale config test error = %v", err)
	}
	record, err := repository.Load(context.Background())
	if err != nil || record.VerifiedFingerprint != "" || repository.adminLinked {
		t.Fatalf("changed config was verified or bound: %+v linked=%v err=%v", record, repository.adminLinked, err)
	}
}

func TestIncompleteReplacementDraftStillRejectsOldTransactionAsConfigurationChanged(t *testing.T) {
	repository := newMemoryConfigRepository()
	repository.adminLinked = true
	repository.actorHasPassword = true
	transactions := &memoryTransactionStore{}
	protector, err := NewAESGCMSecretProtector([]byte("0123456789abcdef0123456789abcdef"), SecretProtectionExternalKey)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(context.Background(), Options{
		Source: ConfigSourceDatabase, Repository: repository, Safety: repository, Transactions: transactions,
		Protector: protector, BasePath: "/reader",
		ProviderFactory: func(cfg oidcauth.RemoteProviderConfig) (probeProvider, error) {
			return &fakeProbeProvider{issuer: cfg.IssuerURL, clientID: cfg.ClientID}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	secret := "secret"
	fields := AdminFields{
		IssuerURL: "https://identity.example.com", ClientID: "client", ProviderName: "Original",
		Scopes: []string{"openid"}, PublicURL: "https://reader.example.com", SessionTTLSeconds: 43200,
	}
	if _, err := manager.Apply(context.Background(), UpdateRequest{
		ExpectedRevision: 0, ActorUserID: "admin", Fields: fields, ClientSecret: &secret,
	}); err != nil {
		t.Fatal(err)
	}
	begin, err := manager.BeginConfigTest(context.Background(), "admin", "admin-session", "/reader/settings")
	if err != nil {
		t.Fatal(err)
	}
	authorizationURL, err := url.Parse(begin.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(context.Background(), UpdateRequest{
		ExpectedRevision: 1,
		ActorUserID:      "admin",
		Fields:           AdminFields{ProviderName: "Cleared", Scopes: []string{"openid"}, SessionTTLSeconds: 43200},
		ClearSecret:      true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Complete(context.Background(), oidcauth.CallbackRequest{
		State: authorizationURL.Query().Get("state"), Code: "code", BindingToken: begin.BindingToken,
	}); !errors.Is(err, oidcauth.ErrConfigurationChanged) {
		t.Fatalf("old transaction error = %v, want ErrConfigurationChanged", err)
	}
}

func TestDatabaseApplyWaitsForCallbackFinalizationAndPublishesOneSnapshot(t *testing.T) {
	repository := newMemoryConfigRepository()
	repository.adminLinked = true
	repository.actorHasPassword = true
	transactions := &memoryTransactionStore{}
	now := time.Unix(1_700_000_000, 0).UTC()
	protector, err := NewAESGCMSecretProtector([]byte("0123456789abcdef0123456789abcdef"), SecretProtectionExternalKey)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, keyID, err := protector.Encrypt([]byte("secret-a"))
	if err != nil {
		t.Fatal(err)
	}
	initialConfig := config.OIDCConfig{
		Enabled: true, IssuerURL: "https://identity.example.com", ClientID: "client-a", ClientSecret: "secret-a",
		ProviderName: "Original", PublicURL: "https://reader.example.com",
		CallbackURL: "https://reader.example.com/reader/api/auth/oidc/callback",
		Scopes:      []string{"openid"}, SessionAbsoluteTTL: 12 * time.Hour,
	}
	repository.record = StoredConfig{
		Enabled: true, IssuerURL: initialConfig.IssuerURL, ClientID: initialConfig.ClientID,
		SecretCiphertext: ciphertext, SecretKeyID: keyID, ProviderName: initialConfig.ProviderName,
		Scopes: "openid", PublicURL: initialConfig.PublicURL, SessionTTLSeconds: 43200, Revision: 1,
		VerifiedFingerprint: protocolFingerprint(initialConfig, "/reader", ciphertext),
	}
	manager, err := NewManager(context.Background(), Options{
		Source: ConfigSourceDatabase, Repository: repository, Safety: repository,
		Transactions: transactions, Protector: protector, BasePath: "/reader", Now: func() time.Time { return now },
		ProviderFactory: func(cfg oidcauth.RemoteProviderConfig) (probeProvider, error) {
			return &fakeProbeProvider{issuer: cfg.IssuerURL, clientID: cfg.ClientID}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	staleBegin, err := manager.Begin(context.Background(), oidcauth.BeginRequest{Purpose: oidcauth.PurposeLogin, ReturnTo: "/reader/stale"})
	if err != nil {
		t.Fatal(err)
	}
	begin, err := manager.Begin(context.Background(), oidcauth.BeginRequest{Purpose: oidcauth.PurposeLogin, ReturnTo: "/reader/"})
	if err != nil {
		t.Fatal(err)
	}
	authorizationURL, err := url.Parse(begin.URL)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	completeDone := make(chan error, 1)
	go func() {
		_, completeErr := manager.CompleteAndFinalize(context.Background(), oidcauth.CallbackRequest{
			State: authorizationURL.Query().Get("state"), Code: "valid-code", BindingToken: begin.BindingToken,
		}, func(state State, _ oidcauth.AuthenticatedIdentity) error {
			if !state.Config.Enabled || state.Config.ClientID != "client-a" || state.Config.ProviderName != "Original" {
				return fmt.Errorf("callback observed mixed configuration: %+v", state.Config)
			}
			close(entered)
			<-release
			return nil
		})
		completeDone <- completeErr
	}()
	<-entered
	applyDone := make(chan error, 1)
	go func() {
		_, applyErr := manager.Apply(context.Background(), UpdateRequest{
			ExpectedRevision: 1,
			ActorUserID:      "admin",
			Fields: AdminFields{
				Enabled: false, IssuerURL: initialConfig.IssuerURL, ClientID: "client-b", ProviderName: "Replacement",
				Scopes: []string{"openid"}, PublicURL: initialConfig.PublicURL, SessionTTLSeconds: 43200,
			},
		})
		applyDone <- applyErr
	}()
	select {
	case applyErr := <-applyDone:
		t.Fatalf("Apply completed before callback finalization was released: %v", applyErr)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-completeDone; err != nil {
		t.Fatalf("CompleteAndFinalize() error = %v", err)
	}
	if err := <-applyDone; err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	state := manager.State()
	if state.Config.Enabled || state.Config.ClientID != "client-b" || state.Config.ProviderName != "Replacement" || state.Ready {
		t.Fatalf("published state = %+v", state)
	}
	staleURL, err := url.Parse(staleBegin.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Complete(context.Background(), oidcauth.CallbackRequest{
		State: staleURL.Query().Get("state"), Code: "valid-code", BindingToken: staleBegin.BindingToken,
	}); !errors.Is(err, oidcauth.ErrConfigurationChanged) {
		t.Fatalf("stale transaction error = %v, want ErrConfigurationChanged", err)
	}
	newBegin, err := manager.BeginConfigTest(context.Background(), "admin", "admin-session", "/reader/settings")
	if err != nil {
		t.Fatal(err)
	}
	newURL, err := url.Parse(newBegin.URL)
	if err != nil || newURL.Query().Get("client_id") != "client-b" {
		t.Fatalf("new transaction used mixed provider: %q, %v", newBegin.URL, err)
	}
}

func TestApplyWaitsForGenericStateLease(t *testing.T) {
	manager, err := NewManager(context.Background(), Options{
		Source:            ConfigSourceEnvironment,
		EnvironmentConfig: config.OIDCConfig{},
		Transactions:      &memoryTransactionStore{},
	})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	leaseDone := make(chan error, 1)
	go func() {
		leaseDone <- manager.WithStateLease(func(State) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	applyDone := make(chan error, 1)
	go func() {
		_, applyErr := manager.Apply(context.Background(), UpdateRequest{})
		applyDone <- applyErr
	}()
	select {
	case applyErr := <-applyDone:
		t.Fatalf("Apply bypassed active state lease: %v", applyErr)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-leaseDone; err != nil {
		t.Fatal(err)
	}
	if err := <-applyDone; !errors.Is(err, ErrEnvironmentManaged) {
		t.Fatalf("Apply() error = %v, want ErrEnvironmentManaged after lease release", err)
	}
}

func cloneStoredConfig(value StoredConfig) StoredConfig {
	value.LastVerifiedAt = cloneTime(value.LastVerifiedAt)
	return value
}
