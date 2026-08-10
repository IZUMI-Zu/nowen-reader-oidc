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

const defaultOIDCSessionAbsoluteTTL = 12 * time.Hour

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
		Scopes:                 []string{"openid", "profile", "email"},
		BootstrapAdminSubjects: make(map[string]struct{}),
		SessionAbsoluteTTL:     defaultOIDCSessionAbsoluteTTL,
	}
	if disablePasswordErr != nil {
		return cfg, disablePasswordErr
	}
	if enabledErr != nil {
		return cfg, enabledErr
	}
	if cfg.DisablePasswordLogin && !cfg.Enabled {
		return cfg, fmt.Errorf("OIDC_ENABLED must be true when OIDC_DISABLE_PASSWORD_LOGIN is enabled")
	}
	if cfg.ProviderName == "" {
		cfg.ProviderName = "OpenID Connect"
	}
	if !enabled {
		return cfg, nil
	}

	cfg.IssuerURL = strings.TrimSpace(os.Getenv("OIDC_ISSUER_URL"))
	cfg.ClientID = strings.TrimSpace(os.Getenv("OIDC_CLIENT_ID"))
	cfg.ClientSecret = strings.TrimSpace(os.Getenv("OIDC_CLIENT_SECRET"))
	cfg.PublicURL = strings.TrimSpace(os.Getenv("PUBLIC_URL"))
	for key, value := range map[string]string{
		"OIDC_ISSUER_URL":    cfg.IssuerURL,
		"OIDC_CLIENT_ID":     cfg.ClientID,
		"OIDC_CLIENT_SECRET": cfg.ClientSecret,
		"PUBLIC_URL":         cfg.PublicURL,
	} {
		if value == "" {
			return cfg, fmt.Errorf("%s is required when OIDC is enabled", key)
		}
	}

	issuer, err := url.Parse(cfg.IssuerURL)
	if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return cfg, fmt.Errorf("OIDC issuer must be an absolute HTTPS URL without userinfo, query, or fragment")
	}

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

	basePath, err := NormalizeBasePath(os.Getenv("BASE_PATH"))
	if err != nil {
		return cfg, err
	}
	callbackPath := "/api/auth/oidc/callback"
	if basePath != "/" {
		callbackPath = basePath + callbackPath
	}
	cfg.CallbackURL = cfg.PublicURL + callbackPath

	cfg.AutoProvision, err = parseOptionalBool("OIDC_AUTO_PROVISION", false)
	if err != nil {
		return cfg, err
	}
	if rawScopes := strings.TrimSpace(os.Getenv("OIDC_SCOPES")); rawScopes != "" {
		cfg.Scopes = nil
		seen := make(map[string]struct{})
		for _, scope := range strings.Fields(rawScopes) {
			if !validOIDCScopeToken(scope) {
				return cfg, fmt.Errorf("OIDC_SCOPES contains an invalid OAuth scope token")
			}
			if _, exists := seen[scope]; !exists {
				seen[scope] = struct{}{}
				cfg.Scopes = append(cfg.Scopes, scope)
			}
		}
		if _, ok := seen["openid"]; !ok {
			return cfg, fmt.Errorf("OIDC_SCOPES must include openid")
		}
	}
	for _, subject := range strings.Split(os.Getenv("OIDC_BOOTSTRAP_ADMIN_SUBJECTS"), ",") {
		if subject = strings.TrimSpace(subject); subject != "" {
			cfg.BootstrapAdminSubjects[subject] = struct{}{}
		}
	}

	if rawTTL := strings.TrimSpace(os.Getenv("OIDC_SESSION_MAX_AGE")); rawTTL != "" {
		ttl, parseErr := time.ParseDuration(rawTTL)
		if parseErr != nil || ttl < 5*time.Minute || ttl > 30*24*time.Hour {
			return cfg, fmt.Errorf("OIDC_SESSION_MAX_AGE must be a duration between 5m and 720h")
		}
		cfg.SessionAbsoluteTTL = ttl
	}

	return cfg, nil
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
