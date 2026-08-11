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

	"github.com/nowen-reader/nowen-reader/internal/archive"
	"github.com/nowen-reader/nowen-reader/internal/config"
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
	if policy := response.Header().Get("Referrer-Policy"); policy != "no-referrer" {
		t.Fatalf("non-EH redirect referrer policy = %q", policy)
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
