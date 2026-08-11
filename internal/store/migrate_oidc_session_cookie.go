package store

func init() {
	Migrations = append(Migrations, Migration{
		Version:     42,
		Description: "Preserve the cookie security policy of issued OIDC sessions",
		SQL: `ALTER TABLE "UserSession" ADD COLUMN "cookieSecure" BOOLEAN
			CHECK ("cookieSecure" IN (0, 1));`,
	})
}
