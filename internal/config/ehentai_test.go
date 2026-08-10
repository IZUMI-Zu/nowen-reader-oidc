package config

import (
	"strings"
	"testing"
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

func TestEHentaiConfigIsDisabledByDefault(t *testing.T) {
	clearEHentaiEnv(t)

	cfg, err := GetEHentaiConfig()
	if err != nil {
		t.Fatalf("GetEHentaiConfig() error = %v", err)
	}
	if cfg.Enabled {
		t.Fatal("E-Hentai must be disabled unless explicitly enabled")
	}
	if cfg.Site != EHentaiSitePublic {
		t.Fatalf("Site = %q, want %q", cfg.Site, EHentaiSitePublic)
	}
}

func TestEHentaiConfigAcceptsPublicAndAuthenticatedModes(t *testing.T) {
	t.Run("public without cookies", func(t *testing.T) {
		clearEHentaiEnv(t)
		t.Setenv("EHENTAI_ENABLED", "true")

		cfg, err := GetEHentaiConfig()
		if err != nil {
			t.Fatalf("GetEHentaiConfig() error = %v", err)
		}
		if !cfg.Enabled || cfg.HasLogin() {
			t.Fatalf("unexpected public configuration: %+v", cfg)
		}
	})

	t.Run("exhentai with cookies", func(t *testing.T) {
		clearEHentaiEnv(t)
		t.Setenv("EHENTAI_ENABLED", "true")
		t.Setenv("EHENTAI_SITE", "exhentai")
		t.Setenv("EHENTAI_IPB_MEMBER_ID", "1234567")
		t.Setenv("EHENTAI_IPB_PASS_HASH", "0123456789abcdef0123456789abcdef")
		t.Setenv("EHENTAI_STAR", "mystar")
		t.Setenv("EHENTAI_IGNEOUS", "igneous-value")
		t.Setenv("EHENTAI_PREFER_ORIGINAL_TITLE", "true")
		t.Setenv("EHENTAI_SEARCH_EXPUNGED", "true")

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
		{name: "unknown site", site: "example", wantError: "EHENTAI_SITE"},
		{name: "exhentai without login", site: "exhentai", wantError: "requires"},
		{name: "member only", memberID: "123", wantError: "together"},
		{name: "pass only", passHash: "0123456789abcdef0123456789abcdef", wantError: "together"},
		{name: "non-numeric member", memberID: "12a", passHash: "0123456789abcdef0123456789abcdef", wantError: "EHENTAI_IPB_MEMBER_ID"},
		{name: "cookie separator", memberID: "123", passHash: "abc;def", wantError: "EHENTAI_IPB_PASS_HASH"},
		{name: "cookie newline", memberID: "123", passHash: "abc\ndef", wantError: "EHENTAI_IPB_PASS_HASH"},
		{name: "oversized optional cookie", memberID: "123", passHash: "abcdef", star: strings.Repeat("a", maxEHentaiCookieLength+1), wantError: "EHENTAI_STAR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEHentaiEnv(t)
			t.Setenv("EHENTAI_ENABLED", "true")
			t.Setenv("EHENTAI_SITE", tt.site)
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

func TestEHentaiConfigRejectsMalformedBoolean(t *testing.T) {
	clearEHentaiEnv(t)
	t.Setenv("EHENTAI_ENABLED", "yes")
	if _, err := GetEHentaiConfig(); err == nil || !strings.Contains(err.Error(), "EHENTAI_ENABLED") {
		t.Fatalf("GetEHentaiConfig() error = %v, want boolean validation error", err)
	}
}
