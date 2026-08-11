package store

func init() {
	Migrations = append(Migrations, Migration{
		Version:     43,
		Description: "Bind OIDC configuration tests to their initiating administrator session",
		SQL:         `ALTER TABLE "OIDCLoginTransaction" ADD COLUMN "sessionId" TEXT NOT NULL DEFAULT '';`,
	})
}
