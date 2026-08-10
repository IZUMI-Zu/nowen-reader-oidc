package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/nowen-reader/nowen-reader/internal/config"
)

func TestEHentaiSettingsAreAdminOnlyAndNeverReturnSecrets(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("EHENTAI_IPB_MEMBER_ID", "1234567")
	t.Setenv("EHENTAI_IPB_PASS_HASH", "fixture-secret-pass-hash")
	t.Setenv("EHENTAI_STAR", "fixture-secret-star")
	t.Setenv("EHENTAI_IGNEOUS", "fixture-secret-igneous")
	if err := config.SaveSiteConfig(&config.SiteConfig{}); err != nil {
		t.Fatal(err)
	}

	r := setupTestRouter(t)
	if response := performRequest(r, http.MethodGet, "/api/metadata/ehentai/settings", nil); response.Code == http.StatusOK {
		t.Fatal("EH/EX settings endpoint must require administrator authentication")
	}

	cookie := registerAndLogin(t, r)
	response := performAuthedRequest(r, http.MethodGet, "/api/metadata/ehentai/settings", nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("GET settings status = %d: %s", response.Code, response.Body.String())
	}
	for _, secret := range []string{"fixture-secret-pass-hash", "fixture-secret-star", "fixture-secret-igneous"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("settings response leaked secret %q", secret)
		}
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if configured, _ := body["credentialsConfigured"].(bool); !configured {
		t.Fatalf("masked credential status missing: %s", response.Body.String())
	}
	if forcedLanguage, ok := body["forcedLanguage"].(string); !ok || forcedLanguage != "" {
		t.Fatalf("settings response omitted the empty forcedLanguage field required by WebUI: %s", response.Body.String())
	}

	response = performAuthedRequest(r, http.MethodPut, "/api/metadata/ehentai/settings", map[string]any{
		"enabled":             true,
		"site":                "exhentai",
		"preferOriginalTitle": true,
		"searchExpunged":      true,
		"forcedLanguage":      "english",
	}, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT settings status = %d: %s", response.Code, response.Body.String())
	}

	data, err := os.ReadFile(config.SiteConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture-secret-pass-hash", "fixture-secret-star", "fixture-secret-igneous"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("site-config.json leaked secret %q", secret)
		}
	}
	if !strings.Contains(string(data), `"ehentai"`) || !strings.Contains(string(data), `"forcedLanguage": "english"`) {
		t.Fatalf("non-sensitive settings were not persisted: %s", data)
	}

	publicResponse := performRequest(r, http.MethodGet, "/api/site-settings", nil)
	if strings.Contains(publicResponse.Body.String(), "credentialsConfigured") || strings.Contains(publicResponse.Body.String(), "ehentai") {
		t.Fatalf("public site settings exposed EH/EX admin configuration: %s", publicResponse.Body.String())
	}
}

func TestEHentaiSettingsRejectEnabledExHentaiWithoutCredentials(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	for _, key := range []string{"EHENTAI_IPB_MEMBER_ID", "EHENTAI_IPB_PASS_HASH", "EHENTAI_STAR", "EHENTAI_IGNEOUS"} {
		t.Setenv(key, "")
	}
	if err := config.SaveSiteConfig(&config.SiteConfig{}); err != nil {
		t.Fatal(err)
	}
	r := setupTestRouter(t)
	cookie := registerAndLogin(t, r)

	response := performAuthedRequest(r, http.MethodPut, "/api/metadata/ehentai/settings", map[string]any{
		"enabled": true,
		"site":    "exhentai",
	}, cookie)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("PUT enabled ExHentai without credentials status = %d: %s", response.Code, response.Body.String())
	}
	if config.GetSiteConfig().ResolvedEHentaiSettings().Enabled {
		t.Fatal("invalid ExHentai configuration was persisted")
	}
}
