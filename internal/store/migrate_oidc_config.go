package store

import "strings"

func init() {
	Migrations = append(Migrations, Migration{
		Version:     41,
		Description: "Add administrator-managed OIDC provider configuration and audit log",
		SQL: strings.Join([]string{
			`CREATE TABLE IF NOT EXISTS "OIDCProviderConfig" (
				"id"                       INTEGER NOT NULL PRIMARY KEY CHECK ("id" = 1),
					"enabled"                  BOOLEAN NOT NULL DEFAULT 0 CHECK ("enabled" IN (0, 1)),
				"issuerURL"                TEXT NOT NULL DEFAULT '',
				"clientID"                 TEXT NOT NULL DEFAULT '',
				"secretCiphertext"         TEXT NOT NULL DEFAULT '',
				"secretKeyID"              TEXT NOT NULL DEFAULT '',
				"providerName"             TEXT NOT NULL DEFAULT 'OpenID Connect',
				"scopes"                   TEXT NOT NULL DEFAULT 'openid profile email',
				"publicURL"                TEXT NOT NULL DEFAULT '',
					"autoProvision"            BOOLEAN NOT NULL DEFAULT 0 CHECK ("autoProvision" IN (0, 1)),
					"sessionTTLSeconds"        INTEGER NOT NULL DEFAULT 43200 CHECK ("sessionTTLSeconds" BETWEEN 300 AND 2592000),
					"disablePasswordLogin"     BOOLEAN NOT NULL DEFAULT 0 CHECK ("disablePasswordLogin" IN (0, 1)),
					"revision"                 INTEGER NOT NULL DEFAULT 0 CHECK ("revision" >= 0),
				"verifiedFingerprint"      TEXT NOT NULL DEFAULT '',
				"lastVerifiedAt"           DATETIME,
				"updatedBy"                TEXT NOT NULL DEFAULT '',
					"updatedAt"                DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
					CHECK ("disablePasswordLogin" = 0 OR "enabled" = 1)
			);`,
			`INSERT OR IGNORE INTO "OIDCProviderConfig" ("id") VALUES (1);`,
			`CREATE TABLE IF NOT EXISTS "OIDCConfigAudit" (
				"id"             TEXT NOT NULL PRIMARY KEY,
				"actorUserID"    TEXT NOT NULL DEFAULT '',
				"action"         TEXT NOT NULL,
				"result"         TEXT NOT NULL,
				"changedFields"  TEXT NOT NULL DEFAULT '[]',
					"configRevision" INTEGER NOT NULL CHECK ("configRevision" >= 0),
				"requestID"      TEXT NOT NULL DEFAULT '',
				"createdAt"      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			);`,
			`CREATE INDEX IF NOT EXISTS "OIDCConfigAudit_createdAt_idx" ON "OIDCConfigAudit"("createdAt");`,
			`ALTER TABLE "OIDCLoginTransaction" ADD COLUMN "configRevision" INTEGER NOT NULL DEFAULT 0;`,
			`ALTER TABLE "OIDCLoginTransaction" ADD COLUMN "configFingerprint" TEXT NOT NULL DEFAULT '';`,
		}, "\n"),
	})
}
