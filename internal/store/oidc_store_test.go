package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

	oidcauth "github.com/nowen-reader/nowen-reader/internal/auth/oidc"
	"github.com/nowen-reader/nowen-reader/internal/model"
)

func setupOIDCStoreTest(t *testing.T) {
	t.Helper()
	if err := InitDB(filepath.Join(t.TempDir(), "oidc.db")); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	t.Cleanup(CloseDB)
}

func TestOIDCTransactionStoreConsumesBrowserBoundStateOnce(t *testing.T) {
	setupOIDCStoreTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	repository := OIDCTransactionStore{}
	transaction := oidcauth.LoginTransaction{
		StateHash: "state-hash", BindingHash: "binding-hash", Nonce: "nonce",
		PKCEVerifier: "verifier", Purpose: oidcauth.PurposeLogin, ReturnTo: "/books",
		CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}
	if err := repository.Create(context.Background(), transaction); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if _, err := repository.Consume(context.Background(), "state-hash", "wrong", now); !errors.Is(err, oidcauth.ErrInvalidTransaction) {
		t.Fatalf("wrong binding error = %v", err)
	}
	consumed, err := repository.Consume(context.Background(), "state-hash", "binding-hash", now)
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if consumed.Nonce != "nonce" || consumed.PKCEVerifier != "verifier" || consumed.ReturnTo != "/books" {
		t.Fatalf("consumed transaction = %+v", consumed)
	}
	if _, err := repository.Consume(context.Background(), "state-hash", "binding-hash", now); !errors.Is(err, oidcauth.ErrInvalidTransaction) {
		t.Fatalf("replay error = %v", err)
	}
}

func TestOIDCTransactionStoreRejectsExpiredStateAndCleansOldRows(t *testing.T) {
	setupOIDCStoreTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	repository := OIDCTransactionStore{}
	transaction := oidcauth.LoginTransaction{
		StateHash: "expired", BindingHash: "binding", Nonce: "nonce", PKCEVerifier: "verifier",
		Purpose: oidcauth.PurposeLogin, ReturnTo: "/", CreatedAt: now.Add(-10 * time.Minute), ExpiresAt: now.Add(-time.Minute),
	}
	if err := repository.Create(context.Background(), transaction); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := repository.Consume(context.Background(), "expired", "binding", now); !errors.Is(err, oidcauth.ErrInvalidTransaction) {
		t.Fatalf("expired transaction error = %v", err)
	}
	deleted, err := DeleteExpiredOIDCLoginTransactions(now)
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteExpiredOIDCLoginTransactions() = %d, %v; want 1, nil", deleted, err)
	}
}

func TestResolveOIDCLoginDefaultsToDenyAndNeverLinksByUsernameOrEmail(t *testing.T) {
	setupOIDCStoreTest(t)
	local := &model.User{ID: "local-user", Username: "alice", Password: "password-hash", Nickname: "Local Alice", Role: "admin", AiEnabled: true}
	if err := CreateUser(local); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	identity := oidcauth.VerifiedIdentity{
		Issuer: "https://identity.example.com", Subject: "external-alice", PreferredUsername: "alice",
		DisplayName: "External Alice", Email: "alice@example.com", EmailVerified: true,
	}

	if _, _, err := ResolveOIDCLogin(context.Background(), identity, OIDCProvisionPolicy{}); !errors.Is(err, ErrOIDCIdentityNotLinked) {
		t.Fatalf("disabled provisioning error = %v, want ErrOIDCIdentityNotLinked", err)
	}
	user, created, err := ResolveOIDCLogin(context.Background(), identity, OIDCProvisionPolicy{AutoProvision: true})
	if err != nil {
		t.Fatalf("ResolveOIDCLogin() error = %v", err)
	}
	if !created || user.ID == local.ID || user.Username == local.Username {
		t.Fatalf("external identity was silently linked or not provisioned separately: %+v", user)
	}
	if user.Password != "" || user.Role != "user" {
		t.Fatalf("OIDC-only user has unsafe local credentials/role: %+v", user)
	}

	again, createdAgain, err := ResolveOIDCLogin(context.Background(), identity, OIDCProvisionPolicy{})
	if err != nil || createdAgain || again.ID != user.ID {
		t.Fatalf("existing identity resolution = %+v, %v, %v", again, createdAgain, err)
	}
}

func TestResolveOIDCLoginRequiresExplicitSubjectForFirstAdmin(t *testing.T) {
	setupOIDCStoreTest(t)
	identity := oidcauth.VerifiedIdentity{Issuer: "https://identity.example.com", Subject: "bootstrap-subject", PreferredUsername: "owner"}

	if _, _, err := ResolveOIDCLogin(context.Background(), identity, OIDCProvisionPolicy{AutoProvision: true}); !errors.Is(err, ErrOIDCBootstrapDenied) {
		t.Fatalf("unapproved first login error = %v, want ErrOIDCBootstrapDenied", err)
	}
	user, created, err := ResolveOIDCLogin(context.Background(), identity, OIDCProvisionPolicy{
		AutoProvision: true, BootstrapAdmin: true,
	})
	if err != nil || !created {
		t.Fatalf("approved bootstrap = %+v, %v, %v", user, created, err)
	}
	if user.Role != "admin" || !user.AiEnabled {
		t.Fatalf("bootstrap user is not an administrator: %+v", user)
	}
}

func TestResolveOIDCLoginTruncatesUnicodeUsernameOnRuneBoundaries(t *testing.T) {
	setupOIDCStoreTest(t)
	identity := oidcauth.VerifiedIdentity{
		Issuer: "https://identity.example.com", Subject: "unicode-subject",
		PreferredUsername: "阅读阅读阅读阅读阅读阅读阅读阅读阅读阅读阅读阅读阅读阅读阅读阅读阅读阅读",
	}
	user, created, err := ResolveOIDCLogin(context.Background(), identity, OIDCProvisionPolicy{
		AutoProvision: true, BootstrapAdmin: true,
	})
	if err != nil || !created {
		t.Fatalf("ResolveOIDCLogin() = %+v, %v, %v", user, created, err)
	}
	if !utf8.ValidString(user.Username) || len([]rune(user.Username)) > 32 {
		t.Fatalf("invalid truncated username %q", user.Username)
	}
}

func TestLinkOIDCIdentityRequiresAuthenticatedLocalUserAndRejectsCrossAccountClaim(t *testing.T) {
	setupOIDCStoreTest(t)
	for _, user := range []*model.User{
		{ID: "first", Username: "first", Password: "hash", Nickname: "First", Role: "admin"},
		{ID: "second", Username: "second", Password: "hash", Nickname: "Second", Role: "user"},
	} {
		if err := CreateUser(user); err != nil {
			t.Fatalf("CreateUser(%s) error = %v", user.ID, err)
		}
	}
	identity := oidcauth.VerifiedIdentity{Issuer: "https://identity.example.com", Subject: "subject", Email: "a@example.com"}
	if err := LinkOIDCIdentity(context.Background(), "first", identity); err != nil {
		t.Fatalf("LinkOIDCIdentity() error = %v", err)
	}
	if ok, err := OIDCIdentityBelongsToUser(context.Background(), "first", identity.Issuer, identity.Subject); err != nil || !ok {
		t.Fatalf("OIDCIdentityBelongsToUser(first) = %v, %v", ok, err)
	}
	if err := LinkOIDCIdentity(context.Background(), "second", identity); !errors.Is(err, ErrOIDCIdentityAlreadyLinked) {
		t.Fatalf("cross-account LinkOIDCIdentity() error = %v", err)
	}
}

func TestUnlinkOIDCIdentityRefusesToRemoveLastLoginMethod(t *testing.T) {
	setupOIDCStoreTest(t)
	user := &model.User{ID: "oidc-only", Username: "reader", Password: "", Nickname: "Reader", Role: "user"}
	if err := CreateUser(user); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	identity := oidcauth.VerifiedIdentity{Issuer: "https://identity.example.com", Subject: "subject"}
	if err := LinkOIDCIdentity(context.Background(), user.ID, identity); err != nil {
		t.Fatalf("LinkOIDCIdentity() error = %v", err)
	}
	if err := UnlinkOIDCIdentity(context.Background(), user.ID, identity.Issuer, true); !errors.Is(err, ErrOIDCWouldLockOut) {
		t.Fatalf("UnlinkOIDCIdentity() error = %v, want ErrOIDCWouldLockOut", err)
	}
	if err := UpdateUserPassword(user.ID, "new-password-hash"); err != nil {
		t.Fatalf("UpdateUserPassword() error = %v", err)
	}
	legacyIdentity := oidcauth.VerifiedIdentity{Issuer: "https://old-identity.example.com", Subject: "old-subject"}
	if err := LinkOIDCIdentity(context.Background(), user.ID, legacyIdentity); err != nil {
		t.Fatalf("LinkOIDCIdentity(legacy) error = %v", err)
	}
	if err := UnlinkOIDCIdentity(context.Background(), user.ID, identity.Issuer, false); !errors.Is(err, ErrOIDCWouldLockOut) {
		t.Fatalf("UnlinkOIDCIdentity() with disabled password login error = %v, want ErrOIDCWouldLockOut", err)
	}
	if err := UnlinkOIDCIdentity(context.Background(), user.ID, identity.Issuer, true); err != nil {
		t.Fatalf("UnlinkOIDCIdentity() with password error = %v", err)
	}
}
