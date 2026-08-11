package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nowen-reader/nowen-reader/internal/archive"
	"github.com/nowen-reader/nowen-reader/internal/config"
)

func TestGroupCoverURLChangesWhenStoredSourceChangesBeforeCacheReplacement(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	setupTestDB(t)

	groupID64, err := CreateGroup("Versioned cover")
	if err != nil {
		t.Fatal(err)
	}
	groupID := int(groupID64)
	coverA := "https://ul.ehgt.org/old-cover.png"
	if err := UpdateGroupMetadata(groupID, GroupMetadataUpdate{CoverURL: &coverA}); err != nil {
		t.Fatal(err)
	}

	thumbDir := config.GetThumbnailsDir()
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(thumbDir, archive.GroupCoverCacheName(groupID))
	if err := os.WriteFile(cachePath, []byte("old cached cover"), 0644); err != nil {
		t.Fatal(err)
	}

	before, err := GetGroupByID(groupID)
	if err != nil || before == nil {
		t.Fatalf("load old group cover: %#v, %v", before, err)
	}
	coverB := "https://ul.ehgt.org/new-cover.png"
	if err := UpdateGroupMetadata(groupID, GroupMetadataUpdate{CoverURL: &coverB}); err != nil {
		t.Fatal(err)
	}
	after, err := GetGroupByID(groupID)
	if err != nil || after == nil {
		t.Fatalf("load new group cover: %#v, %v", after, err)
	}

	if before.CoverURL == after.CoverURL {
		t.Fatalf("cover URL stayed %q after stored source changed while old cache remained", after.CoverURL)
	}
	if strings.Contains(after.CoverURL, coverA) || strings.Contains(after.CoverURL, coverB) {
		t.Fatalf("cover URL leaked a stored remote source: %q", after.CoverURL)
	}
}
