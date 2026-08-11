package handler

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nowen-reader/nowen-reader/internal/archive"
	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/middleware"
	"github.com/nowen-reader/nowen-reader/internal/model"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

type metadataCoverRoundTripFunc func(*http.Request) (*http.Response, error)

func (f metadataCoverRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestEHGroupCoverCacheMissIsFetchedServerSide(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	router := setupTestRouter(t)
	if err := store.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	cookie := registerAndLogin(t, router)

	groupID64, err := store.CreateGroup("EH cover group")
	if err != nil {
		t.Fatal(err)
	}
	groupID := int(groupID64)
	coverURL := "https://ul.ehgt.org/g/fixture.png"
	if err := store.UpdateGroupMetadata(groupID, store.GroupMetadataUpdate{CoverURL: &coverURL}); err != nil {
		t.Fatal(err)
	}
	stubMetadataCoverTransport(t, handlerSolidPNG(t, color.RGBA{R: 255, A: 255}))

	response := performAuthedRequest(router, http.MethodGet,
		"/api/comics/group_"+strconv.Itoa(groupID)+"/thumbnail", nil, cookie)
	cachePath := filepath.Join(config.GetThumbnailsDir(), archive.GroupCoverCacheName(groupID))
	waitForMetadataCoverCache(t, cachePath)

	if response.Code != http.StatusOK {
		t.Fatalf("group cover status = %d, want server-side image response; location=%q",
			response.Code, response.Header().Get("Location"))
	}
	if location := response.Header().Get("Location"); location != "" {
		t.Fatalf("EH group cover exposed a browser redirect to %q", location)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "image/") {
		t.Fatalf("group cover content type = %q", contentType)
	}
}

func TestEHSeriesCoverCacheMissIsFetchedServerSide(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	router := setupTestRouter(t)
	if err := store.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	cookie := registerAndLogin(t, router)

	library := &model.Library{
		ID:            "eh-cover-series-library",
		Name:          "EH cover series",
		Type:          "comic",
		RootPath:      t.TempDir(),
		Enabled:       true,
		DefaultAccess: "public",
		ScanEnabled:   true,
	}
	if err := store.CreateLibrary(library); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO "Comic" ("id", "filename", "title", "type", "libraryId", "relativePath")
		VALUES ('eh-cover-series-comic', 'series.cbz', 'Series member', 'comic', ?, 'series.cbz');
		INSERT INTO "ComicSeries" ("id", "libraryId", "rootRelativePath", "title", "sortTitle", "coverComicId", "coverUrl")
		VALUES ('eh-cover-series', ?, '', 'EH series', 'eh series', 'eh-cover-series-comic', 'https://ul.ehgt.org/g/series.png');
		INSERT INTO "ComicSeriesItem" ("seriesId", "comicId", "sortIndex")
		VALUES ('eh-cover-series', 'eh-cover-series-comic', 0)
	`, library.ID, library.ID); err != nil {
		t.Fatal(err)
	}
	stubMetadataCoverTransport(t, handlerSolidPNG(t, color.RGBA{B: 255, A: 255}))

	response := performAuthedRequest(router, http.MethodGet,
		"/api/comics/series_eh-cover-series/thumbnail", nil, cookie)
	cachePath := filepath.Join(config.GetThumbnailsDir(), archive.SeriesCoverCacheName("eh-cover-series"))
	waitForMetadataCoverCache(t, cachePath)

	if response.Code != http.StatusOK {
		t.Fatalf("series cover status = %d, want server-side image response; location=%q",
			response.Code, response.Header().Get("Location"))
	}
	if location := response.Header().Get("Location"); location != "" {
		t.Fatalf("EH series cover exposed a browser redirect to %q", location)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "image/") {
		t.Fatalf("series cover content type = %q", contentType)
	}
}

func TestEHGroupCoverFetchFailureDoesNotRedirectBrowser(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	router := setupTestRouter(t)
	if err := store.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	cookie := registerAndLogin(t, router)

	groupID64, err := store.CreateGroup("Offline EH cover group")
	if err != nil {
		t.Fatal(err)
	}
	groupID := int(groupID64)
	coverURL := "https://ul.ehgt.org/g/offline.png"
	if err := store.UpdateGroupMetadata(groupID, store.GroupMetadataUpdate{CoverURL: &coverURL}); err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	http.DefaultTransport = metadataCoverRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("fixture transport offline")
	})
	t.Cleanup(func() { http.DefaultTransport = original })

	response := performAuthedRequest(router, http.MethodGet,
		"/api/comics/group_"+strconv.Itoa(groupID)+"/thumbnail", nil, cookie)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("group cover failure status = %d, body=%s", response.Code, response.Body.String())
	}
	if location := response.Header().Get("Location"); location != "" {
		t.Fatalf("failed server-side EH fetch redirected the browser to %q", location)
	}
}

func TestNonEHGroupCoverRedirectSuppressesReferrer(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	router := setupTestRouter(t)
	if err := store.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	cookie := registerAndLogin(t, router)

	groupID64, err := store.CreateGroup("External cover group")
	if err != nil {
		t.Fatal(err)
	}
	groupID := int(groupID64)
	coverURL := "https://covers.example/fixture.png"
	if err := store.UpdateGroupMetadata(groupID, store.GroupMetadataUpdate{CoverURL: &coverURL}); err != nil {
		t.Fatal(err)
	}
	requested := make(chan struct{})
	original := http.DefaultTransport
	http.DefaultTransport = metadataCoverRoundTripFunc(func(*http.Request) (*http.Response, error) {
		close(requested)
		return nil, errors.New("fixture transport offline")
	})
	t.Cleanup(func() { http.DefaultTransport = original })

	response := performAuthedRequest(router, http.MethodGet,
		"/api/comics/group_"+strconv.Itoa(groupID)+"/thumbnail", nil, cookie)
	select {
	case <-requested:
	case <-time.After(2 * time.Second):
		t.Fatal("background non-EH cover request did not use the stub transport")
	}
	if response.Code != http.StatusTemporaryRedirect || response.Header().Get("Location") != coverURL {
		t.Fatalf("non-EH redirect = %d %q", response.Code, response.Header().Get("Location"))
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "private, no-store" {
		t.Fatalf("non-EH redirect cache control = %q", cacheControl)
	}
	if policy := response.Header().Get("Referrer-Policy"); policy != "no-referrer" {
		t.Fatalf("non-EH redirect referrer policy = %q", policy)
	}
}

// 有书库权限、但合集成员一本都不在这些书库里，同样不能读封面。
func TestGroupCoverRejectsUserWhoseLibrariesHoldNoGroupMembers(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	router := setupTestRouter(t)
	if err := store.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	adminCookie := registerAndLogin(t, router)
	readerCookie := createCoverReader(t, router, adminCookie, "scoped-reader")

	for _, id := range []string{"cover-hidden-library", "cover-visible-library"} {
		if err := store.CreateLibrary(&model.Library{
			ID: id, Name: id, Type: "comic", RootPath: t.TempDir(),
			Enabled: true, DefaultAccess: "private", ScanEnabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DB().Exec(`
		INSERT INTO "Comic" ("id", "filename", "title", "type", "libraryId", "relativePath")
		VALUES ('cover-hidden-comic', 'hidden.cbz', 'Hidden member', 'comic', 'cover-hidden-library', 'hidden.cbz')
	`); err != nil {
		t.Fatal(err)
	}
	reader, err := store.GetUserByUsername("scoped-reader")
	if err != nil || reader == nil {
		t.Fatalf("load reader: %#v, %v", reader, err)
	}
	if err := store.SetUserLibraryAccess(reader.ID, []store.LibraryAccessReq{
		{LibraryID: "cover-visible-library", CanView: true},
	}); err != nil {
		t.Fatal(err)
	}

	groupID64, err := store.CreateGroupWithItems("Hidden cover group", "", []string{"cover-hidden-comic"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	groupID := int(groupID64)
	coverURL := "https://ul.ehgt.org/g/hidden.png"
	if err := store.UpdateGroupMetadata(groupID, store.GroupMetadataUpdate{CoverURL: &coverURL}); err != nil {
		t.Fatal(err)
	}
	thumbDir := config.GetThumbnailsDir()
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(thumbDir, archive.GroupCoverCacheName(groupID)),
		handlerSolidPNG(t, color.RGBA{G: 200, A: 255}), 0644); err != nil {
		t.Fatal(err)
	}

	path := "/api/comics/group_" + strconv.Itoa(groupID) + "/thumbnail"
	response := performAuthedRequest(router, http.MethodGet, path, nil, readerCookie)
	if response.Code != http.StatusForbidden {
		t.Fatalf("group cover status for a reader with no visible members = %d, want 403", response.Code)
	}
}

func createCoverReader(t *testing.T, router *gin.Engine, adminCookie, username string) string {
	t.Helper()
	createUser := performAuthedRequest(router, http.MethodPost, "/api/auth/users", map[string]any{
		"username": username, "password": "readerpass", "nickname": username, "role": "user",
	}, adminCookie)
	if createUser.Code != http.StatusOK {
		t.Fatalf("create reader: %d %s", createUser.Code, createUser.Body.String())
	}
	login := performRequest(router, http.MethodPost, "/api/auth/login", map[string]string{
		"username": username, "password": "readerpass",
	})
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == middleware.SessionCookie {
			return cookie.Value
		}
	}
	t.Fatalf("reader login failed: %d %s", login.Code, login.Body.String())
	return ""
}

func TestGroupCoverRejectsUserWithoutMemberLibraryAccess(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	router := setupTestRouter(t)
	if err := store.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	adminCookie := registerAndLogin(t, router)
	createUser := performAuthedRequest(router, http.MethodPost, "/api/auth/users", map[string]any{
		"username": "cover-reader", "password": "readerpass", "nickname": "Cover Reader", "role": "user",
	}, adminCookie)
	if createUser.Code != http.StatusOK {
		t.Fatalf("create reader: %d %s", createUser.Code, createUser.Body.String())
	}
	login := performRequest(router, http.MethodPost, "/api/auth/login", map[string]string{
		"username": "cover-reader", "password": "readerpass",
	})
	readerCookie := ""
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == middleware.SessionCookie {
			readerCookie = cookie.Value
		}
	}
	if readerCookie == "" {
		t.Fatalf("reader login failed: %d %s", login.Code, login.Body.String())
	}

	library := &model.Library{
		ID:            "private-group-cover-library",
		Name:          "Private covers",
		Type:          "comic",
		RootPath:      t.TempDir(),
		Enabled:       true,
		DefaultAccess: "private",
		ScanEnabled:   true,
	}
	if err := store.CreateLibrary(library); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO "Comic" ("id", "filename", "title", "type", "libraryId", "relativePath")
		VALUES ('private-group-cover-comic', 'private.cbz', 'Private member', 'comic', ?, 'private.cbz')
	`, library.ID); err != nil {
		t.Fatal(err)
	}
	groupID64, err := store.CreateGroupWithItems("Private cover group", "", []string{"private-group-cover-comic"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	groupID := int(groupID64)
	coverURL := "https://ul.ehgt.org/g/private.png"
	if err := store.UpdateGroupMetadata(groupID, store.GroupMetadataUpdate{CoverURL: &coverURL}); err != nil {
		t.Fatal(err)
	}
	thumbDir := config.GetThumbnailsDir()
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(thumbDir, archive.GroupCoverCacheName(groupID))
	if err := os.WriteFile(cachePath, handlerSolidPNG(t, color.RGBA{R: 120, B: 120, A: 255}), 0644); err != nil {
		t.Fatal(err)
	}
	path := "/api/comics/group_" + strconv.Itoa(groupID) + "/thumbnail"
	adminResponse := performAuthedRequest(router, http.MethodGet, path, nil, adminCookie)
	if adminResponse.Code != http.StatusOK {
		t.Fatalf("admin group cover status = %d", adminResponse.Code)
	}
	if cacheControl := adminResponse.Header().Get("Cache-Control"); !strings.HasPrefix(cacheControl, "private,") {
		t.Fatalf("admin group cover cache control = %q", cacheControl)
	}
	readerResponse := performAuthedRequest(router, http.MethodGet, path, nil, readerCookie)
	if readerResponse.Code != http.StatusForbidden {
		t.Fatalf("unauthorized reader group cover status = %d, want 403", readerResponse.Code)
	}
	reader, err := store.GetUserByUsername("cover-reader")
	if err != nil || reader == nil {
		t.Fatalf("load reader: %#v, %v", reader, err)
	}
	if err := store.SetUserLibraryAccess(reader.ID, []store.LibraryAccessReq{{LibraryID: library.ID, CanView: true}}); err != nil {
		t.Fatal(err)
	}
	allowedResponse := performAuthedRequest(router, http.MethodGet, path, nil, readerCookie)
	if allowedResponse.Code != http.StatusOK {
		t.Fatalf("authorized reader group cover status = %d", allowedResponse.Code)
	}
	if cacheControl := allowedResponse.Header().Get("Cache-Control"); !strings.HasPrefix(cacheControl, "private,") {
		t.Fatalf("authorized reader group cover cache control = %q", cacheControl)
	}
}

func stubMetadataCoverTransport(t *testing.T, imageData []byte) {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = metadataCoverRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(bytes.NewReader(imageData)),
			Request:    req,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
}

func handlerSolidPNG(t *testing.T, fill color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, fill)
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func waitForMetadataCoverCache(t *testing.T, cachePath string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(cachePath); err == nil && info.Size() > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("metadata cover cache was not written: %s", cachePath)
}
