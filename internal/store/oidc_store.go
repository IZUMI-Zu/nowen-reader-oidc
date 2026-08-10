package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	oidcauth "github.com/nowen-reader/nowen-reader/internal/auth/oidc"
	"github.com/nowen-reader/nowen-reader/internal/model"
)

var (
	ErrOIDCIdentityNotLinked     = errors.New("OIDC identity is not linked to a local user")
	ErrOIDCBootstrapDenied       = errors.New("OIDC subject is not allowed to bootstrap the first administrator")
	ErrOIDCIdentityAlreadyLinked = errors.New("OIDC identity is already linked to another user")
	ErrOIDCProviderAlreadyLinked = errors.New("user already has a different identity from this OIDC provider")
	ErrOIDCWouldLockOut          = errors.New("cannot remove the user's last login method")
)

// OIDCTransactionStore implements oidcauth.TransactionStore with SQLite.
type OIDCTransactionStore struct{}

func (OIDCTransactionStore) Create(ctx context.Context, transaction oidcauth.LoginTransaction) error {
	_, err := db.ExecContext(ctx, `INSERT INTO "OIDCLoginTransaction"
		("stateHash", "bindingHash", "nonce", "pkceVerifier", "purpose", "sessionUserId", "returnTo", "expiresAt", "createdAt", "configRevision", "configFingerprint")
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		transaction.StateHash, transaction.BindingHash, transaction.Nonce, transaction.PKCEVerifier,
		string(transaction.Purpose), transaction.SessionUserID, transaction.ReturnTo, transaction.ExpiresAt, transaction.CreatedAt,
		transaction.ConfigRevision, transaction.ConfigFingerprint,
	)
	return err
}

// LinkOIDCIdentity explicitly associates a verified external identity with a
// caller-selected local user. It never resolves a target by email or username.
func LinkOIDCIdentity(ctx context.Context, userID string, identity oidcauth.VerifiedIdentity) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := linkOIDCIdentityTx(ctx, tx, userID, identity, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func linkOIDCIdentityTx(ctx context.Context, tx *sql.Tx, userID string, identity oidcauth.VerifiedIdentity, now time.Time) error {
	if userID == "" || identity.Issuer == "" || identity.Subject == "" {
		return oidcauth.ErrInvalidIdentity
	}
	var userExists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM "User" WHERE "id" = ?`, userID).Scan(&userExists); err != nil {
		return err
	}
	if userExists != 1 {
		return sql.ErrNoRows
	}

	var linkedUserID string
	err := tx.QueryRowContext(ctx, `SELECT "userId" FROM "ExternalIdentity" WHERE "issuer" = ? AND "subject" = ?`, identity.Issuer, identity.Subject).Scan(&linkedUserID)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		if linkedUserID != userID {
			return ErrOIDCIdentityAlreadyLinked
		}
		if _, err := tx.ExecContext(ctx, `UPDATE "ExternalIdentity"
			SET "email" = ?, "emailVerified" = ?, "displayName" = ?, "lastLoginAt" = ?
			WHERE "issuer" = ? AND "subject" = ?`,
			identity.Email, identity.EmailVerified, identity.DisplayName, now, identity.Issuer, identity.Subject,
		); err != nil {
			return err
		}
		return nil
	}

	var providerIdentityCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM "ExternalIdentity" WHERE "userId" = ? AND "issuer" = ?`, userID, identity.Issuer).Scan(&providerIdentityCount); err != nil {
		return err
	}
	if providerIdentityCount != 0 {
		return ErrOIDCProviderAlreadyLinked
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO "ExternalIdentity"
		("id", "userId", "issuer", "subject", "email", "emailVerified", "displayName", "createdAt", "lastLoginAt")
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), userID, identity.Issuer, identity.Subject, identity.Email,
		identity.EmailVerified, identity.DisplayName, now, now,
	); err != nil {
		return err
	}
	return nil
}

func OIDCIdentityBelongsToUser(ctx context.Context, userID, issuer, subject string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "ExternalIdentity"
		WHERE "userId" = ? AND "issuer" = ? AND "subject" = ?`, userID, issuer, subject).Scan(&count)
	return count == 1, err
}

func HasOIDCIdentityForUser(ctx context.Context, userID, issuer string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "ExternalIdentity" WHERE "userId" = ? AND "issuer" = ?`, userID, issuer).Scan(&count)
	return count == 1, err
}

func UnlinkOIDCIdentity(ctx context.Context, userID, issuer string, passwordLoginEnabled bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var password string
	if err := tx.QueryRowContext(ctx, `SELECT "password" FROM "User" WHERE "id" = ?`, userID).Scan(&password); err != nil {
		return err
	}
	// The application currently has one configured OIDC provider. Identities
	// left by an older issuer are not proof that the user can still sign in, so
	// unlinking requires a currently usable local password login path.
	if !passwordLoginEnabled || password == "" {
		return ErrOIDCWouldLockOut
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM "ExternalIdentity" WHERE "userId" = ? AND "issuer" = ?`, userID, issuer)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (OIDCTransactionStore) Consume(ctx context.Context, stateHash, bindingHash string, now time.Time) (oidcauth.LoginTransaction, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return oidcauth.LoginTransaction{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE "OIDCLoginTransaction"
		SET "consumedAt" = ?
		WHERE "stateHash" = ? AND "bindingHash" = ? AND "consumedAt" IS NULL AND "expiresAt" > ?`,
		now, stateHash, bindingHash, now,
	)
	if err != nil {
		return oidcauth.LoginTransaction{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return oidcauth.LoginTransaction{}, err
	}
	if rows != 1 {
		return oidcauth.LoginTransaction{}, oidcauth.ErrInvalidTransaction
	}

	var transaction oidcauth.LoginTransaction
	var purpose string
	err = tx.QueryRowContext(ctx, `SELECT "stateHash", "bindingHash", "nonce", "pkceVerifier", "purpose",
		"sessionUserId", "returnTo", "expiresAt", "createdAt", "configRevision", "configFingerprint"
		FROM "OIDCLoginTransaction" WHERE "stateHash" = ?`, stateHash).Scan(
		&transaction.StateHash, &transaction.BindingHash, &transaction.Nonce, &transaction.PKCEVerifier, &purpose,
		&transaction.SessionUserID, &transaction.ReturnTo, &transaction.ExpiresAt, &transaction.CreatedAt,
		&transaction.ConfigRevision, &transaction.ConfigFingerprint,
	)
	if err != nil {
		return oidcauth.LoginTransaction{}, err
	}
	transaction.Purpose = oidcauth.Purpose(purpose)
	if err := tx.Commit(); err != nil {
		return oidcauth.LoginTransaction{}, err
	}
	return transaction, nil
}

func DeleteExpiredOIDCLoginTransactions(now time.Time) (int64, error) {
	result, err := db.Exec(`DELETE FROM "OIDCLoginTransaction" WHERE "expiresAt" <= ? OR "consumedAt" IS NOT NULL`, now)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type OIDCProvisionPolicy struct {
	AutoProvision  bool
	BootstrapAdmin bool
}

// ResolveOIDCLogin resolves only by the verified (issuer, subject) pair. When
// provisioning is enabled, username/email claims are profile candidates and
// are never used to attach the identity to an existing account.
func ResolveOIDCLogin(ctx context.Context, identity oidcauth.VerifiedIdentity, policy OIDCProvisionPolicy) (*model.User, bool, error) {
	if identity.Issuer == "" || identity.Subject == "" {
		return nil, false, oidcauth.ErrInvalidIdentity
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	user, err := getOIDCUserTx(ctx, tx, identity.Issuer, identity.Subject)
	if err != nil && err != sql.ErrNoRows {
		return nil, false, err
	}
	if user != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE "ExternalIdentity"
			SET "email" = ?, "emailVerified" = ?, "displayName" = ?, "lastLoginAt" = ?
			WHERE "issuer" = ? AND "subject" = ?`,
			identity.Email, identity.EmailVerified, identity.DisplayName, time.Now().UTC(), identity.Issuer, identity.Subject,
		); err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return user, false, nil
	}
	if !policy.AutoProvision {
		return nil, false, ErrOIDCIdentityNotLinked
	}

	var userCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM "User"`).Scan(&userCount); err != nil {
		return nil, false, err
	}
	if userCount == 0 && !policy.BootstrapAdmin {
		return nil, false, ErrOIDCBootstrapDenied
	}

	username, err := availableOIDCUsername(ctx, tx, identity)
	if err != nil {
		return nil, false, err
	}
	role := "user"
	if userCount == 0 && policy.BootstrapAdmin {
		role = "admin"
	}
	nickname := strings.TrimSpace(identity.DisplayName)
	if nickname == "" {
		nickname = username
	}
	now := time.Now().UTC()
	user = &model.User{
		ID: uuid.NewString(), Username: username, Password: "", Nickname: nickname,
		Role: role, AiEnabled: role == "admin", CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO "User"
		("id", "username", "password", "nickname", "role", "aiEnabled", "createdAt", "updatedAt")
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		user.ID, user.Username, user.Password, user.Nickname, user.Role, user.AiEnabled, now, now,
	); err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO "ExternalIdentity"
		("id", "userId", "issuer", "subject", "email", "emailVerified", "displayName", "createdAt", "lastLoginAt")
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), user.ID, identity.Issuer, identity.Subject, identity.Email,
		identity.EmailVerified, identity.DisplayName, now, now,
	); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return user, true, nil
}

func getOIDCUserTx(ctx context.Context, tx *sql.Tx, issuer, subject string) (*model.User, error) {
	user := &model.User{}
	err := tx.QueryRowContext(ctx, `SELECT u."id", u."username", u."password", u."nickname", u."role", u."aiEnabled", u."createdAt", u."updatedAt"
		FROM "ExternalIdentity" e JOIN "User" u ON u."id" = e."userId"
		WHERE e."issuer" = ? AND e."subject" = ?`, issuer, subject).Scan(
		&user.ID, &user.Username, &user.Password, &user.Nickname, &user.Role, &user.AiEnabled, &user.CreatedAt, &user.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	return user, err
}

func availableOIDCUsername(ctx context.Context, tx *sql.Tx, identity oidcauth.VerifiedIdentity) (string, error) {
	digest := sha256.Sum256([]byte(identity.Issuer + "\x00" + identity.Subject))
	suffix := hex.EncodeToString(digest[:4])
	candidates := []string{identity.PreferredUsername}
	if identity.EmailVerified {
		if at := strings.Index(identity.Email, "@"); at > 0 {
			candidates = append(candidates, identity.Email[:at])
		}
	}
	candidates = append(candidates, "oidc-"+suffix)
	for _, raw := range candidates {
		candidate := sanitizeOIDCUsername(raw)
		if candidate == "" {
			continue
		}
		available, err := usernameAvailableTx(ctx, tx, candidate)
		if err != nil {
			return "", err
		}
		if available {
			return candidate, nil
		}
		withSuffix := candidate
		withSuffix = truncateRunes(withSuffix, 23)
		withSuffix += "-" + suffix
		available, err = usernameAvailableTx(ctx, tx, withSuffix)
		if err != nil {
			return "", err
		}
		if available {
			return withSuffix, nil
		}
	}
	return "", fmt.Errorf("could not allocate a unique OIDC username")
}

func sanitizeOIDCUsername(raw string) string {
	raw = strings.TrimSpace(raw)
	var builder strings.Builder
	for _, value := range raw {
		if unicode.IsLetter(value) || unicode.IsDigit(value) || value == '.' || value == '_' || value == '-' {
			builder.WriteRune(value)
		} else if unicode.IsSpace(value) {
			builder.WriteRune('-')
		}
	}
	result := strings.Trim(builder.String(), ".-_")
	result = truncateRunes(result, 32)
	if len([]rune(result)) < 3 {
		return ""
	}
	return result
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func usernameAvailableTx(ctx context.Context, tx *sql.Tx, username string) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM "User" WHERE "username" = ?`, username).Scan(&count); err != nil {
		return false, err
	}
	return count == 0, nil
}
