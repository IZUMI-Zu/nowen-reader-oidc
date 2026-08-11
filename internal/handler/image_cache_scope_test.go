package handler

import (
	"image/color"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nowen-reader/nowen-reader/internal/archive"
	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/model"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

// 这些路由都过 checkComicAccess，按书库权限决定谁能看。发 public 缓存头意味着
// 中间的共享缓存/CDN 会把 A 用户的私有内容存下来再发给无权限的 B 用户。
func TestProtectedComicContentIsNotStoredBySharedCaches(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	router := setupTestRouter(t)
	if err := store.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	cookie := registerAndLogin(t, router)

	libraryDir := t.TempDir()
	library := &model.Library{
		ID: "cache-scope-lib", Name: "Cache scope", Type: "comic",
		RootPath: libraryDir, Enabled: true, DefaultAccess: "public", ScanEnabled: true,
	}
	if err := store.CreateLibrary(library); err != nil {
		t.Fatal(err)
	}
	const filename = "cache-scope.cbz"
	if err := os.WriteFile(filepath.Join(libraryDir, filename), createImageCBZ(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO "Comic" ("id", "filename", "title", "type", "libraryId", "relativePath", "pageCount", "coverImageUrl")
		VALUES ('cache-scope-comic', ?, 'Cache scope', 'comic', ?, ?, 1, 'https://covers.example/a.png')
	`, filename, library.ID, filename); err != nil {
		t.Fatal(err)
	}
	thumbDir := config.GetThumbnailsDir()
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(thumbDir, archive.ThumbnailCacheName("cache-scope-comic"))
	if err := os.WriteFile(cachePath, handlerSolidPNG(t, color.RGBA{G: 180, A: 255}), 0644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/api/comics/cache-scope-comic/thumbnail",
		"/api/comics/cache-scope-comic/page/0",
	} {
		response := performAuthedRequest(router, http.MethodGet, path, nil, cookie)
		if response.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, response.Code, response.Body.String())
		}
		cacheControl := response.Header().Get("Cache-Control")
		if !strings.HasPrefix(cacheControl, "private") {
			t.Fatalf("%s Cache-Control = %q, want a private cache", path, cacheControl)
		}
		if vary := response.Header().Get("Vary"); !strings.Contains(vary, "Cookie") {
			t.Fatalf("%s Vary = %q, want the session to be part of the cache key", path, vary)
		}
	}
}
