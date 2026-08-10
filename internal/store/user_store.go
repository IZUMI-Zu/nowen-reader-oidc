package store

import (
	"database/sql"
	"time"

	"github.com/nowen-reader/nowen-reader/internal/model"
)

// CreateUser inserts a new user into the database.
func CreateUser(user *model.User) error {
	now := time.Now()
	user.CreatedAt = now
	user.UpdatedAt = now

	_, err := db.Exec(
		`INSERT INTO "User" ("id", "username", "password", "nickname", "role", "aiEnabled", "createdAt", "updatedAt")
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		user.ID, user.Username, user.Password, user.Nickname, user.Role, user.AiEnabled, now, now,
	)
	return err
}

// GetUserByUsername finds a user by username.
func GetUserByUsername(username string) (*model.User, error) {
	user := &model.User{}
	err := db.QueryRow(
		`SELECT "id", "username", "password", "nickname", "role", "aiEnabled", "createdAt", "updatedAt"
		 FROM "User" WHERE "username" = ?`,
		username,
	).Scan(&user.ID, &user.Username, &user.Password, &user.Nickname, &user.Role, &user.AiEnabled, &user.CreatedAt, &user.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	return user, err
}

// GetUserByID finds a user by ID.
func GetUserByID(id string) (*model.User, error) {
	user := &model.User{}
	err := db.QueryRow(
		`SELECT "id", "username", "password", "nickname", "role", "aiEnabled", "createdAt", "updatedAt"
		 FROM "User" WHERE "id" = ?`,
		id,
	).Scan(&user.ID, &user.Username, &user.Password, &user.Nickname, &user.Role, &user.AiEnabled, &user.CreatedAt, &user.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	return user, err
}

// CountUsers returns the total number of users.
func CountUsers() (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM "User"`).Scan(&count)
	return count, err
}

// ListUsers returns all users (without password), ordered by creation time.
func ListUsers() ([]model.AuthUser, error) {
	rows, err := db.Query(
		`SELECT "id", "username", "nickname", "role", "aiEnabled", "password" != ''
		 FROM "User" ORDER BY "createdAt" ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []model.AuthUser
	for rows.Next() {
		var u model.AuthUser
		if err := rows.Scan(&u.ID, &u.Username, &u.Nickname, &u.Role, &u.AiEnabled, &u.HasPassword); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// UpdateUserPassword updates a user's password hash.
func UpdateUserPassword(userID, hashedPassword string) error {
	_, err := db.Exec(
		`UPDATE "User" SET "password" = ?, "updatedAt" = ? WHERE "id" = ?`,
		hashedPassword, time.Now(), userID,
	)
	return err
}

// SetInitialUserPassword atomically adds a password only when the account does
// not already have one. This prevents concurrent setup requests from replacing
// a credential without proving the existing password.
func SetInitialUserPassword(userID, hashedPassword string) (bool, error) {
	result, err := db.Exec(`UPDATE "User" SET "password" = ?, "updatedAt" = ? WHERE "id" = ? AND "password" = ''`,
		hashedPassword, time.Now().UTC(), userID)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

// UpdateUserProfile updates a user's nickname.
func UpdateUserProfile(userID, nickname string) error {
	_, err := db.Exec(
		`UPDATE "User" SET "nickname" = ?, "updatedAt" = ? WHERE "id" = ?`,
		nickname, time.Now(), userID,
	)
	return err
}

// DeleteUser removes a user by ID (cascade deletes sessions).
func DeleteUser(userID string) error {
	_, err := db.Exec(`DELETE FROM "User" WHERE "id" = ?`, userID)
	return err
}

// UpdateUserRole 更新用户角色（admin 或 user）。
func UpdateUserRole(userID, role string) error {
	_, err := db.Exec(
		`UPDATE "User" SET "role" = ?, "updatedAt" = ? WHERE "id" = ?`,
		role, time.Now(), userID,
	)
	return err
}

// UpdateUserAiEnabled 更新用户的 AI 使用权限。
func UpdateUserAiEnabled(userID string, aiEnabled bool) error {
	_, err := db.Exec(
		`UPDATE "User" SET "aiEnabled" = ?, "updatedAt" = ? WHERE "id" = ?`,
		aiEnabled, time.Now(), userID,
	)
	return err
}


// GetUserRole 获取用户角色并写入 role 指针。用于轻量级权限检查。
func GetUserRole(userID string, role *string) error {
	return db.QueryRow(`SELECT "role" FROM "User" WHERE "id" = ?`, userID).Scan(role)
}

// ============================================================
// Session operations
// ============================================================

// CreateSession inserts a new user session.
func CreateSession(session *model.UserSession) error {
	now := time.Now().UTC()
	if session.AuthMethod == "" {
		session.AuthMethod = model.SessionAuthMethodPassword
	}
	if session.AuthenticatedAt.IsZero() {
		session.AuthenticatedAt = now
	}
	session.CreatedAt = now
	_, err := db.Exec(
		`INSERT INTO "UserSession"
		 ("id", "userId", "expiresAt", "authMethod", "authenticatedAt", "absoluteExpiresAt", "createdAt")
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.UserID, session.ExpiresAt, session.AuthMethod,
		session.AuthenticatedAt, session.AbsoluteExpiresAt, session.CreatedAt,
	)
	return err
}

// GetSessionWithUser retrieves a session and its associated user.
// Returns nil, nil if not found.
func GetSessionWithUser(token string) (*model.UserSession, *model.User, error) {
	session := &model.UserSession{}
	user := &model.User{}
	var absoluteExpiresAt sql.NullTime

	err := db.QueryRow(
		`SELECT s."id", s."userId", s."expiresAt", s."authMethod",
		        s."authenticatedAt", s."absoluteExpiresAt", s."createdAt",
		        u."id", u."username", u."password", u."nickname", u."role", u."aiEnabled", u."createdAt", u."updatedAt"
		 FROM "UserSession" s
		 JOIN "User" u ON u."id" = s."userId"
		 WHERE s."id" = ?`,
		token,
	).Scan(
		&session.ID, &session.UserID, &session.ExpiresAt, &session.AuthMethod,
		&session.AuthenticatedAt, &absoluteExpiresAt, &session.CreatedAt,
		&user.ID, &user.Username, &user.Password, &user.Nickname, &user.Role, &user.AiEnabled, &user.CreatedAt, &user.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if absoluteExpiresAt.Valid {
		session.AbsoluteExpiresAt = &absoluteExpiresAt.Time
	}
	return session, user, nil
}

// DeleteSession removes a session by token.
func DeleteSession(token string) error {
	_, err := db.Exec(`DELETE FROM "UserSession" WHERE "id" = ?`, token)
	return err
}

// DeleteSessionsByUserID removes all sessions for a user, except the given token (current session).
func DeleteSessionsByUserID(userID string, exceptToken string) error {
	_, err := db.Exec(`DELETE FROM "UserSession" WHERE "userId" = ? AND "id" != ?`, userID, exceptToken)
	return err
}

// RenewSession 更新 Session 的过期时间（自动续期）。
func RenewSession(token string, newExpiry time.Time) error {
	_, err := db.Exec(`UPDATE "UserSession" SET "expiresAt" = ? WHERE "id" = ?`, newExpiry, token)
	return err
}

// MarkSessionAuthenticated records a successful password or OIDC
// reauthentication for later sensitive-operation checks.
func MarkSessionAuthenticated(token string, authenticatedAt time.Time) error {
	_, err := db.Exec(`UPDATE "UserSession" SET "authenticatedAt" = ? WHERE "id" = ?`, authenticatedAt, token)
	return err
}

// CleanExpiredSessions 删除所有已过期的 Session，返回删除的数量。
func CleanExpiredSessions() (int64, error) {
	result, err := db.Exec(`DELETE FROM "UserSession" WHERE "expiresAt" < ?`, time.Now())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
