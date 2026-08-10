package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	oidcauth "github.com/nowen-reader/nowen-reader/internal/auth/oidc"
	"github.com/nowen-reader/nowen-reader/internal/auth/oidcruntime"
)

type OIDCConfigStore struct{}

func (OIDCConfigStore) RecordAudit(ctx context.Context, revision int64, audit oidcruntime.AuditEvent) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertOIDCConfigAudit(ctx, tx, revision, audit); err != nil {
		return err
	}
	return tx.Commit()
}

func (OIDCConfigStore) Load(ctx context.Context) (oidcruntime.StoredConfig, error) {
	return loadOIDCConfigRow(ctx, db)
}

func (OIDCConfigStore) Save(
	ctx context.Context,
	expectedRevision int64,
	next oidcruntime.StoredConfig,
	audit oidcruntime.AuditEvent,
) (oidcruntime.StoredConfig, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return oidcruntime.StoredConfig{}, err
	}
	defer tx.Rollback()

	updatedAt := next.UpdatedAt.UTC()
	if next.UpdatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	result, err := tx.ExecContext(ctx, `UPDATE "OIDCProviderConfig" SET
		"enabled" = ?, "issuerURL" = ?, "clientID" = ?, "secretCiphertext" = ?, "secretKeyID" = ?,
		"providerName" = ?, "scopes" = ?, "publicURL" = ?, "autoProvision" = ?, "sessionTTLSeconds" = ?,
		"disablePasswordLogin" = ?, "verifiedFingerprint" = ?, "lastVerifiedAt" = ?,
		"updatedBy" = ?, "updatedAt" = ?, "revision" = "revision" + 1
		WHERE "id" = 1 AND "revision" = ?`,
		next.Enabled, next.IssuerURL, next.ClientID, next.SecretCiphertext, next.SecretKeyID,
		next.ProviderName, next.Scopes, next.PublicURL, next.AutoProvision, next.SessionTTLSeconds,
		next.DisablePasswordLogin, next.VerifiedFingerprint, nullableTime(next.LastVerifiedAt),
		next.UpdatedBy, updatedAt, expectedRevision,
	)
	if err != nil {
		return oidcruntime.StoredConfig{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return oidcruntime.StoredConfig{}, err
	}
	if rows != 1 {
		return oidcruntime.StoredConfig{}, oidcruntime.ErrConfigConflict
	}
	saved, err := loadOIDCConfigRow(ctx, tx)
	if err != nil {
		return oidcruntime.StoredConfig{}, err
	}
	if err := insertOIDCConfigAudit(ctx, tx, saved.Revision, audit); err != nil {
		return oidcruntime.StoredConfig{}, err
	}
	if err := tx.Commit(); err != nil {
		return oidcruntime.StoredConfig{}, err
	}
	return saved, nil
}

func (OIDCConfigStore) UserHasIdentityForIssuer(ctx context.Context, userID, issuer string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM "ExternalIdentity" identity
		JOIN "User" user ON user."id" = identity."userId"
		WHERE identity."userId" = ? AND identity."issuer" = ? AND user."role" = 'admin'`, userID, issuer).Scan(&count)
	return count > 0, err
}

func (OIDCConfigStore) UserHasPassword(ctx context.Context, userID string) (bool, error) {
	var password string
	err := db.QueryRowContext(ctx, `SELECT "password" FROM "User" WHERE "id" = ?`, userID).Scan(&password)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return password != "", err
}

func (OIDCConfigStore) OIDCOnlyUserCount(ctx context.Context, issuer string) (int64, error) {
	var count int64
	err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT user."id")
		FROM "User" user
		JOIN "ExternalIdentity" identity ON identity."userId" = user."id"
		WHERE identity."issuer" = ? AND user."password" = ''`, issuer).Scan(&count)
	return count, err
}

func (OIDCConfigStore) CompleteTest(
	ctx context.Context,
	expectedRevision int64,
	fingerprint string,
	verifiedAt time.Time,
	actorUserID string,
	identity oidcauth.VerifiedIdentity,
	audit oidcruntime.AuditEvent,
) (oidcruntime.StoredConfig, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return oidcruntime.StoredConfig{}, err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `UPDATE "OIDCProviderConfig"
		SET "verifiedFingerprint" = ?, "lastVerifiedAt" = ?, "revision" = "revision" + 1,
			"updatedBy" = ?, "updatedAt" = ?
		WHERE "id" = 1 AND "revision" = ?
			AND EXISTS (SELECT 1 FROM "User" WHERE "id" = ? AND "role" = 'admin')`,
		fingerprint, verifiedAt.UTC(), audit.ActorUserID, verifiedAt.UTC(), expectedRevision, actorUserID,
	)
	if err != nil {
		return oidcruntime.StoredConfig{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return oidcruntime.StoredConfig{}, err
	}
	if rows != 1 {
		var administratorExists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM "User" WHERE "id" = ? AND "role" = 'admin'`, actorUserID).Scan(&administratorExists); err != nil {
			return oidcruntime.StoredConfig{}, err
		}
		if administratorExists != 1 {
			return oidcruntime.StoredConfig{}, oidcruntime.ErrAdminIdentityRequired
		}
		return oidcruntime.StoredConfig{}, oidcruntime.ErrConfigConflict
	}
	if err := linkOIDCIdentityTx(ctx, tx, actorUserID, identity, verifiedAt.UTC()); err != nil {
		return oidcruntime.StoredConfig{}, err
	}

	saved, err := loadOIDCConfigRow(ctx, tx)
	if err != nil {
		return oidcruntime.StoredConfig{}, err
	}
	if err := insertOIDCConfigAudit(ctx, tx, saved.Revision, audit); err != nil {
		return oidcruntime.StoredConfig{}, err
	}
	if err := tx.Commit(); err != nil {
		return oidcruntime.StoredConfig{}, err
	}
	return saved, nil
}

type oidcConfigQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadOIDCConfigRow(ctx context.Context, queryer oidcConfigQueryer) (oidcruntime.StoredConfig, error) {
	var result oidcruntime.StoredConfig
	var lastVerified sql.NullTime
	err := queryer.QueryRowContext(ctx, `SELECT "enabled", "issuerURL", "clientID", "secretCiphertext", "secretKeyID",
		"providerName", "scopes", "publicURL", "autoProvision", "sessionTTLSeconds", "disablePasswordLogin",
		"revision", "verifiedFingerprint", "lastVerifiedAt", "updatedBy", "updatedAt"
		FROM "OIDCProviderConfig" WHERE "id" = 1`).Scan(
		&result.Enabled, &result.IssuerURL, &result.ClientID, &result.SecretCiphertext, &result.SecretKeyID,
		&result.ProviderName, &result.Scopes, &result.PublicURL, &result.AutoProvision, &result.SessionTTLSeconds,
		&result.DisablePasswordLogin, &result.Revision, &result.VerifiedFingerprint, &lastVerified,
		&result.UpdatedBy, &result.UpdatedAt,
	)
	if err != nil {
		return oidcruntime.StoredConfig{}, err
	}
	if lastVerified.Valid {
		value := lastVerified.Time.UTC()
		result.LastVerifiedAt = &value
	}
	return result, nil
}

func insertOIDCConfigAudit(ctx context.Context, tx *sql.Tx, revision int64, audit oidcruntime.AuditEvent) error {
	changedFields, err := json.Marshal(audit.ChangedFields)
	if err != nil {
		return fmt.Errorf("encode OIDC audit changed fields: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO "OIDCConfigAudit"
		("id", "actorUserID", "action", "result", "changedFields", "configRevision", "requestID", "createdAt")
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), audit.ActorUserID, audit.Action, audit.Result, string(changedFields), revision, audit.RequestID, time.Now().UTC(),
	)
	return err
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}
