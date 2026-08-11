package config

import (
	"strings"
	"testing"
	"time"
)

func clearEHentaiEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"EHENTAI_ENABLED",
		"EHENTAI_SITE",
		"EHENTAI_IPB_MEMBER_ID",
		"EHENTAI_IPB_PASS_HASH",
		"EHENTAI_STAR",
		"EHENTAI_IGNEOUS",
		"EHENTAI_PREFER_ORIGINAL_TITLE",
		"EHENTAI_SEARCH_EXPUNGED",
	} {
		t.Setenv(key, "")
	}
}

func setEHentaiSiteSettings(t *testing.T, settings *EHentaiSettings) {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	siteConfigMu.Lock()
	siteConfigCache = &SiteConfig{EHentai: settings}
	siteConfigCacheTs = time.Now()
	siteConfigMu.Unlock()
	t.Cleanup(func() {
		siteConfigMu.Lock()
		siteConfigCache = nil
		siteConfigCacheTs = time.Time{}
		siteConfigMu.Unlock()
	})
}

func TestEHentaiConfigIsDisabledByDefault(t *testing.T) {
	clearEHentaiEnv(t)
	setEHentaiSiteSettings(t, nil)

	cfg, err := GetEHentaiConfig()
	if err != nil {
		t.Fatalf("GetEHentaiConfig() error = %v", err)
	}
	if cfg.Enabled {
		t.Fatal("E-Hentai must be disabled unless explicitly enabled in WebUI settings")
	}
	if cfg.Site != EHentaiSitePublic {
		t.Fatalf("Site = %q, want %q", cfg.Site, EHentaiSitePublic)
	}
}

func TestEHentaiConfigUsesWebUISettingsAndEnvironmentCredentials(t *testing.T) {
	t.Run("public without cookies", func(t *testing.T) {
		clearEHentaiEnv(t)
		setEHentaiSiteSettings(t, &EHentaiSettings{Enabled: true, Site: EHentaiSitePublic, ForcedLanguage: "english"})

		cfg, err := GetEHentaiConfig()
		if err != nil {
			t.Fatalf("GetEHentaiConfig() error = %v", err)
		}
		if !cfg.Enabled || cfg.HasLogin() || cfg.ForcedLanguage != "english" {
			t.Fatalf("unexpected public configuration: %+v", cfg)
		}
	})

	t.Run("exhentai with cookies", func(t *testing.T) {
		clearEHentaiEnv(t)
		setEHentaiSiteSettings(t, &EHentaiSettings{
			Enabled:             true,
			Site:                EHentaiSiteRestricted,
			PreferOriginalTitle: true,
			SearchExpunged:      true,
		})
		t.Setenv("EHENTAI_IPB_MEMBER_ID", "1234567")
		t.Setenv("EHENTAI_IPB_PASS_HASH", "0123456789abcdef0123456789abcdef")
		t.Setenv("EHENTAI_STAR", "mystar")
		t.Setenv("EHENTAI_IGNEOUS", "igneous-value")

		cfg, err := GetEHentaiConfig()
		if err != nil {
			t.Fatalf("GetEHentaiConfig() error = %v", err)
		}
		if cfg.Site != EHentaiSiteRestricted || !cfg.HasLogin() || !cfg.PreferOriginalTitle || !cfg.SearchExpunged {
			t.Fatalf("unexpected ExHentai configuration: %+v", cfg)
		}
	})
}

func TestEHentaiConfigRejectsIncompleteOrUnsafeCredentials(t *testing.T) {
	tests := []struct {
		name      string
		site      string
		memberID  string
		passHash  string
		star      string
		igneous   string
		wantError string
	}{
		{name: "exhentai without login", site: EHentaiSiteRestricted, wantError: "requires"},
		{name: "member only", site: EHentaiSitePublic, memberID: "123", wantError: "together"},
		{name: "pass only", site: EHentaiSitePublic, passHash: "0123456789abcdef0123456789abcdef", wantError: "together"},
		{name: "non-numeric member", site: EHentaiSitePublic, memberID: "12a", passHash: "0123456789abcdef0123456789abcdef", wantError: "EHENTAI_IPB_MEMBER_ID"},
		{name: "cookie separator", site: EHentaiSitePublic, memberID: "123", passHash: "abc;def", wantError: "EHENTAI_IPB_PASS_HASH"},
		{name: "cookie newline", site: EHentaiSitePublic, memberID: "123", passHash: "abc\ndef", wantError: "EHENTAI_IPB_PASS_HASH"},
		{name: "oversized optional cookie", site: EHentaiSitePublic, memberID: "123", passHash: "abcdef", star: strings.Repeat("a", maxEHentaiCookieLength+1), wantError: "EHENTAI_STAR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEHentaiEnv(t)
			setEHentaiSiteSettings(t, &EHentaiSettings{Enabled: true, Site: tt.site})
			t.Setenv("EHENTAI_IPB_MEMBER_ID", tt.memberID)
			t.Setenv("EHENTAI_IPB_PASS_HASH", tt.passHash)
			t.Setenv("EHENTAI_STAR", tt.star)
			t.Setenv("EHENTAI_IGNEOUS", tt.igneous)

			_, err := GetEHentaiConfig()
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("GetEHentaiConfig() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func TestValidateEHentaiSettingsRejectsInvalidWebUIValues(t *testing.T) {
	for _, test := range []struct {
		name     string
		settings EHentaiSettings
	}{
		{name: "unknown site", settings: EHentaiSettings{Site: "example"}},
		{name: "language expression", settings: EHentaiSettings{Site: EHentaiSitePublic, ForcedLanguage: `english OR uploader:someone`}},
		{name: "language operator", settings: EHentaiSettings{Site: EHentaiSitePublic, ForcedLanguage: "english OR uploader"}},
		{name: "non ASCII language", settings: EHentaiSettings{Site: EHentaiSitePublic, ForcedLanguage: "中文"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ValidateEHentaiSettings(test.settings); err == nil {
				t.Fatal("ValidateEHentaiSettings() accepted invalid settings")
			}
		})
	}
}

func TestLegacyNonSecretEnvironmentVariablesDoNotEnableSource(t *testing.T) {
	clearEHentaiEnv(t)
	setEHentaiSiteSettings(t, nil)
	t.Setenv("EHENTAI_ENABLED", "true")
	t.Setenv("EHENTAI_SITE", "exhentai")

	cfg, err := GetEHentaiConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || cfg.Site != EHentaiSitePublic {
		t.Fatalf("legacy non-secret environment variables unexpectedly changed config: %+v", cfg)
	}
}
