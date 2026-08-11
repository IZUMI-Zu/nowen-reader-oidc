package service

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nowen-reader/nowen-reader/internal/archive"
	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

func TestDownloadGroupCoverReplacesExistingCache(t *testing.T) {
	setupGroupCoverTest(t, 901, "https://ul.ehgt.org/a.png")
	images := map[string][]byte{
		"/a.png": solidPNG(t, color.RGBA{R: 255, A: 255}),
		"/b.png": solidPNG(t, color.RGBA{B: 255, A: 255}),
	}
	stubGroupCoverTransport(t, func(req *http.Request) (*http.Response, error) {
		return imageResponse(req, images[req.URL.Path]), nil
	})

	DownloadGroupCover(901, "https://ul.ehgt.org/a.png", config.EHentaiSitePublic)
	cachePath := filepath.Join(config.GetThumbnailsDir(), archive.GroupCoverCacheName(901))
	first, err := os.ReadFile(cachePath)
	if err != nil || len(first) == 0 {
		t.Fatalf("read first cover cache: %v", err)
	}

	coverB := "https://ul.ehgt.org/b.png"
	if err := store.UpdateGroupMetadata(901, store.GroupMetadataUpdate{CoverURL: &coverB}); err != nil {
		t.Fatal(err)
	}
	DownloadGroupCover(901, coverB, config.EHentaiSitePublic)

	got, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := archive.ResizeImageToWebP(images["/b.png"], config.GetThumbnailWidth(), config.GetThumbnailHeight(), 85)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(got, first) || !bytes.Equal(got, want) {
		t.Fatal("group cover cache did not change from A to B")
	}
}

func TestDownloadGroupCoverNormalizesStoredHTTPURLConditionally(t *testing.T) {
	const coverHTTP = "http://covers.example/a.png"
	const coverHTTPS = "https://covers.example/a.png"
	setupGroupCoverTest(t, 903, coverHTTP)
	imageA := solidPNG(t, color.RGBA{G: 255, A: 255})
	stubGroupCoverTransport(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != coverHTTPS {
			t.Fatalf("cover request URL = %q, want %q", req.URL, coverHTTPS)
		}
		return imageResponse(req, imageA), nil
	})

	DownloadGroupCover(903, coverHTTP)
	storedURL, err := store.GetGroupStoredCoverURL(903)
	if err != nil {
		t.Fatal(err)
	}
	if storedURL != coverHTTPS {
		t.Fatalf("stored cover URL = %q, want %q", storedURL, coverHTTPS)
	}
}

func TestDownloadGroupCoverDoesNotLetOlderRequestOverwriteNewerURL(t *testing.T) {
	setupGroupCoverTest(t, 902, "https://ul.ehgt.org/a.png")
	imageA := solidPNG(t, color.RGBA{R: 255, A: 255})
	imageB := solidPNG(t, color.RGBA{B: 255, A: 255})
	aStarted := make(chan struct{})
	releaseA := make(chan struct{})
	var startOnce sync.Once
	stubGroupCoverTransport(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/a.png" {
			startOnce.Do(func() { close(aStarted) })
			<-releaseA
			return imageResponse(req, imageA), nil
		}
		return imageResponse(req, imageB), nil
	})

	oldDone := make(chan struct{})
	go func() {
		DownloadGroupCover(902, "https://ul.ehgt.org/a.png", config.EHentaiSitePublic)
		close(oldDone)
	}()
	select {
	case <-aStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("old cover request did not start")
	}

	coverB := "https://ul.ehgt.org/b.png"
	if err := store.UpdateGroupMetadata(902, store.GroupMetadataUpdate{CoverURL: &coverB}); err != nil {
		t.Fatal(err)
	}
	close(releaseA)
	select {
	case <-oldDone:
	case <-time.After(2 * time.Second):
		t.Fatal("old cover request did not finish")
	}

	cachePath := filepath.Join(config.GetThumbnailsDir(), archive.GroupCoverCacheName(902))
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("stale in-flight request wrote a cache after the URL changed: %v", err)
	}
	DownloadGroupCover(902, coverB, config.EHentaiSitePublic)

	got, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := archive.ResizeImageToWebP(imageB, config.GetThumbnailWidth(), config.GetThumbnailHeight(), 85)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("older in-flight cover request overwrote the newer cover")
	}
}

func TestGroupCoverClearWaitsForCheckedRemotePublish(t *testing.T) {
	setupGroupCoverTest(t, 904, "https://ul.ehgt.org/a.png")
	imageA := solidPNG(t, color.RGBA{R: 255, A: 255})
	stubGroupCoverTransport(t, func(req *http.Request) (*http.Response, error) {
		return imageResponse(req, imageA), nil
	})
	checked := make(chan struct{})
	release := make(chan struct{})
	groupCoverBeforePublish = func(groupID int) {
		if groupID == 904 {
			close(checked)
			<-release
		}
	}
	t.Cleanup(func() { groupCoverBeforePublish = nil })

	oldDone := make(chan struct{})
	go func() {
		DownloadGroupCover(904, "https://ul.ehgt.org/a.png", config.EHentaiSitePublic)
		close(oldDone)
	}()
	select {
	case <-checked:
	case <-time.After(2 * time.Second):
		t.Fatal("old cover did not reach the publication boundary")
	}

	empty := ""
	if err := store.UpdateGroupMetadata(904, store.GroupMetadataUpdate{CoverURL: &empty}); err != nil {
		t.Fatal(err)
	}
	clearStarted := make(chan struct{})
	clearDone := make(chan struct{})
	go func() {
		close(clearStarted)
		ClearGroupCoverCache(904)
		close(clearDone)
	}()
	<-clearStarted
	close(release)
	waitGroupCoverTestTask(t, "old publish", oldDone)
	waitGroupCoverTestTask(t, "clear", clearDone)

	cachePath := filepath.Join(config.GetThumbnailsDir(), archive.GroupCoverCacheName(904))
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("old request recreated cache after cover clear: %v", err)
	}
}

func TestGroupCoverDataURLWinsAfterCheckedRemotePublish(t *testing.T) {
	setupGroupCoverTest(t, 905, "https://ul.ehgt.org/a.png")
	imageA := solidPNG(t, color.RGBA{R: 255, A: 255})
	imageB := solidPNG(t, color.RGBA{B: 255, A: 255})
	stubGroupCoverTransport(t, func(req *http.Request) (*http.Response, error) {
		return imageResponse(req, imageA), nil
	})
	checked := make(chan struct{})
	release := make(chan struct{})
	groupCoverBeforePublish = func(groupID int) {
		if groupID == 905 {
			close(checked)
			<-release
		}
	}
	t.Cleanup(func() { groupCoverBeforePublish = nil })

	oldDone := make(chan struct{})
	go func() {
		DownloadGroupCover(905, "https://ul.ehgt.org/a.png", config.EHentaiSitePublic)
		close(oldDone)
	}()
	select {
	case <-checked:
	case <-time.After(2 * time.Second):
		t.Fatal("old cover did not reach the publication boundary")
	}

	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageB)
	if err := store.UpdateGroupMetadata(905, store.GroupMetadataUpdate{CoverURL: &dataURL}); err != nil {
		t.Fatal(err)
	}
	dataDone := make(chan error, 1)
	go func() { dataDone <- CacheGroupCoverDataURL(905, dataURL) }()
	select {
	case err := <-dataDone:
		t.Fatalf("data URL publication bypassed the remote publication lock: %v", err)
	case <-time.After(50 * time.Millisecond):
		// The checked remote publication still owns the shared lock.
	}
	close(release)
	waitGroupCoverTestTask(t, "old publish", oldDone)
	select {
	case err := <-dataDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("data URL cover cache did not finish")
	}

	cachePath := filepath.Join(config.GetThumbnailsDir(), archive.GroupCoverCacheName(905))
	got, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := archive.ResizeImageToWebP(imageB, config.GetThumbnailWidth(), config.GetThumbnailHeight(), 85)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("old remote cover overwrote the newer data URL cache")
	}
}

func waitGroupCoverTestTask(t *testing.T, name string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not finish", name)
	}
}

func setupGroupCoverTest(t *testing.T, groupID int, coverURL string) {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	setupTestDB(t)
	if _, err := store.DB().Exec(`
		INSERT INTO "ComicGroup" ("id", "name", "coverUrl") VALUES (?, 'Fixture group', ?)
	`, groupID, coverURL); err != nil {
		t.Fatal(err)
	}
}

func stubGroupCoverTransport(t *testing.T, roundTrip func(*http.Request) (*http.Response, error)) {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = ehRoundTripFunc(roundTrip)
	t.Cleanup(func() { http.DefaultTransport = original })
}

func imageResponse(req *http.Request, data []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"image/png"}},
		Body:       io.NopCloser(bytes.NewReader(data)),
		Request:    req,
	}
}

func solidPNG(t *testing.T, fill color.Color) []byte {
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
