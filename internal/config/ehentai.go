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
}

// HasLogin reports whether both cookies required for an authenticated session
// are present.
func (c EHentaiConfig) HasLogin() bool {
	return c.IPBMemberID != "" && c.IPBPassHash != ""
}

// GetEHentaiConfig resolves and validates the optional EH/EX metadata source.
// The source is opt-in. Invalid or incomplete credentials make an enabled
// source unavailable instead of silently falling back to anonymous access.
func GetEHentaiConfig() (EHentaiConfig, error) {
	enabled, err := parseOptionalBool("EHENTAI_ENABLED", false)
	cfg := EHentaiConfig{
		Enabled: enabled,
		Site:    strings.ToLower(strings.TrimSpace(os.Getenv("EHENTAI_SITE"))),
	}
	if err != nil {
		return cfg, err
	}
	if cfg.Site == "" {
		cfg.Site = EHentaiSitePublic
	}
	if !enabled {
		return cfg, nil
	}

	switch cfg.Site {
	case EHentaiSitePublic, EHentaiSiteRestricted:
	default:
		return cfg, fmt.Errorf("EHENTAI_SITE must be ehentai or exhentai")
	}

	cfg.IPBMemberID = strings.TrimSpace(os.Getenv("EHENTAI_IPB_MEMBER_ID"))
	cfg.IPBPassHash = strings.TrimSpace(os.Getenv("EHENTAI_IPB_PASS_HASH"))
	cfg.Star = strings.TrimSpace(os.Getenv("EHENTAI_STAR"))
	cfg.Igneous = strings.TrimSpace(os.Getenv("EHENTAI_IGNEOUS"))

	if (cfg.IPBMemberID == "") != (cfg.IPBPassHash == "") {
		return cfg, fmt.Errorf("EHENTAI_IPB_MEMBER_ID and EHENTAI_IPB_PASS_HASH must be configured together")
	}
	if cfg.Site == EHentaiSiteRestricted && !cfg.HasLogin() {
		return cfg, fmt.Errorf("EHENTAI_SITE=exhentai requires EHENTAI_IPB_MEMBER_ID and EHENTAI_IPB_PASS_HASH")
	}
	if cfg.IPBMemberID != "" {
		for _, value := range cfg.IPBMemberID {
			if !unicode.IsDigit(value) || value > unicode.MaxASCII {
				return cfg, fmt.Errorf("EHENTAI_IPB_MEMBER_ID must contain only ASCII digits")
			}
		}
	}
	for key, value := range map[string]string{
		"EHENTAI_IPB_MEMBER_ID": cfg.IPBMemberID,
		"EHENTAI_IPB_PASS_HASH": cfg.IPBPassHash,
		"EHENTAI_STAR":          cfg.Star,
		"EHENTAI_IGNEOUS":       cfg.Igneous,
	} {
		if !validEHentaiCookieValue(value) {
			return cfg, fmt.Errorf("%s contains invalid cookie characters or is too long", key)
		}
	}

	cfg.PreferOriginalTitle, err = parseOptionalBool("EHENTAI_PREFER_ORIGINAL_TITLE", false)
	if err != nil {
		return cfg, err
	}
	cfg.SearchExpunged, err = parseOptionalBool("EHENTAI_SEARCH_EXPUNGED", false)
	if err != nil {
		return cfg, err
	}
	return cfg, nil
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
