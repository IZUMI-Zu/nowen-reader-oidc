package service

import (
	"bytes"
	"image/color"
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

// 目录作品封面和合集封面走同一套发布保护：等待者比较 URL、发布前复查数据库。
// 这个测试让旧封面 A 的下载卡在传输层，期间把封面换成 B，然后释放 A。
func TestDownloadSeriesCoverDoesNotLetOlderRequestOverwriteNewerURL(t *testing.T) {
	const seriesID = "series-cover-race"
	setupSeriesCoverTest(t, seriesID)
	coverA := "https://ul.ehgt.org/a.png"
	coverB := "https://ul.ehgt.org/b.png"
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
		DownloadSeriesCover(seriesID, coverA, config.EHentaiSitePublic)
		close(oldDone)
	}()
	select {
	case <-aStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("old series cover request did not start")
	}

	newDone := make(chan struct{})
	go func() {
		DownloadSeriesCover(seriesID, coverB, config.EHentaiSitePublic)
		close(newDone)
	}()
	waitSeriesStoredCover(t, seriesID, coverB)
	close(releaseA)
	waitGroupCoverTestTask(t, "old series cover request", oldDone)
	waitGroupCoverTestTask(t, "new series cover request", newDone)

	cachePath := filepath.Join(config.GetThumbnailsDir(), archive.SeriesCoverCacheName(seriesID))
	got, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read series cover cache: %v", err)
	}
	want, _, err := archive.ResizeImageToWebP(imageB, config.GetThumbnailWidth(), config.GetThumbnailHeight(), 85)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("series cover cache does not hold the newest cover")
	}
}

// 发布前复查：旧封面的下载在数据库已经换成新封面之后返回时，不允许落盘。
func TestDownloadSeriesCoverSkipsPublishAfterStoredURLChanged(t *testing.T) {
	const seriesID = "series-cover-stale"
	setupSeriesCoverTest(t, seriesID)
	coverA := "https://ul.ehgt.org/a.png"
	coverB := "https://ul.ehgt.org/b.png"
	imageA := solidPNG(t, color.RGBA{R: 255, A: 255})

	aStarted := make(chan struct{})
	releaseA := make(chan struct{})
	var startOnce sync.Once
	stubGroupCoverTransport(t, func(req *http.Request) (*http.Response, error) {
		startOnce.Do(func() { close(aStarted) })
		<-releaseA
		return imageResponse(req, imageA), nil
	})

	oldDone := make(chan struct{})
	go func() {
		DownloadSeriesCover(seriesID, coverA, config.EHentaiSitePublic)
		close(oldDone)
	}()
	select {
	case <-aStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("old series cover request did not start")
	}

	if err := store.UpdateSeriesMetadata(seriesID, store.SeriesMetadataUpdate{CoverURL: &coverB}); err != nil {
		t.Fatal(err)
	}
	close(releaseA)
	waitGroupCoverTestTask(t, "old series cover request", oldDone)

	cachePath := filepath.Join(config.GetThumbnailsDir(), archive.SeriesCoverCacheName(seriesID))
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("stale in-flight request published a series cover after the URL changed: %v", err)
	}
}

func setupSeriesCoverTest(t *testing.T, seriesID string) {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	setupTestDB(t)
	if err := store.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	db := store.DB()
	if _, err := db.Exec(`
		INSERT INTO "Library" ("id", "name", "rootPath") VALUES ('series-cover-lib', 'Cover library', '/tmp/series-cover')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO "ComicSeries" ("id", "libraryId", "rootRelativePath", "title")
		VALUES (?, 'series-cover-lib', ?, 'Cover series')
	`, seriesID, seriesID); err != nil {
		t.Fatal(err)
	}
}

func waitSeriesStoredCover(t *testing.T, seriesID, coverURL string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stored, err := store.GetSeriesStoredCoverURL(seriesID)
		if err == nil && stored == coverURL {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("series %s cover URL never became %q", seriesID, coverURL)
}
