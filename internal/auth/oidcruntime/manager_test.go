package oidcruntime

import (
	"context"
	"errors"
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
	adminLinked      bool
	actorHasPassword bool
	oidcOnlyUsers    int64
	audits           []AuditEvent
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
	}}
}

func (r *memoryConfigRepository) Load(context.Context) (StoredConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneStoredConfig(r.record), nil
}

func (r *memoryConfigRepository) Save(_ context.Context, expected int64, next StoredConfig, audit AuditEvent) (StoredConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record.Revision != expected {
		return StoredConfig{}, ErrConfigConflict
	}
	next.Revision = expected + 1
	r.record = cloneStoredConfig(next)
	r.audits = append(r.audits, audit)
	return cloneStoredConfig(r.record), nil
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
	issuer string
	mu     sync.Mutex
	nonce  string
	probes int
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
	return "https://identity.example.com/authorize?state=" + url.QueryEscape(request.State), nil
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

	begin, err := manager.BeginConfigTest(context.Background(), "admin", "/reader/settings?tab=authentication")
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
	if identity.Purpose != oidcauth.PurposeConfigTest || identity.SessionUserID != "admin" || identity.Subject != "admin-subject" {
		t.Fatalf("test identity = %+v", identity)
	}

	repository.actorHasPassword = true
	verified, err := manager.CompleteConfigTest(context.Background(), "admin", "request-verify", identity)
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
	begin, err := manager.BeginConfigTest(context.Background(), "admin", "/reader/settings?tab=authentication")
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
	if _, err := manager.CompleteConfigTest(context.Background(), "admin", "request", identity); !errors.Is(err, oidcauth.ErrConfigurationChanged) {
		t.Fatalf("stale config test error = %v", err)
	}
	record, err := repository.Load(context.Background())
	if err != nil || record.VerifiedFingerprint != "" || repository.adminLinked {
		t.Fatalf("changed config was verified or bound: %+v linked=%v err=%v", record, repository.adminLinked, err)
	}
}

func TestCompleteAndFinalizeBlocksConfigurationApplyUntilLocalSideEffectsFinish(t *testing.T) {
	transactions := &memoryTransactionStore{}
	now := time.Unix(1_700_000_000, 0).UTC()
	manager, err := NewManager(context.Background(), Options{
		Source: ConfigSourceEnvironment,
		EnvironmentConfig: config.OIDCConfig{
			Enabled: true, IssuerURL: "https://identity.example.com", ClientID: "client", ClientSecret: "secret",
			PublicURL: "https://reader.example.com", CallbackURL: "https://reader.example.com/reader/api/auth/oidc/callback",
			Scopes: []string{"openid"}, SessionAbsoluteTTL: 12 * time.Hour,
		},
		Transactions: transactions, BasePath: "/reader", Now: func() time.Time { return now },
		ProviderFactory: func(cfg oidcauth.RemoteProviderConfig) (probeProvider, error) {
			return &fakeProbeProvider{issuer: cfg.IssuerURL}, nil
		},
	})
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
		}, func(State, oidcauth.AuthenticatedIdentity) error {
			close(entered)
			<-release
			return nil
		})
		completeDone <- completeErr
	}()
	<-entered
	applyDone := make(chan error, 1)
	go func() {
		_, applyErr := manager.Apply(context.Background(), UpdateRequest{})
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
	if err := <-applyDone; !errors.Is(err, ErrEnvironmentManaged) {
		t.Fatalf("Apply() error = %v, want ErrEnvironmentManaged", err)
	}
}

func cloneStoredConfig(value StoredConfig) StoredConfig {
	value.LastVerifiedAt = cloneTime(value.LastVerifiedAt)
	return value
}
