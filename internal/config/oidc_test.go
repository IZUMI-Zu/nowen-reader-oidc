package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clearOIDCEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"OIDC_ENABLED",
		"OIDC_DISABLE_PASSWORD_LOGIN",
		"OIDC_ISSUER_URL",
		"OIDC_CLIENT_ID",
		"OIDC_CLIENT_SECRET",
		"OIDC_DISPLAY_NAME",
		"OIDC_SCOPES",
		"OIDC_AUTO_PROVISION",
		"OIDC_BOOTSTRAP_ADMIN_SUBJECTS",
		"OIDC_SESSION_MAX_AGE",
		"OIDC_CONFIG_MODE",
		"OIDC_CONFIG_KEY_FILE",
		"OIDC_CLIENT_SECRET_FILE",
		"OIDC_FORCE_PASSWORD_LOGIN",
		"PUBLIC_URL",
		"BASE_PATH",
	} {
		t.Setenv(key, "")
	}
}

func TestOIDCConfigModeUsesOneCompleteAuthority(t *testing.T) {
	clearOIDCEnv(t)
	if mode, err := GetOIDCConfigMode(); err != nil || mode != OIDCConfigModeDatabase {
		t.Fatalf("default mode = %q, %v", mode, err)
	}
	t.Setenv("OIDC_ENABLED", "false")
	if mode, err := GetOIDCConfigMode(); err != nil || mode != OIDCConfigModeEnvironment {
		t.Fatalf("legacy environment mode = %q, %v", mode, err)
	}
	t.Setenv("OIDC_CONFIG_MODE", "database")
	if mode, err := GetOIDCConfigMode(); err != nil || mode != OIDCConfigModeDatabase {
		t.Fatalf("explicit database mode = %q, %v", mode, err)
	}
	t.Setenv("OIDC_CONFIG_MODE", "mixed")
	if _, err := GetOIDCConfigMode(); err == nil {
		t.Fatal("invalid source mode was accepted")
	}
}

func TestOIDCEnvironmentClientSecretFile(t *testing.T) {
	clearOIDCEnv(t)
	secretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretFile, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("OIDC_ISSUER_URL", "https://id.example.com")
	t.Setenv("OIDC_CLIENT_ID", "client")
	t.Setenv("OIDC_CLIENT_SECRET_FILE", secretFile)
	t.Setenv("PUBLIC_URL", "https://reader.example.com")
	cfg, err := GetOIDCConfig()
	if err != nil || cfg.ClientSecret != "file-secret" {
		t.Fatalf("GetOIDCConfig() secret = %q, error = %v", cfg.ClientSecret, err)
	}
	t.Setenv("OIDC_CLIENT_SECRET", "duplicate")
	if _, err := GetOIDCConfig(); err == nil || !strings.Contains(err.Error(), "only one") {
		t.Fatalf("duplicate secret sources error = %v", err)
	}
}

func TestOIDCForcePasswordLoginFailsSafe(t *testing.T) {
	clearOIDCEnv(t)
	if forced, err := GetOIDCForcePasswordLogin(); err != nil || forced {
		t.Fatalf("default force switch = %v, %v", forced, err)
	}
	t.Setenv("OIDC_FORCE_PASSWORD_LOGIN", "true")
	if forced, err := GetOIDCForcePasswordLogin(); err != nil || !forced {
		t.Fatalf("true force switch = %v, %v", forced, err)
	}
	t.Setenv("OIDC_FORCE_PASSWORD_LOGIN", "tru")
	if forced, err := GetOIDCForcePasswordLogin(); err == nil || !forced {
		t.Fatalf("malformed force switch = %v, %v", forced, err)
	}
}

func TestValidateOIDCConfigValidatesDisabledWebDraft(t *testing.T) {
	draft := OIDCConfig{
		IssuerURL: "https://identity.example.com", ClientID: "nowen-reader", ClientSecret: "secret",
		PublicURL: "https://reader.example.com", Scopes: []string{"openid", "profile", "profile"},
	}
	resolved, err := ValidateOIDCConfig(draft, "/reader", true)
	if err != nil {
		t.Fatalf("ValidateOIDCConfig() error = %v", err)
	}
	if resolved.Enabled || resolved.CallbackURL != "https://reader.example.com/reader/api/auth/oidc/callback" {
		t.Fatalf("resolved draft = %+v", resolved)
	}
	if got := strings.Join(resolved.Scopes, " "); got != "openid profile" {
		t.Fatalf("Scopes = %q", got)
	}

	draft.ClientSecret = ""
	if _, err := ValidateOIDCConfig(draft, "/reader", true); err == nil || !strings.Contains(err.Error(), "OIDC_CLIENT_SECRET") {
		t.Fatalf("missing secret error = %v", err)
	}
}

func TestValidateOIDCConfigRejectsInvalidPopulatedDraftFields(t *testing.T) {
	tests := []struct {
		name string
		cfg  OIDCConfig
	}{
		{name: "insecure issuer", cfg: OIDCConfig{IssuerURL: "http://identity.example.com"}},
		{name: "discovery document instead of issuer", cfg: OIDCConfig{IssuerURL: "https://identity.example.com/.well-known/openid-configuration"}},
		{name: "public URL path", cfg: OIDCConfig{PublicURL: "https://reader.example.com/app"}},
		{name: "scope item contains space", cfg: OIDCConfig{Scopes: []string{"openid", "custom scope"}}},
		{name: "missing openid scope", cfg: OIDCConfig{Scopes: []string{"profile"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ValidateOIDCConfig(tt.cfg, "/reader", false); err == nil {
				t.Fatalf("invalid disabled draft was accepted: %+v", tt.cfg)
			}
		})
	}
}

func TestOIDCSessionTTLSecondsRejectsOverflowBeforeDurationConversion(t *testing.T) {
	for _, seconds := range []int64{-1, 299, 2_592_001, int64(^uint64(0) >> 1)} {
		if _, err := OIDCSessionTTLFromSeconds(seconds); err == nil {
			t.Fatalf("OIDCSessionTTLFromSeconds(%d) accepted invalid value", seconds)
		}
	}
	for _, seconds := range []int64{0, 300, 2_592_000} {
		if _, err := OIDCSessionTTLFromSeconds(seconds); err != nil {
			t.Fatalf("OIDCSessionTTLFromSeconds(%d) error = %v", seconds, err)
		}
	}
}

func TestValidateOIDCConfigPreservesOpaqueClientSecret(t *testing.T) {
	cfg := OIDCConfig{
		Enabled: true, IssuerURL: "https://identity.example.com", ClientID: " client ",
		ClientSecret: " secret with surrounding spaces ", PublicURL: "https://reader.example.com",
	}
	resolved, err := ValidateOIDCConfig(cfg, "/", false)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ClientSecret != cfg.ClientSecret {
		t.Fatalf("ClientSecret was changed during validation: %q", resolved.ClientSecret)
	}
	if resolved.ClientID != cfg.ClientID {
		t.Fatalf("ClientID was changed during validation: %q", resolved.ClientID)
	}
}

func TestOIDCConfigIsDisabledByDefault(t *testing.T) {
	clearOIDCEnv(t)

	cfg, err := GetOIDCConfig()
	if err != nil {
		t.Fatalf("GetOIDCConfig() error = %v", err)
	}
	if cfg.Enabled {
		t.Fatal("OIDC must be disabled unless explicitly enabled")
	}
	if cfg.DisablePasswordLogin {
		t.Fatal("password login must remain enabled by default")
	}
}

func TestOIDCPasswordLoginSwitchIsExplicitAndFailClosed(t *testing.T) {
	t.Run("malformed value remains closed", func(t *testing.T) {
		clearOIDCEnv(t)
		t.Setenv("OIDC_DISABLE_PASSWORD_LOGIN", "tru")
		cfg, err := GetOIDCConfig()
		if err == nil || !strings.Contains(err.Error(), "OIDC_DISABLE_PASSWORD_LOGIN") {
			t.Fatalf("GetOIDCConfig() error = %v, want boolean validation error", err)
		}
		if !cfg.DisablePasswordLogin {
			t.Fatal("malformed password-login switch reopened password login")
		}
	})

	t.Run("requires OIDC", func(t *testing.T) {
		clearOIDCEnv(t)
		t.Setenv("OIDC_DISABLE_PASSWORD_LOGIN", "true")
		cfg, err := GetOIDCConfig()
		if err == nil || !strings.Contains(err.Error(), "OIDC_ENABLED") {
			t.Fatalf("GetOIDCConfig() error = %v, want OIDC requirement", err)
		}
		if !cfg.DisablePasswordLogin {
			t.Fatal("invalid OIDC configuration reopened password login")
		}
	})

	t.Run("enabled", func(t *testing.T) {
		clearOIDCEnv(t)
		t.Setenv("OIDC_ENABLED", "true")
		t.Setenv("OIDC_DISABLE_PASSWORD_LOGIN", "true")
		t.Setenv("OIDC_ISSUER_URL", "https://id.example.com")
		t.Setenv("OIDC_CLIENT_ID", "client")
		t.Setenv("OIDC_CLIENT_SECRET", "secret")
		t.Setenv("PUBLIC_URL", "https://reader.example.com")
		cfg, err := GetOIDCConfig()
		if err != nil {
			t.Fatalf("GetOIDCConfig() error = %v", err)
		}
		if !cfg.DisablePasswordLogin {
			t.Fatal("OIDC_DISABLE_PASSWORD_LOGIN=true was ignored")
		}
	})

	t.Run("provider configuration error remains closed", func(t *testing.T) {
		clearOIDCEnv(t)
		t.Setenv("OIDC_ENABLED", "true")
		t.Setenv("OIDC_DISABLE_PASSWORD_LOGIN", "true")
		t.Setenv("OIDC_ISSUER_URL", "https://id.example.com")
		t.Setenv("OIDC_CLIENT_ID", "client")
		t.Setenv("PUBLIC_URL", "https://reader.example.com")
		cfg, err := GetOIDCConfig()
		if err == nil {
			t.Fatal("incomplete OIDC configuration was accepted")
		}
		if !cfg.DisablePasswordLogin {
			t.Fatal("incomplete OIDC configuration reopened password login")
		}
	})
}

func TestOIDCConfigBuildsCallbackFromPublicURLAndBasePath(t *testing.T) {
	clearOIDCEnv(t)
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("OIDC_ISSUER_URL", "https://identity.example.com/realms/readers")
	t.Setenv("OIDC_CLIENT_ID", "nowen-reader")
	t.Setenv("OIDC_CLIENT_SECRET", "secret")
	t.Setenv("OIDC_DISPLAY_NAME", "Company Login")
	t.Setenv("OIDC_SCOPES", "openid profile groups profile")
	t.Setenv("OIDC_AUTO_PROVISION", "true")
	t.Setenv("OIDC_BOOTSTRAP_ADMIN_SUBJECTS", "admin-sub, second-admin ")
	t.Setenv("OIDC_SESSION_MAX_AGE", "8h")
	t.Setenv("PUBLIC_URL", "https://reader.example.com")
	t.Setenv("BASE_PATH", "/reader/")

	cfg, err := GetOIDCConfig()
	if err != nil {
		t.Fatalf("GetOIDCConfig() error = %v", err)
	}
	if got, want := cfg.CallbackURL, "https://reader.example.com/reader/api/auth/oidc/callback"; got != want {
		t.Fatalf("CallbackURL = %q, want %q", got, want)
	}
	if cfg.ProviderName != "Company Login" || !cfg.AutoProvision {
		t.Fatalf("unexpected display/provisioning config: %+v", cfg)
	}
	if cfg.SessionAbsoluteTTL != 8*time.Hour {
		t.Fatalf("SessionAbsoluteTTL = %v, want 8h", cfg.SessionAbsoluteTTL)
	}
	if got := strings.Join(cfg.Scopes, " "); got != "openid profile groups" {
		t.Fatalf("Scopes = %q", got)
	}
	if !cfg.IsBootstrapAdmin("admin-sub") || !cfg.IsBootstrapAdmin("second-admin") {
		t.Fatalf("bootstrap subjects not parsed: %#v", cfg.BootstrapAdminSubjects)
	}
}

func TestOIDCConfigRequiresOpenIDScope(t *testing.T) {
	clearOIDCEnv(t)
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("OIDC_ISSUER_URL", "https://id.example.com")
	t.Setenv("OIDC_CLIENT_ID", "client")
	t.Setenv("OIDC_CLIENT_SECRET", "secret")
	t.Setenv("PUBLIC_URL", "https://reader.example.com")
	t.Setenv("OIDC_SCOPES", "profile email")
	if _, err := GetOIDCConfig(); err == nil || !strings.Contains(err.Error(), "openid") {
		t.Fatalf("GetOIDCConfig() error = %v, want openid requirement", err)
	}
}

func TestOIDCConfigRejectsIncompleteOrUnsafeConfiguration(t *testing.T) {
	tests := []struct {
		name      string
		publicURL string
		issuerURL string
		clientID  string
		secret    string
		wantError string
	}{
		{name: "missing issuer", publicURL: "https://reader.example.com", clientID: "client", secret: "secret", wantError: "OIDC_ISSUER_URL"},
		{name: "issuer query", publicURL: "https://reader.example.com", issuerURL: "https://id.example.com/realm?tenant=x", clientID: "client", secret: "secret", wantError: "issuer"},
		{name: "missing client id", publicURL: "https://reader.example.com", issuerURL: "https://id.example.com", secret: "secret", wantError: "OIDC_CLIENT_ID"},
		{name: "missing secret", publicURL: "https://reader.example.com", issuerURL: "https://id.example.com", clientID: "client", wantError: "OIDC_CLIENT_SECRET"},
		{name: "request derived URL forbidden", issuerURL: "https://id.example.com", clientID: "client", secret: "secret", wantError: "PUBLIC_URL"},
		{name: "public URL path", publicURL: "https://reader.example.com/app", issuerURL: "https://id.example.com", clientID: "client", secret: "secret", wantError: "origin"},
		{name: "insecure public URL", publicURL: "http://reader.example.com", issuerURL: "https://id.example.com", clientID: "client", secret: "secret", wantError: "HTTPS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearOIDCEnv(t)
			t.Setenv("OIDC_ENABLED", "true")
			t.Setenv("PUBLIC_URL", tt.publicURL)
			t.Setenv("OIDC_ISSUER_URL", tt.issuerURL)
			t.Setenv("OIDC_CLIENT_ID", tt.clientID)
			t.Setenv("OIDC_CLIENT_SECRET", tt.secret)

			_, err := GetOIDCConfig()
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("GetOIDCConfig() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func TestOIDCConfigAllowsHTTPOnlyForLoopbackDevelopment(t *testing.T) {
	clearOIDCEnv(t)
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("OIDC_ISSUER_URL", "https://id.example.com")
	t.Setenv("OIDC_CLIENT_ID", "client")
	t.Setenv("OIDC_CLIENT_SECRET", "secret")
	t.Setenv("PUBLIC_URL", "http://127.0.0.1:5080")

	cfg, err := GetOIDCConfig()
	if err != nil {
		t.Fatalf("loopback PUBLIC_URL rejected: %v", err)
	}
	if cfg.SecureCookies {
		t.Fatal("loopback HTTP development must not set Secure cookies")
	}
}
