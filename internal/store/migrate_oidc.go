package store

import "strings"

func init() {
	Migrations = append(Migrations, Migration{
		Version:     40,
		Description: "Add OIDC external identities and browser-bound login transactions",
		SQL: strings.Join([]string{
			`ALTER TABLE "UserSession" ADD COLUMN "authMethod" TEXT NOT NULL DEFAULT 'password';`,
			`ALTER TABLE "UserSession" ADD COLUMN "authenticatedAt" DATETIME;`,
			`ALTER TABLE "UserSession" ADD COLUMN "absoluteExpiresAt" DATETIME;`,
			`UPDATE "UserSession" SET "authenticatedAt" = "createdAt" WHERE "authenticatedAt" IS NULL;`,
			`CREATE TABLE IF NOT EXISTS "ExternalIdentity" (
				"id"            TEXT NOT NULL PRIMARY KEY,
				"userId"        TEXT NOT NULL,
				"issuer"        TEXT NOT NULL,
				"subject"       TEXT NOT NULL,
				"email"         TEXT NOT NULL DEFAULT '',
				"emailVerified" BOOLEAN NOT NULL DEFAULT 0,
				"displayName"   TEXT NOT NULL DEFAULT '',
				"createdAt"     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				"lastLoginAt"   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				CONSTRAINT "ExternalIdentity_userId_fkey" FOREIGN KEY ("userId")
					REFERENCES "User" ("id") ON DELETE CASCADE ON UPDATE CASCADE
			);`,
			`CREATE UNIQUE INDEX IF NOT EXISTS "ExternalIdentity_issuer_subject_key" ON "ExternalIdentity"("issuer", "subject");`,
			`CREATE UNIQUE INDEX IF NOT EXISTS "ExternalIdentity_user_issuer_key" ON "ExternalIdentity"("userId", "issuer");`,
			`CREATE TABLE IF NOT EXISTS "OIDCLoginTransaction" (
				"stateHash"     TEXT NOT NULL PRIMARY KEY,
				"bindingHash"   TEXT NOT NULL,
				"nonce"         TEXT NOT NULL,
				"pkceVerifier"  TEXT NOT NULL,
				"purpose"       TEXT NOT NULL,
				"sessionUserId" TEXT NOT NULL DEFAULT '',
				"returnTo"      TEXT NOT NULL,
				"expiresAt"     DATETIME NOT NULL,
				"consumedAt"    DATETIME,
				"createdAt"     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			);`,
			`CREATE INDEX IF NOT EXISTS "OIDCLoginTransaction_expiresAt_idx" ON "OIDCLoginTransaction"("expiresAt");`,
		}, "\n"),
	})
}
