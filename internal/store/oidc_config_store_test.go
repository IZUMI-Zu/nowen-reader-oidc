package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	oidcauth "github.com/nowen-reader/nowen-reader/internal/auth/oidc"
	"github.com/nowen-reader/nowen-reader/internal/auth/oidcruntime"
	"github.com/nowen-reader/nowen-reader/internal/model"
)

func TestOIDCConfigStoreUsesRevisionCASAndAuditsWithoutSecret(t *testing.T) {
	setupOIDCStoreTest(t)
	ctx := context.Background()
	repository := OIDCConfigStore{}
	if err := CreateUser(&model.User{ID: "admin-user", Username: "admin-user", Password: "hash", Nickname: "Admin", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	initial, err := repository.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if initial.Revision != 0 || initial.Enabled {
		t.Fatalf("initial config = %+v", initial)
	}

	next := initial
	next.Enabled = true
	next.IssuerURL = "https://identity.example.com"
	next.ClientID = "nowen-reader"
	next.SecretCiphertext = "v1:key:ciphertext-must-not-be-audited"
	next.SecretKeyID = "key"
	next.ProviderName = "Company Login"
	next.Scopes = "openid profile email"
	next.PublicURL = "https://reader.example.com"
	next.AutoProvision = true
	next.SessionTTLSeconds = 28800
	next.DisablePasswordLogin = true
	next.VerifiedFingerprint = "verified-fingerprint"
	verifiedAt := time.Now().UTC().Truncate(time.Second)
	next.LastVerifiedAt = &verifiedAt
	next.UpdatedBy = "spoofed-updater"
	saved, err := repository.Save(ctx, oidcruntime.SaveRequest{
		ExpectedRevision: 0,
		Next:             next,
		Audit: oidcruntime.AuditEvent{
			ActorUserID: "admin-user", Action: "update", Result: "success",
			ChangedFields: []string{"issuerURL", "clientID", "clientSecret"}, RequestID: "request-1",
		},
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if saved.Revision != 1 || !saved.Enabled || saved.SecretCiphertext != next.SecretCiphertext || saved.SecretKeyID != next.SecretKeyID ||
		saved.IssuerURL != next.IssuerURL || saved.ClientID != next.ClientID || saved.ProviderName != next.ProviderName || saved.Scopes != next.Scopes ||
		saved.PublicURL != next.PublicURL || !saved.AutoProvision || saved.SessionTTLSeconds != next.SessionTTLSeconds || !saved.DisablePasswordLogin ||
		saved.VerifiedFingerprint != next.VerifiedFingerprint || saved.LastVerifiedAt == nil || !saved.LastVerifiedAt.Equal(verifiedAt) || saved.UpdatedBy != "admin-user" {
		t.Fatalf("saved config = %+v", saved)
	}
	if _, err := repository.Save(ctx, oidcruntime.SaveRequest{
		ExpectedRevision: 0, Next: next, Audit: oidcruntime.AuditEvent{ActorUserID: "admin-user"},
	}); !errors.Is(err, oidcruntime.ErrConfigConflict) {
		t.Fatalf("stale Save() error = %v", err)
	}

	var changedFields string
	if err := DB().QueryRow(`SELECT "changedFields" FROM "OIDCConfigAudit" WHERE "configRevision" = 1`).Scan(&changedFields); err != nil {
		t.Fatalf("load audit error = %v", err)
	}
	if changedFields != `["issuerURL","clientID","clientSecret"]` {
		t.Fatalf("changedFields = %s", changedFields)
	}
	if strings.Contains(changedFields, "ciphertext-must-not-be-audited") {
		t.Fatal("audit exposed OIDC secret ciphertext")
	}
}

func TestOIDCProviderConfigDatabaseConstraintsRejectInvalidSafetyValues(t *testing.T) {
	setupOIDCStoreTest(t)
	for _, statement := range []string{
		`UPDATE "OIDCProviderConfig" SET "sessionTTLSeconds" = 299 WHERE "id" = 1`,
		`UPDATE "OIDCProviderConfig" SET "sessionTTLSeconds" = 2592001 WHERE "id" = 1`,
		`UPDATE "OIDCProviderConfig" SET "enabled" = 2 WHERE "id" = 1`,
		`UPDATE "OIDCProviderConfig" SET "revision" = -1 WHERE "id" = 1`,
		`UPDATE "OIDCProviderConfig" SET "enabled" = 0, "disablePasswordLogin" = 1 WHERE "id" = 1`,
	} {
		if _, err := DB().Exec(statement); err == nil {
			t.Fatalf("database accepted invalid OIDC config statement: %s", statement)
		}
	}
}

func TestOIDCConfigStoreAtomicallyBindsIdentityAndMarksExactRevisionVerified(t *testing.T) {
	setupOIDCStoreTest(t)
	ctx := context.Background()
	repository := OIDCConfigStore{}
	if err := CreateUser(&model.User{ID: "admin", Username: "admin", Password: "hash", Nickname: "Admin", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	verifiedAt := time.Now().UTC().Truncate(time.Second)
	identity := oidcauth.VerifiedIdentity{Issuer: "https://identity.example.com", Subject: "admin-subject"}
	saved, err := repository.CompleteTest(ctx, 0, "fingerprint", verifiedAt, "admin", identity, oidcruntime.AuditEvent{
		ActorUserID: "spoofed-actor", Action: "verify", Result: "success", ChangedFields: []string{"verifiedFingerprint"},
	})
	if err != nil {
		t.Fatalf("CompleteTest() error = %v", err)
	}
	if saved.Revision != 1 || saved.VerifiedFingerprint != "fingerprint" || saved.LastVerifiedAt == nil || !saved.LastVerifiedAt.Equal(verifiedAt) || saved.UpdatedBy != "admin" {
		t.Fatalf("verified config = %+v", saved)
	}
	var auditActor string
	if err := DB().QueryRow(`SELECT "actorUserID" FROM "OIDCConfigAudit" WHERE "configRevision" = 1`).Scan(&auditActor); err != nil || auditActor != "admin" {
		t.Fatalf("verification audit actor = %q, %v", auditActor, err)
	}
	if linked, err := OIDCIdentityBelongsToUser(ctx, "admin", identity.Issuer, identity.Subject); err != nil || !linked {
		t.Fatalf("atomic identity binding = %v, %v", linked, err)
	}
	if _, err := repository.CompleteTest(ctx, 0, "old", verifiedAt, "admin", identity, oidcruntime.AuditEvent{}); !errors.Is(err, oidcruntime.ErrConfigConflict) {
		t.Fatalf("stale CompleteTest() error = %v", err)
	}
}

func TestOIDCConfigStoreRollsBackVerificationWhenIdentityBindingFails(t *testing.T) {
	setupOIDCStoreTest(t)
	ctx := context.Background()
	for _, user := range []*model.User{
		{ID: "admin", Username: "admin", Password: "hash", Nickname: "Admin", Role: "admin"},
		{ID: "other", Username: "other", Password: "hash", Nickname: "Other", Role: "user"},
	} {
		if err := CreateUser(user); err != nil {
			t.Fatal(err)
		}
	}
	identity := oidcauth.VerifiedIdentity{Issuer: "https://identity.example.com", Subject: "subject"}
	if err := LinkOIDCIdentity(ctx, "other", identity); err != nil {
		t.Fatal(err)
	}
	repository := OIDCConfigStore{}
	if _, err := repository.CompleteTest(ctx, 0, "fingerprint", time.Now().UTC(), "admin", identity, oidcruntime.AuditEvent{
		ActorUserID: "admin", Action: "verify", Result: "success",
	}); !errors.Is(err, ErrOIDCIdentityAlreadyLinked) {
		t.Fatalf("CompleteTest() error = %v", err)
	}
	record, err := repository.Load(ctx)
	if err != nil || record.Revision != 0 || record.VerifiedFingerprint != "" || record.LastVerifiedAt != nil {
		t.Fatalf("verification update was not rolled back: %+v, %v", record, err)
	}
	var auditCount int
	if err := DB().QueryRow(`SELECT COUNT(*) FROM "OIDCConfigAudit"`).Scan(&auditCount); err != nil || auditCount != 0 {
		t.Fatalf("failed verification wrote audit rows: count=%d err=%v", auditCount, err)
	}
}

func TestOIDCConfigStoreRejectsDemotedVerificationActorInSameTransaction(t *testing.T) {
	setupOIDCStoreTest(t)
	ctx := context.Background()
	if err := CreateUser(&model.User{ID: "admin", Username: "admin", Password: "hash", Nickname: "Admin", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	repository := OIDCConfigStore{}
	identity := oidcauth.VerifiedIdentity{Issuer: "https://identity.example.com", Subject: "admin-subject"}
	if _, err := repository.CompleteTest(ctx, 0, "fingerprint", time.Now().UTC(), "admin", identity, oidcruntime.AuditEvent{
		ActorUserID: "admin", Action: "verify", Result: "success",
	}); !errors.Is(err, oidcruntime.ErrAdministratorRequired) {
		t.Fatalf("CompleteTest() error = %v, want ErrAdministratorRequired", err)
	}
	record, err := repository.Load(ctx)
	if err != nil || record.Revision != 0 || record.VerifiedFingerprint != "" {
		t.Fatalf("demoted actor changed verification: %+v, %v", record, err)
	}
	if linked, err := OIDCIdentityBelongsToUser(ctx, "admin", identity.Issuer, identity.Subject); err != nil || linked {
		t.Fatalf("demoted actor identity binding = %v, %v", linked, err)
	}
}

func TestOIDCConfigStoreRechecksAuthorizationAndLockoutGuardsAtCommit(t *testing.T) {
	const issuer = "https://identity.example.com"
	tests := []struct {
		name   string
		mutate func(t *testing.T)
		guard  func(*oidcruntime.SaveRequest)
		want   error
	}{
		{
			name: "administrator was demoted",
			mutate: func(t *testing.T) {
				if _, err := DB().Exec(`UPDATE "User" SET "role" = 'user' WHERE "id" = 'admin'`); err != nil {
					t.Fatal(err)
				}
			},
			want: oidcruntime.ErrAdministratorRequired,
		},
		{
			name: "administrator identity was unlinked",
			mutate: func(t *testing.T) {
				if _, err := DB().Exec(`DELETE FROM "ExternalIdentity" WHERE "userId" = 'admin' AND "issuer" = ?`, issuer); err != nil {
					t.Fatal(err)
				}
			},
			guard: func(request *oidcruntime.SaveRequest) { request.RequireActorIdentityIssuer = issuer },
			want:  oidcruntime.ErrAdminIdentityRequired,
		},
		{
			name: "recovery password was removed",
			mutate: func(t *testing.T) {
				if _, err := DB().Exec(`UPDATE "User" SET "password" = '' WHERE "id" = 'admin'`); err != nil {
					t.Fatal(err)
				}
			},
			guard: func(request *oidcruntime.SaveRequest) { request.RequireActorPassword = true },
			want:  oidcruntime.ErrBreakGlassPasswordRequired,
		},
		{
			name: "OIDC-only user appeared",
			mutate: func(t *testing.T) {
				if err := CreateUser(&model.User{ID: "oidc-only", Username: "oidc-only", Nickname: "OIDC Only", Role: "user"}); err != nil {
					t.Fatal(err)
				}
				if err := LinkOIDCIdentity(context.Background(), "oidc-only", oidcauth.VerifiedIdentity{Issuer: issuer, Subject: "oidc-only-subject"}); err != nil {
					t.Fatal(err)
				}
			},
			guard: func(request *oidcruntime.SaveRequest) { request.RequireNoOIDCOnlyUsersIssuer = issuer },
			want:  oidcruntime.ErrOIDCOnlyUsersConfirmation,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupOIDCStoreTest(t)
			ctx := context.Background()
			if err := CreateUser(&model.User{ID: "admin", Username: "admin", Password: "hash", Nickname: "Admin", Role: "admin"}); err != nil {
				t.Fatal(err)
			}
			if err := LinkOIDCIdentity(ctx, "admin", oidcauth.VerifiedIdentity{Issuer: issuer, Subject: "admin-subject"}); err != nil {
				t.Fatal(err)
			}
			repository := OIDCConfigStore{}
			current, err := repository.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			next := current
			next.ProviderName = "Changed"
			request := oidcruntime.SaveRequest{
				ExpectedRevision: current.Revision,
				Next:             next,
				Audit:            oidcruntime.AuditEvent{ActorUserID: "admin", Action: "update", Result: "success"},
			}
			if tt.guard != nil {
				tt.guard(&request)
			}
			tt.mutate(t)
			if _, err := repository.Save(ctx, request); !errors.Is(err, tt.want) {
				t.Fatalf("Save() error = %v, want %v", err, tt.want)
			}
			after, err := repository.Load(ctx)
			if err != nil || after.Revision != current.Revision || after.ProviderName != current.ProviderName {
				t.Fatalf("rejected save changed config: before=%+v after=%+v err=%v", current, after, err)
			}
			var audits int
			if err := DB().QueryRow(`SELECT COUNT(*) FROM "OIDCConfigAudit"`).Scan(&audits); err != nil || audits != 0 {
				t.Fatalf("rejected save wrote success audit: count=%d err=%v", audits, err)
			}
		})
	}
}
