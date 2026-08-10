package oidcauth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	oidcauth "github.com/nowen-reader/nowen-reader/internal/auth/oidc"
)

type fakeProvider struct {
	identity              oidcauth.VerifiedIdentity
	err                   error
	beginErr              error
	state                 string
	nonce                 string
	authorizationVerifier string
	purpose               oidcauth.Purpose
	verifier              string
	exchanges             int
}

func (p *fakeProvider) Issuer() string { return "https://identity.example.com" }

func (p *fakeProvider) AuthorizationURL(_ context.Context, request oidcauth.AuthorizationRequest) (string, error) {
	p.state, p.nonce, p.authorizationVerifier, p.purpose = request.State, request.Nonce, request.CodeVerifier, request.Purpose
	if p.beginErr != nil {
		return "", p.beginErr
	}
	return "https://identity.example.com/authorize?state=" + request.State, nil
}

func (p *fakeProvider) Exchange(_ context.Context, _ string, verifier string) (oidcauth.VerifiedIdentity, error) {
	p.exchanges++
	p.verifier = verifier
	if p.err != nil {
		return oidcauth.VerifiedIdentity{}, p.err
	}
	return p.identity, nil
}

type fakeTransactions struct {
	transaction oidcauth.LoginTransaction
	createErr   error
	consumed    bool
}

func (s *fakeTransactions) Create(_ context.Context, transaction oidcauth.LoginTransaction) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.transaction = transaction
	return nil
}

func (s *fakeTransactions) Consume(_ context.Context, stateHash, bindingHash string, now time.Time) (oidcauth.LoginTransaction, error) {
	if s.consumed || stateHash != s.transaction.StateHash || bindingHash != s.transaction.BindingHash || !now.Before(s.transaction.ExpiresAt) {
		return oidcauth.LoginTransaction{}, oidcauth.ErrInvalidTransaction
	}
	s.consumed = true
	return s.transaction, nil
}

func newService(provider *fakeProvider, transactions *fakeTransactions) *oidcauth.Service {
	return oidcauth.NewService(provider, transactions, oidcauth.Options{
		BasePath:       "/reader",
		TransactionTTL: 5 * time.Minute,
	})
}

func TestBeginCreatesBoundSingleUseAuthorizationTransaction(t *testing.T) {
	provider := &fakeProvider{}
	transactions := &fakeTransactions{}
	service := newService(provider, transactions)

	result, err := service.Begin(context.Background(), oidcauth.BeginRequest{
		Purpose:  oidcauth.PurposeLogin,
		ReturnTo: "/reader/books?sort=recent",
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if !strings.HasPrefix(result.URL, "https://identity.example.com/authorize?") {
		t.Fatalf("authorization URL = %q", result.URL)
	}
	if result.BindingToken == "" || provider.state == "" || provider.nonce == "" {
		t.Fatalf("missing browser-bound authorization material: %+v", result)
	}
	if strings.Contains(transactions.transaction.StateHash, provider.state) || strings.Contains(transactions.transaction.BindingHash, result.BindingToken) {
		t.Fatal("raw state or binding token was persisted")
	}
	if transactions.transaction.ReturnTo != "/reader/books?sort=recent" || transactions.transaction.Purpose != oidcauth.PurposeLogin {
		t.Fatalf("transaction intent was not preserved: %+v", transactions.transaction)
	}

	if provider.authorizationVerifier != transactions.transaction.PKCEVerifier || len(provider.authorizationVerifier) < 43 {
		t.Fatalf("provider did not receive the generated RFC 7636 verifier")
	}
}

func TestBeginRejectsExternalOrCrossBasePathReturnTargets(t *testing.T) {
	for _, returnTo := range []string{
		"https://evil.example/steal",
		"//evil.example/steal",
		"/other/path",
		"/reader\\evil",
		"/reader/path#fragment",
		"/reader/path%0aheader",
	} {
		t.Run(returnTo, func(t *testing.T) {
			service := newService(&fakeProvider{}, &fakeTransactions{})
			_, err := service.Begin(context.Background(), oidcauth.BeginRequest{Purpose: oidcauth.PurposeLogin, ReturnTo: returnTo})
			if !errors.Is(err, oidcauth.ErrInvalidReturnTo) {
				t.Fatalf("Begin(%q) error = %v, want ErrInvalidReturnTo", returnTo, err)
			}
		})
	}
}

func TestBeginRequiresSessionUserOnlyForAccountBoundPurposes(t *testing.T) {
	service := newService(&fakeProvider{}, &fakeTransactions{})
	for _, request := range []oidcauth.BeginRequest{
		{Purpose: oidcauth.PurposeLogin, SessionUserID: "unexpected", ReturnTo: "/reader/"},
		{Purpose: oidcauth.PurposeLink, ReturnTo: "/reader/"},
		{Purpose: oidcauth.PurposeReauth, ReturnTo: "/reader/"},
		{Purpose: oidcauth.PurposeConfigTest, ReturnTo: "/reader/"},
	} {
		if _, err := service.Begin(context.Background(), request); !errors.Is(err, oidcauth.ErrInvalidTransaction) {
			t.Fatalf("Begin(%+v) error = %v, want ErrInvalidTransaction", request, err)
		}
	}
}

func TestInteractivePurposesRequireFreshProviderAuthenticationTime(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	for _, purpose := range []oidcauth.Purpose{oidcauth.PurposeReauth, oidcauth.PurposeConfigTest} {
		t.Run(string(purpose), func(t *testing.T) {
			for _, tt := range []struct {
				name     string
				authTime time.Time
				wantErr  bool
			}{
				{name: "fresh", authTime: now, wantErr: false},
				{name: "missing", wantErr: true},
				{name: "stale SSO", authTime: now.Add(-time.Hour), wantErr: true},
				{name: "future", authTime: now.Add(5 * time.Minute), wantErr: true},
			} {
				t.Run(tt.name, func(t *testing.T) {
					provider := &fakeProvider{}
					transactions := &fakeTransactions{}
					service := oidcauth.NewService(provider, transactions, oidcauth.Options{
						BasePath: "/reader", TransactionTTL: 5 * time.Minute, Now: func() time.Time { return now },
					})
					begin, err := service.Begin(context.Background(), oidcauth.BeginRequest{
						Purpose: purpose, SessionUserID: "user-1", ReturnTo: "/reader/settings",
					})
					if err != nil {
						t.Fatalf("Begin() error = %v", err)
					}
					if provider.purpose != purpose {
						t.Fatalf("provider purpose = %q", provider.purpose)
					}
					provider.identity = oidcauth.VerifiedIdentity{
						Issuer: provider.Issuer(), Subject: "subject", Nonce: provider.nonce, AuthTime: tt.authTime,
					}
					_, err = service.Complete(context.Background(), oidcauth.CallbackRequest{
						State: provider.state, Code: "code", BindingToken: begin.BindingToken,
					})
					if tt.wantErr && !errors.Is(err, oidcauth.ErrInvalidIdentity) {
						t.Fatalf("Complete() error = %v, want ErrInvalidIdentity", err)
					}
					if !tt.wantErr && err != nil {
						t.Fatalf("Complete() error = %v", err)
					}
				})
			}
		})
	}
}

func TestCompleteVerifiesBindingNonceAndConsumesTransactionOnce(t *testing.T) {
	provider := &fakeProvider{}
	transactions := &fakeTransactions{}
	service := newService(provider, transactions)
	begin, err := service.Begin(context.Background(), oidcauth.BeginRequest{Purpose: oidcauth.PurposeLogin, ReturnTo: "/reader/"})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	provider.identity = oidcauth.VerifiedIdentity{
		Issuer:            provider.Issuer(),
		Subject:           "subject-123",
		Nonce:             provider.nonce,
		PreferredUsername: "alice",
		DisplayName:       "Alice",
		Email:             "alice@example.com",
		EmailVerified:     true,
	}

	if _, err := service.Complete(context.Background(), oidcauth.CallbackRequest{
		State: provider.state, Code: "code", BindingToken: "wrong",
	}); !errors.Is(err, oidcauth.ErrInvalidTransaction) {
		t.Fatalf("wrong binding error = %v, want ErrInvalidTransaction", err)
	}
	if provider.exchanges != 0 {
		t.Fatal("authorization code was exchanged before browser binding validation")
	}

	identity, err := service.Complete(context.Background(), oidcauth.CallbackRequest{
		State: provider.state, Code: "code", BindingToken: begin.BindingToken,
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if identity.Subject != "subject-123" || identity.ReturnTo != "/reader/" || provider.verifier == "" {
		t.Fatalf("unexpected completed identity: %+v", identity)
	}

	if _, err := service.Complete(context.Background(), oidcauth.CallbackRequest{
		State: provider.state, Code: "code", BindingToken: begin.BindingToken,
	}); !errors.Is(err, oidcauth.ErrInvalidTransaction) {
		t.Fatalf("replay error = %v, want ErrInvalidTransaction", err)
	}
}

func TestCancelReturnsLocalTargetAndConsumesTransactionWithoutCodeExchange(t *testing.T) {
	provider := &fakeProvider{}
	transactions := &fakeTransactions{}
	service := newService(provider, transactions)
	begin, err := service.Begin(context.Background(), oidcauth.BeginRequest{
		Purpose: oidcauth.PurposeLogin, ReturnTo: "/reader/settings?tab=account",
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	returnTo, err := service.Cancel(context.Background(), oidcauth.CancelRequest{
		State: provider.state, BindingToken: begin.BindingToken,
	})
	if err != nil || returnTo != "/reader/settings?tab=account" {
		t.Fatalf("Cancel() = %q, %v", returnTo, err)
	}
	if provider.exchanges != 0 || !transactions.consumed {
		t.Fatalf("cancel exchanged code or did not consume transaction: exchanges=%d consumed=%v", provider.exchanges, transactions.consumed)
	}
	if _, err := service.Cancel(context.Background(), oidcauth.CancelRequest{
		State: provider.state, BindingToken: begin.BindingToken,
	}); !errors.Is(err, oidcauth.ErrInvalidTransaction) {
		t.Fatalf("Cancel replay error = %v", err)
	}
}

func TestCancelRejectsTransactionFromChangedConfiguration(t *testing.T) {
	provider := &fakeProvider{}
	transactions := &fakeTransactions{}
	original := oidcauth.NewService(provider, transactions, oidcauth.Options{
		BasePath: "/reader", TransactionTTL: 5 * time.Minute, ConfigFingerprint: "original",
	})
	begin, err := original.Begin(context.Background(), oidcauth.BeginRequest{
		Purpose: oidcauth.PurposeLogin, ReturnTo: "/reader/settings?tab=account",
	})
	if err != nil {
		t.Fatal(err)
	}
	replacement := oidcauth.NewService(provider, transactions, oidcauth.Options{
		BasePath: "/reader", TransactionTTL: 5 * time.Minute, ConfigFingerprint: "replacement",
	})
	returnTo, err := replacement.Cancel(context.Background(), oidcauth.CancelRequest{
		State: provider.state, BindingToken: begin.BindingToken,
	})
	if !errors.Is(err, oidcauth.ErrConfigurationChanged) || returnTo != "/reader/settings?tab=account" {
		t.Fatalf("Cancel() = %q, %v, want safe target and ErrConfigurationChanged", returnTo, err)
	}
	if provider.exchanges != 0 || !transactions.consumed {
		t.Fatalf("stale cancellation exchanged code or stayed replayable: exchanges=%d consumed=%v", provider.exchanges, transactions.consumed)
	}
}

func TestCompletePreservesSafeReturnTargetWhenProviderIsUnavailable(t *testing.T) {
	provider := &fakeProvider{err: oidcauth.ErrProviderUnavailable}
	transactions := &fakeTransactions{}
	service := newService(provider, transactions)
	begin, err := service.Begin(context.Background(), oidcauth.BeginRequest{
		Purpose: oidcauth.PurposeLogin, ReturnTo: "/reader/settings?tab=account",
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	identity, err := service.Complete(context.Background(), oidcauth.CallbackRequest{
		State: provider.state, Code: "code", BindingToken: begin.BindingToken,
	})
	if !errors.Is(err, oidcauth.ErrProviderUnavailable) {
		t.Fatalf("Complete() error = %v, want provider unavailable", err)
	}
	if identity.ReturnTo != "/reader/settings?tab=account" || identity.Purpose != oidcauth.PurposeLogin {
		t.Fatalf("Complete() lost validated transaction metadata: %+v", identity)
	}
}

func TestCompleteRejectsOnlyProtocolConfigurationChanges(t *testing.T) {
	for _, tt := range []struct {
		name                string
		beginRevision       int64
		completeRevision    int64
		beginFingerprint    string
		completeFingerprint string
		wantChanged         bool
	}{
		{name: "protocol fingerprint changed", beginRevision: 1, completeRevision: 2, beginFingerprint: "old", completeFingerprint: "new", wantChanged: true},
		{name: "policy-only revision changed", beginRevision: 1, completeRevision: 2, beginFingerprint: "same", completeFingerprint: "same", wantChanged: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := &fakeProvider{}
			transactions := &fakeTransactions{}
			beginService := oidcauth.NewService(provider, transactions, oidcauth.Options{
				BasePath: "/reader", ConfigRevision: tt.beginRevision, ConfigFingerprint: tt.beginFingerprint,
			})
			begin, err := beginService.Begin(context.Background(), oidcauth.BeginRequest{Purpose: oidcauth.PurposeLogin, ReturnTo: "/reader/"})
			if err != nil {
				t.Fatalf("Begin() error = %v", err)
			}
			provider.identity = oidcauth.VerifiedIdentity{Issuer: provider.Issuer(), Subject: "subject", Nonce: provider.nonce}
			completeService := oidcauth.NewService(provider, transactions, oidcauth.Options{
				BasePath: "/reader", ConfigRevision: tt.completeRevision, ConfigFingerprint: tt.completeFingerprint,
			})
			identity, err := completeService.Complete(context.Background(), oidcauth.CallbackRequest{
				State: provider.state, Code: "code", BindingToken: begin.BindingToken,
			})
			if tt.wantChanged {
				if !errors.Is(err, oidcauth.ErrConfigurationChanged) || provider.exchanges != 0 || identity.ReturnTo != "/reader/" {
					t.Fatalf("Complete() = %+v, %v, exchanges=%d", identity, err, provider.exchanges)
				}
				return
			}
			if err != nil || provider.exchanges != 1 {
				t.Fatalf("policy-only Complete() = %+v, %v, exchanges=%d", identity, err, provider.exchanges)
			}
		})
	}
}

func TestCompleteRejectsNonceOrIssuerMismatchAfterConsumingTransaction(t *testing.T) {
	tests := []struct {
		name     string
		identity func(*fakeProvider) oidcauth.VerifiedIdentity
	}{
		{
			name: "nonce mismatch",
			identity: func(p *fakeProvider) oidcauth.VerifiedIdentity {
				return oidcauth.VerifiedIdentity{Issuer: p.Issuer(), Subject: "subject", Nonce: "wrong"}
			},
		},
		{
			name: "issuer mismatch",
			identity: func(p *fakeProvider) oidcauth.VerifiedIdentity {
				return oidcauth.VerifiedIdentity{Issuer: "https://attacker.example", Subject: "subject", Nonce: p.nonce}
			},
		},
		{
			name: "missing subject",
			identity: func(p *fakeProvider) oidcauth.VerifiedIdentity {
				return oidcauth.VerifiedIdentity{Issuer: p.Issuer(), Nonce: p.nonce}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &fakeProvider{}
			transactions := &fakeTransactions{}
			service := newService(provider, transactions)
			begin, err := service.Begin(context.Background(), oidcauth.BeginRequest{Purpose: oidcauth.PurposeLogin, ReturnTo: "/reader/"})
			if err != nil {
				t.Fatalf("Begin() error = %v", err)
			}
			provider.identity = tt.identity(provider)

			_, err = service.Complete(context.Background(), oidcauth.CallbackRequest{
				State: provider.state, Code: "code", BindingToken: begin.BindingToken,
			})
			if !errors.Is(err, oidcauth.ErrInvalidIdentity) {
				t.Fatalf("Complete() error = %v, want ErrInvalidIdentity", err)
			}
			if !transactions.consumed {
				t.Fatal("invalid token attempt did not consume its transaction")
			}
		})
	}
}
