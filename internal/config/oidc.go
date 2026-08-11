package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultOIDCSessionAbsoluteTTL = 12 * time.Hour
	minOIDCSessionAbsoluteTTL     = 5 * time.Minute
	maxOIDCSessionAbsoluteTTL     = 30 * 24 * time.Hour
)

type OIDCConfigMode string

const (
	OIDCConfigModeAuto        OIDCConfigMode = "auto"
	OIDCConfigModeEnvironment OIDCConfigMode = "environment"
	OIDCConfigModeDatabase    OIDCConfigMode = "database"
)

// OIDCConfig contains the server-side relying-party configuration. Redirect
// URLs are derived only from PUBLIC_URL and BASE_PATH, never from request
// headers supplied by a client or reverse proxy.
type OIDCConfig struct {
	Enabled                bool
	IssuerURL              string
	ClientID               string
	ClientSecret           string
	ProviderName           string
	Scopes                 []string
	PublicURL              string
	CallbackURL            string
	AutoProvision          bool
	BootstrapAdminSubjects map[string]struct{}
	SessionAbsoluteTTL     time.Duration
	SecureCookies          bool
	DisablePasswordLogin   bool
}

// IsBootstrapAdmin reports whether a verified OIDC subject is explicitly
// allowed to bootstrap the first local administrator.
func (c OIDCConfig) IsBootstrapAdmin(subject string) bool {
	_, ok := c.BootstrapAdminSubjects[subject]
	return ok
}

// GetOIDCConfig resolves and validates OIDC configuration from environment
// variables. OIDC is deliberately opt-in. Password login remains available on
// invalid configuration unless the operator explicitly requested the
// fail-closed OIDC_DISABLE_PASSWORD_LOGIN mode.
func GetOIDCConfig() (OIDCConfig, error) {
	disablePasswordLogin, disablePasswordErr := parseDisablePasswordLogin()
	enabled, enabledErr := parseOptionalBool("OIDC_ENABLED", false)
	cfg := OIDCConfig{
		Enabled:                enabled,
		DisablePasswordLogin:   disablePasswordLogin,
		ProviderName:           strings.TrimSpace(os.Getenv("OIDC_DISPLAY_NAME")),
		BootstrapAdminSubjects: make(map[string]struct{}),
	}
	if disablePasswordErr != nil {
		return cfg, disablePasswordErr
	}
	if enabledErr != nil {
		return cfg, enabledErr
	}
	if !enabled {
		return ValidateOIDCConfig(cfg, os.Getenv("BASE_PATH"), false)
	}
	cfg.IssuerURL = strings.TrimSpace(os.Getenv("OIDC_ISSUER_URL"))
	cfg.ClientID = os.Getenv("OIDC_CLIENT_ID")
	cfg.ClientSecret = os.Getenv("OIDC_CLIENT_SECRET")
	if secretFile := strings.TrimSpace(os.Getenv("OIDC_CLIENT_SECRET_FILE")); secretFile != "" {
		if cfg.ClientSecret != "" {
			return cfg, fmt.Errorf("set only one of OIDC_CLIENT_SECRET and OIDC_CLIENT_SECRET_FILE")
		}
		secret, err := os.ReadFile(secretFile)
		if err != nil {
			return cfg, fmt.Errorf("read OIDC_CLIENT_SECRET_FILE: %w", err)
		}
		cfg.ClientSecret = strings.TrimSuffix(strings.TrimSuffix(string(secret), "\n"), "\r")
	}
	cfg.PublicURL = strings.TrimSpace(os.Getenv("PUBLIC_URL"))
	var err error
	cfg.AutoProvision, err = parseOptionalBool("OIDC_AUTO_PROVISION", false)
	if err != nil {
		return cfg, err
	}
	if rawScopes := strings.TrimSpace(os.Getenv("OIDC_SCOPES")); rawScopes != "" {
		cfg.Scopes = strings.Fields(rawScopes)
	}
	for _, subject := range strings.Split(os.Getenv("OIDC_BOOTSTRAP_ADMIN_SUBJECTS"), ",") {
		if subject = strings.TrimSpace(subject); subject != "" {
			cfg.BootstrapAdminSubjects[subject] = struct{}{}
		}
	}

	if rawTTL := strings.TrimSpace(os.Getenv("OIDC_SESSION_MAX_AGE")); rawTTL != "" {
		ttl, parseErr := time.ParseDuration(rawTTL)
		if parseErr != nil {
			return cfg, fmt.Errorf("OIDC_SESSION_MAX_AGE must be a duration between 5m and 720h")
		}
		cfg.SessionAbsoluteTTL = ttl
	}

	return ValidateOIDCConfig(cfg, os.Getenv("BASE_PATH"), false)
}

// GetOIDCConfigMode selects one complete configuration source. Auto preserves
// existing installations that explicitly set OIDC_ENABLED while making the
// database-backed admin configuration the default for new installations.
func GetOIDCConfigMode() (OIDCConfigMode, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("OIDC_CONFIG_MODE")))
	if raw == "" || raw == string(OIDCConfigModeAuto) {
		if strings.TrimSpace(os.Getenv("OIDC_ENABLED")) != "" {
			return OIDCConfigModeEnvironment, nil
		}
		return OIDCConfigModeDatabase, nil
	}
	switch OIDCConfigMode(raw) {
	case OIDCConfigModeEnvironment, OIDCConfigModeDatabase:
		return OIDCConfigMode(raw), nil
	default:
		return "", fmt.Errorf("OIDC_CONFIG_MODE must be auto, environment, or database")
	}
}

// GetOIDCForcePasswordLogin is a deployment-side recovery switch. A malformed
// non-empty value fails safe by forcing the password entry open.
func GetOIDCForcePasswordLogin() (bool, error) {
	raw := strings.TrimSpace(os.Getenv("OIDC_FORCE_PASSWORD_LOGIN"))
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return true, fmt.Errorf("OIDC_FORCE_PASSWORD_LOGIN must be a boolean")
	}
	return value, nil
}

// ValidateOIDCConfig normalizes and validates a complete OIDC configuration.
// requireProvider is used for disabled Web drafts that still need to be probed
// or tested before activation.
func ValidateOIDCConfig(cfg OIDCConfig, basePath string, requireProvider bool) (OIDCConfig, error) {
	cfg.IssuerURL = strings.TrimSpace(cfg.IssuerURL)
	cfg.ProviderName = strings.TrimSpace(cfg.ProviderName)
	cfg.PublicURL = strings.TrimSpace(cfg.PublicURL)
	if cfg.ProviderName == "" {
		cfg.ProviderName = "OpenID Connect"
	}
	if cfg.SessionAbsoluteTTL == 0 {
		cfg.SessionAbsoluteTTL = defaultOIDCSessionAbsoluteTTL
	}
	if cfg.SessionAbsoluteTTL < minOIDCSessionAbsoluteTTL || cfg.SessionAbsoluteTTL > maxOIDCSessionAbsoluteTTL {
		return cfg, fmt.Errorf("OIDC_SESSION_MAX_AGE must be a duration between 5m and 720h")
	}

	if cfg.DisablePasswordLogin && !cfg.Enabled {
		return cfg, fmt.Errorf("OIDC_ENABLED must be true when OIDC_DISABLE_PASSWORD_LOGIN is enabled")
	}
	providerRequired := cfg.Enabled || requireProvider

	if providerRequired {
		for key, value := range map[string]string{
			"OIDC_ISSUER_URL":    cfg.IssuerURL,
			"OIDC_CLIENT_ID":     cfg.ClientID,
			"OIDC_CLIENT_SECRET": cfg.ClientSecret,
			"PUBLIC_URL":         cfg.PublicURL,
		} {
			if value == "" {
				return cfg, fmt.Errorf("%s is required when OIDC is enabled or being validated", key)
			}
		}
	}

	if cfg.IssuerURL != "" {
		issuer, err := url.Parse(cfg.IssuerURL)
		if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
			return cfg, fmt.Errorf("OIDC issuer must be an absolute HTTPS URL without userinfo, query, or fragment")
		}
		if strings.HasSuffix(strings.TrimSuffix(issuer.Path, "/"), "/.well-known/openid-configuration") {
			return cfg, fmt.Errorf("OIDC issuer must be the issuer identifier, not the Discovery document URL")
		}
	}

	if cfg.PublicURL != "" {
		publicURL, err := url.Parse(cfg.PublicURL)
		if err != nil || publicURL.Host == "" || publicURL.User != nil || publicURL.RawQuery != "" || publicURL.Fragment != "" {
			return cfg, fmt.Errorf("PUBLIC_URL must be an absolute origin")
		}
		if publicURL.Path != "" && publicURL.Path != "/" {
			return cfg, fmt.Errorf("PUBLIC_URL must contain only an origin; put the deployment path in BASE_PATH")
		}
		if publicURL.Scheme != "https" {
			host := publicURL.Hostname()
			if publicURL.Scheme != "http" || !isLoopbackHost(host) {
				return cfg, fmt.Errorf("PUBLIC_URL must use HTTPS except for loopback development")
			}
		}
		cfg.PublicURL = strings.TrimSuffix(cfg.PublicURL, "/")
		cfg.SecureCookies = publicURL.Scheme == "https"

		normalizedBasePath, err := NormalizeBasePath(basePath)
		if err != nil {
			return cfg, err
		}
		callbackPath := "/api/auth/oidc/callback"
		if normalizedBasePath != "/" {
			callbackPath = normalizedBasePath + callbackPath
		}
		cfg.CallbackURL = cfg.PublicURL + callbackPath
	}

	normalizedScopes, err := NormalizeOIDCScopes(cfg.Scopes)
	if err != nil {
		return cfg, err
	}
	cfg.Scopes = normalizedScopes
	if cfg.BootstrapAdminSubjects == nil {
		cfg.BootstrapAdminSubjects = make(map[string]struct{})
	}
	return cfg, nil
}

// OIDCSessionTTLFromSeconds validates persisted/API seconds before converting
// to time.Duration, preventing oversized integers from wrapping during the
// conversion. Zero selects the documented default.
func OIDCSessionTTLFromSeconds(seconds int64) (time.Duration, error) {
	if seconds == 0 {
		return defaultOIDCSessionAbsoluteTTL, nil
	}
	minSeconds := int64(minOIDCSessionAbsoluteTTL / time.Second)
	maxSeconds := int64(maxOIDCSessionAbsoluteTTL / time.Second)
	if seconds < minSeconds || seconds > maxSeconds {
		return 0, fmt.Errorf("OIDC_SESSION_MAX_AGE must be a duration between 5m and 720h")
	}
	return time.Duration(seconds) * time.Second, nil
}

// NormalizeOIDCScopes applies the RFC 6749 scope-token grammar, removes exact
// duplicates while preserving order, and enforces the OpenID Connect scope.
func NormalizeOIDCScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		scopes = []string{"openid", "profile", "email"}
	}
	seen := make(map[string]struct{}, len(scopes))
	normalizedScopes := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if !validOIDCScopeToken(scope) {
			return nil, fmt.Errorf("OIDC_SCOPES contains an invalid OAuth scope token")
		}
		if _, exists := seen[scope]; exists {
			continue
		}
		seen[scope] = struct{}{}
		normalizedScopes = append(normalizedScopes, scope)
	}
	if _, ok := seen["openid"]; !ok {
		return nil, fmt.Errorf("OIDC_SCOPES must include openid")
	}
	return normalizedScopes, nil
}

// parseDisablePasswordLogin treats any non-empty malformed value as enabled.
// This prevents an operator typo from silently reopening password login while
// still returning an error that makes the invalid configuration visible.
func parseDisablePasswordLogin() (bool, error) {
	raw := strings.TrimSpace(os.Getenv("OIDC_DISABLE_PASSWORD_LOGIN"))
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return true, fmt.Errorf("OIDC_DISABLE_PASSWORD_LOGIN must be a boolean")
	}
	return value, nil
}

func validOIDCScopeToken(scope string) bool {
	if scope == "" {
		return false
	}
	for _, value := range []byte(scope) {
		if value < 0x21 || value > 0x7e || value == '"' || value == '\\' {
			return false
		}
	}
	return true
}

func parseOptionalBool(key string, defaultValue bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return value, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
