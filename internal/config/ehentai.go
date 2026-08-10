package config

import (
	"fmt"
	"os"
	"strings"
	"unicode"
)

const (
	EHentaiSitePublic      = "ehentai"
	EHentaiSiteRestricted  = "exhentai"
	maxEHentaiCookieLength = 512
)

var ehentaiLanguages = map[string]struct{}{
	"chinese":  {},
	"english":  {},
	"french":   {},
	"german":   {},
	"japanese": {},
	"korean":   {},
	"spanish":  {},
}

// EHentaiSettings contains the non-sensitive EH/EX options persisted in
// site-config.json and managed from the administrator WebUI. Login cookies are
// deliberately excluded from this type.
type EHentaiSettings struct {
	Enabled             bool   `json:"enabled"`
	Site                string `json:"site"`
	PreferOriginalTitle bool   `json:"preferOriginalTitle"`
	SearchExpunged      bool   `json:"searchExpunged"`
	ForcedLanguage      string `json:"forcedLanguage,omitempty"`
}

// EHentaiConfig contains server-side configuration for the optional
// E-Hentai/ExHentai metadata source. Account cookies are intentionally read
// only from environment variables and must never be persisted in SiteConfig.
type EHentaiConfig struct {
	Enabled             bool
	Site                string
	IPBMemberID         string
	IPBPassHash         string
	Star                string
	Igneous             string
	PreferOriginalTitle bool
	SearchExpunged      bool
	ForcedLanguage      string
}

// HasLogin reports whether both cookies required for an authenticated session
// are present.
func (c EHentaiConfig) HasLogin() bool {
	return c.IPBMemberID != "" && c.IPBPassHash != ""
}

// ResolvedEHentaiSettings returns a validated-value-shaped copy with defaults.
// Validation is kept separate so the settings handler can reject invalid input
// before it reaches site-config.json.
func (c SiteConfig) ResolvedEHentaiSettings() EHentaiSettings {
	settings := EHentaiSettings{Site: EHentaiSitePublic}
	if c.EHentai != nil {
		settings = *c.EHentai
		settings.Site = strings.ToLower(strings.TrimSpace(settings.Site))
		settings.ForcedLanguage = strings.TrimSpace(settings.ForcedLanguage)
		if settings.Site == "" {
			settings.Site = EHentaiSitePublic
		}
	}
	return settings
}

// ValidateEHentaiSettings validates values that can be written from WebUI.
func ValidateEHentaiSettings(settings EHentaiSettings) (EHentaiSettings, error) {
	settings.Site = strings.ToLower(strings.TrimSpace(settings.Site))
	settings.ForcedLanguage = strings.ToLower(strings.TrimSpace(settings.ForcedLanguage))
	if settings.Site == "" {
		settings.Site = EHentaiSitePublic
	}
	switch settings.Site {
	case EHentaiSitePublic, EHentaiSiteRestricted:
	default:
		return settings, fmt.Errorf("site must be ehentai or exhentai")
	}
	if settings.ForcedLanguage != "" {
		if _, ok := ehentaiLanguages[settings.ForcedLanguage]; !ok {
			return settings, fmt.Errorf("forcedLanguage is not supported")
		}
	}
	return settings, nil
}

// GetEHentaiConfig resolves WebUI-managed options and environment-only account
// credentials. Invalid or incomplete credentials make an enabled source
// unavailable instead of silently falling back to anonymous access.
func GetEHentaiConfig() (EHentaiConfig, error) {
	settings := GetSiteConfig().ResolvedEHentaiSettings()
	return ResolveEHentaiConfig(settings)
}

// ResolveEHentaiConfig validates settings against credentials in the current
// process environment. It is also used by the admin settings endpoint before
// persisting an enabled configuration.
func ResolveEHentaiConfig(settings EHentaiSettings) (EHentaiConfig, error) {
	settings, err := ValidateEHentaiSettings(settings)
	cfg := EHentaiConfig{
		Enabled:             settings.Enabled,
		Site:                settings.Site,
		PreferOriginalTitle: settings.PreferOriginalTitle,
		SearchExpunged:      settings.SearchExpunged,
		ForcedLanguage:      settings.ForcedLanguage,
	}
	if err != nil {
		return cfg, err
	}
	if !cfg.Enabled {
		return cfg, nil
	}

	credentials, err := loadEHentaiCredentials()
	if err != nil {
		return cfg, err
	}
	cfg.IPBMemberID = credentials.IPBMemberID
	cfg.IPBPassHash = credentials.IPBPassHash
	cfg.Star = credentials.Star
	cfg.Igneous = credentials.Igneous
	if cfg.Site == EHentaiSiteRestricted && !cfg.HasLogin() {
		return cfg, fmt.Errorf("ExHentai requires EHENTAI_IPB_MEMBER_ID and EHENTAI_IPB_PASS_HASH")
	}
	return cfg, nil
}

// EHentaiCredentialsConfigured reports credential presence without returning
// any secret. Invalid or incomplete values are reported as an error.
func EHentaiCredentialsConfigured() (bool, error) {
	credentials, err := loadEHentaiCredentials()
	if err != nil {
		return false, err
	}
	return credentials.IPBMemberID != "" && credentials.IPBPassHash != "", nil
}

type ehentaiCredentials struct {
	IPBMemberID string
	IPBPassHash string
	Star        string
	Igneous     string
}

func loadEHentaiCredentials() (ehentaiCredentials, error) {
	credentials := ehentaiCredentials{
		IPBMemberID: strings.TrimSpace(os.Getenv("EHENTAI_IPB_MEMBER_ID")),
		IPBPassHash: strings.TrimSpace(os.Getenv("EHENTAI_IPB_PASS_HASH")),
		Star:        strings.TrimSpace(os.Getenv("EHENTAI_STAR")),
		Igneous:     strings.TrimSpace(os.Getenv("EHENTAI_IGNEOUS")),
	}
	if (credentials.IPBMemberID == "") != (credentials.IPBPassHash == "") {
		return credentials, fmt.Errorf("EHENTAI_IPB_MEMBER_ID and EHENTAI_IPB_PASS_HASH must be configured together")
	}
	if credentials.IPBMemberID != "" {
		for _, value := range credentials.IPBMemberID {
			if !unicode.IsDigit(value) || value > unicode.MaxASCII {
				return credentials, fmt.Errorf("EHENTAI_IPB_MEMBER_ID must contain only ASCII digits")
			}
		}
	}
	for key, value := range map[string]string{
		"EHENTAI_IPB_MEMBER_ID": credentials.IPBMemberID,
		"EHENTAI_IPB_PASS_HASH": credentials.IPBPassHash,
		"EHENTAI_STAR":          credentials.Star,
		"EHENTAI_IGNEOUS":       credentials.Igneous,
	} {
		if !validEHentaiCookieValue(value) {
			return credentials, fmt.Errorf("%s contains invalid cookie characters or is too long", key)
		}
	}
	return credentials, nil
}

func validEHentaiCookieValue(value string) bool {
	if len(value) > maxEHentaiCookieLength {
		return false
	}
	for _, char := range value {
		if char < 0x21 || char > 0x7e || char == ';' || char == ',' || char == '"' || char == '\\' {
			return false
		}
	}
	return true
}
