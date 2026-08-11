package store

import (
	"database/sql"
	"testing"
)

func TestEHentaiNamespacedTagsSyncToGroupMembers(t *testing.T) {
	setupTestDB(t)
	insertTagSyncComics(t, "group-tag-volume-1", "group-tag-volume-2")

	groupID, err := CreateGroupWithItems(
		"EH tag group",
		"",
		[]string{"group-tag-volume-1", "group-tag-volume-2"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	parentTags := ehNamespacedTagFixture()
	if err := SetGroupTags(int(groupID), parentTags); err != nil {
		t.Fatal(err)
	}
	memberSources := seedMemberTags(t, "group-tag-volume-1", "group-tag-volume-2")

	total, synced, tagsCount, err := SyncGroupTagsToVolumes(int(groupID))
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || synced != 2 || tagsCount != len(parentTags)-1 {
		t.Fatalf("sync result = total:%d synced:%d tags:%d", total, synced, tagsCount)
	}
	assertSyncedEHNamespacedTags(t, parentTags, memberSources, "group-tag-volume-1", "group-tag-volume-2")
}

func TestEHentaiNamespacedTagsOverrideGroupMembers(t *testing.T) {
	setupTestDB(t)
	insertTagSyncComics(t, "group-override-volume-1", "group-override-volume-2")
	groupID, err := CreateGroupWithItems(
		"EH tag override group",
		"",
		[]string{"group-override-volume-1", "group-override-volume-2"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	parentTags := ehNamespacedTagFixture()
	if err := SetGroupTags(int(groupID), parentTags); err != nil {
		t.Fatal(err)
	}
	memberSources := seedMemberTags(t, "group-override-volume-1", "group-override-volume-2")

	total, synced, tagsCount, err := OverrideGroupTagsToVolumes(int(groupID))
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || synced != 2 || tagsCount != len(parentTags)-1 {
		t.Fatalf("override result = total:%d synced:%d tags:%d", total, synced, tagsCount)
	}
	assertOverriddenEHNamespacedTags(t, parentTags, memberSources, "group-override-volume-1", "group-override-volume-2")
}

func TestEHentaiNamespacedTagsSyncToSeriesMembers(t *testing.T) {
	setupTestDB(t)
	if _, err := DB().Exec(`
		INSERT INTO "Library" ("id", "name", "rootPath")
		VALUES ('eh-tag-library', 'EH tag library', '/tmp/eh-tag-library')
	`); err != nil {
		t.Fatal(err)
	}
	insertTagSyncComics(t, "series-tag-volume-1", "series-tag-volume-2")
	if _, err := DB().Exec(`
		UPDATE "Comic" SET "libraryId" = 'eh-tag-library'
		WHERE "id" IN ('series-tag-volume-1', 'series-tag-volume-2')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`
		INSERT INTO "ComicSeries" ("id", "libraryId", "rootRelativePath", "title", "sortTitle")
		VALUES ('eh-tag-series', 'eh-tag-library', 'work', 'EH tag series', 'EH tag series')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`
		INSERT INTO "ComicSeriesItem" ("seriesId", "comicId", "sortIndex") VALUES
			('eh-tag-series', 'series-tag-volume-1', 0),
			('eh-tag-series', 'series-tag-volume-2', 1)
	`); err != nil {
		t.Fatal(err)
	}

	parentTags := ehNamespacedTagFixture()
	if err := SetSeriesTags("eh-tag-series", parentTags); err != nil {
		t.Fatal(err)
	}
	memberSources := seedMemberTags(t, "series-tag-volume-1", "series-tag-volume-2")

	total, synced, tagsCount, err := SyncSeriesTagsToItems("eh-tag-series")
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || synced != 2 || tagsCount != len(parentTags)-1 {
		t.Fatalf("sync result = total:%d synced:%d tags:%d", total, synced, tagsCount)
	}
	assertSyncedEHNamespacedTags(t, parentTags, memberSources, "series-tag-volume-1", "series-tag-volume-2")
}

func TestReplacingComicEHSourcePreservesGroupTagRow(t *testing.T) {
	setupTestDB(t)
	if _, err := DB().Exec(`
		INSERT INTO "Comic" ("id", "filename", "title")
		VALUES ('group-source-owner-comic', 'group-source-owner.cbz', 'Group source owner')
	`); err != nil {
		t.Fatal(err)
	}
	groupID, err := CreateGroup("Source owner group")
	if err != nil {
		t.Fatal(err)
	}

	oldSource := "source:https://e-hentai.org/g/21/aaaaaaaaaa"
	newSource := "source:https://exhentai.org/g/22/bbbbbbbbbb"
	if err := AddTagsToComic("group-source-owner-comic", []string{oldSource}); err != nil {
		t.Fatal(err)
	}
	if err := SetGroupTags(int(groupID), []string{oldSource}); err != nil {
		t.Fatal(err)
	}
	if err := AddTagsToComicReplacingMatching(
		"group-source-owner-comic",
		[]string{newSource},
		IsEHentaiGallerySourceTag,
	); err != nil {
		t.Fatal(err)
	}

	comic, err := GetComicByID("group-source-owner-comic")
	if err != nil || comic == nil {
		t.Fatalf("load comic: %v", err)
	}
	assertTagNamesExactly(t, []string{newSource}, comicTagNames(comic))
	groupTags, err := GetGroupTags(int(groupID))
	if err != nil {
		t.Fatal(err)
	}
	assertTagNamesExactly(t, []string{oldSource}, storeTagNames(groupTags))
}

func TestReplacingComicEHSourcePreservesSeriesTagRow(t *testing.T) {
	setupTestDB(t)
	if _, err := DB().Exec(`
		INSERT INTO "Library" ("id", "name", "rootPath")
		VALUES ('series-source-owner-library', 'Series source owner library', '/tmp/series-source-owner-library')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`
		INSERT INTO "Comic" ("id", "filename", "title", "libraryId")
		VALUES ('series-source-owner-comic', 'series-source-owner.cbz', 'Series source owner', 'series-source-owner-library')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`
		INSERT INTO "ComicSeries" ("id", "libraryId", "rootRelativePath", "title", "sortTitle")
		VALUES ('source-owner-series', 'series-source-owner-library', 'work', 'Source owner series', 'Source owner series')
	`); err != nil {
		t.Fatal(err)
	}
	oldSource := "source:https://e-hentai.org/g/23/dddddddddd"
	newSource := "source:https://exhentai.org/g/24/eeeeeeeeee"
	if err := AddTagsToComic("series-source-owner-comic", []string{oldSource}); err != nil {
		t.Fatal(err)
	}
	if err := SetSeriesTags("source-owner-series", []string{oldSource}); err != nil {
		t.Fatal(err)
	}
	if err := AddTagsToComicReplacingMatching(
		"series-source-owner-comic",
		[]string{newSource},
		IsEHentaiGallerySourceTag,
	); err != nil {
		t.Fatal(err)
	}
	comic, err := GetComicByID("series-source-owner-comic")
	if err != nil || comic == nil {
		t.Fatalf("load comic: %v", err)
	}
	assertTagNamesExactly(t, []string{newSource}, comicTagNames(comic))
	seriesTags, err := GetSeriesTags("source-owner-series")
	if err != nil {
		t.Fatal(err)
	}
	assertTagNamesExactly(t, []string{oldSource}, storeTagNames(seriesTags))
}

func TestReplacingComicEHSourceDeletesTrulyUnreferencedTag(t *testing.T) {
	setupTestDB(t)
	insertTagSyncComics(t, "unreferenced-source-owner")
	oldSource := "source:https://e-hentai.org/g/25/ffffffffff"
	newSource := "source:https://exhentai.org/g/26/1234512345"
	if err := AddTagsToComic("unreferenced-source-owner", []string{oldSource}); err != nil {
		t.Fatal(err)
	}
	if err := AddTagsToComicReplacingMatching("unreferenced-source-owner", []string{newSource}, IsEHentaiGallerySourceTag); err != nil {
		t.Fatal(err)
	}
	var oldCount int
	if err := DB().QueryRow(`SELECT COUNT(*) FROM "Tag" WHERE "name" = ?`, oldSource).Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 {
		t.Fatalf("unreferenced old source global tag count = %d", oldCount)
	}
}

func TestSetGroupTagsCleansOnlyUnreferencedPreviousTags(t *testing.T) {
	setupTestDB(t)
	firstGroupID, err := CreateGroup("First source owner group")
	if err != nil {
		t.Fatal(err)
	}
	secondGroupID, err := CreateGroup("Second source owner group")
	if err != nil {
		t.Fatal(err)
	}
	oldSource := "source:https://e-hentai.org/g/31/aaaaaaaaaa"
	newSource := "source:https://exhentai.org/g/32/bbbbbbbbbb"
	if err := SetGroupTags(int(firstGroupID), []string{oldSource}); err != nil {
		t.Fatal(err)
	}
	if err := SetGroupTags(int(secondGroupID), []string{oldSource}); err != nil {
		t.Fatal(err)
	}
	if err := SetGroupTags(int(firstGroupID), []string{newSource}); err != nil {
		t.Fatal(err)
	}
	assertGlobalTagCount(t, oldSource, 1)
	secondTags, err := GetGroupTags(int(secondGroupID))
	if err != nil {
		t.Fatal(err)
	}
	assertTagNamesExactly(t, []string{oldSource}, storeTagNames(secondTags))

	if err := SetGroupTags(int(secondGroupID), []string{"artist:replacement"}); err != nil {
		t.Fatal(err)
	}
	assertGlobalTagCount(t, oldSource, 0)
	firstTags, err := GetGroupTags(int(firstGroupID))
	if err != nil {
		t.Fatal(err)
	}
	assertTagNamesExactly(t, []string{newSource}, storeTagNames(firstTags))
}

func TestSetSeriesTagsCleansOnlyUnreferencedPreviousTags(t *testing.T) {
	setupTestDB(t)
	if _, err := DB().Exec(`
		INSERT INTO "Library" ("id", "name", "rootPath")
		VALUES ('series-cleanup-library', 'Series cleanup library', '/tmp/series-cleanup-library');
		INSERT INTO "ComicSeries" ("id", "libraryId", "rootRelativePath", "title", "sortTitle") VALUES
			('series-cleanup-first', 'series-cleanup-library', 'first', 'First', 'First'),
			('series-cleanup-second', 'series-cleanup-library', 'second', 'Second', 'Second');
	`); err != nil {
		t.Fatal(err)
	}
	oldSource := "source:https://e-hentai.org/g/33/cccccccccc"
	newSource := "source:https://exhentai.org/g/34/dddddddddd"
	if err := SetSeriesTags("series-cleanup-first", []string{oldSource}); err != nil {
		t.Fatal(err)
	}
	if err := SetSeriesTags("series-cleanup-second", []string{oldSource}); err != nil {
		t.Fatal(err)
	}
	if err := SetSeriesTags("series-cleanup-first", []string{newSource}); err != nil {
		t.Fatal(err)
	}
	assertGlobalTagCount(t, oldSource, 1)
	secondTags, err := GetSeriesTags("series-cleanup-second")
	if err != nil {
		t.Fatal(err)
	}
	assertTagNamesExactly(t, []string{oldSource}, storeTagNames(secondTags))

	if err := SetSeriesTags("series-cleanup-second", []string{"artist:replacement"}); err != nil {
		t.Fatal(err)
	}
	assertGlobalTagCount(t, oldSource, 0)
	firstTags, err := GetSeriesTags("series-cleanup-first")
	if err != nil {
		t.Fatal(err)
	}
	assertTagNamesExactly(t, []string{newSource}, storeTagNames(firstTags))
}

func TestDeleteTagIfUnreferencedSupportsMinimalSchema(t *testing.T) {
	legacyDB, err := sql.Open("sqlite", "file:"+testDBPath(t)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { legacyDB.Close() })
	if _, err := legacyDB.Exec(`
		CREATE TABLE "Tag" ("id" INTEGER PRIMARY KEY, "name" TEXT NOT NULL UNIQUE);
		CREATE TABLE "ComicTag" ("comicId" TEXT NOT NULL, "tagId" INTEGER NOT NULL REFERENCES "Tag"("id") ON DELETE CASCADE);
		INSERT INTO "Tag" ("id", "name") VALUES
			(1, 'source:https://e-hentai.org/g/41/eeeeeeeeee'),
			(2, 'user:referenced');
		INSERT INTO "ComicTag" ("comicId", "tagId") VALUES ('legacy-comic', 2);
	`); err != nil {
		t.Fatal(err)
	}
	if err := deleteTagIfUnreferenced(legacyDB, 1); err != nil {
		t.Fatalf("delete unreferenced tag with optional tables absent: %v", err)
	}
	if err := deleteTagIfUnreferenced(legacyDB, 2); err != nil {
		t.Fatalf("preserve referenced tag with optional tables absent: %v", err)
	}
	var count int
	if err := legacyDB.QueryRow(`SELECT COUNT(*) FROM "Tag"`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("minimal schema global tag count = %d, want 1", count)
	}
	if err := legacyDB.QueryRow(`SELECT COUNT(*) FROM "Tag" WHERE "name" = 'user:referenced'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("referenced minimal-schema tag was deleted")
	}
}

func TestReplacingComicTagsRollsBackOnAssociationFailure(t *testing.T) {
	setupTestDB(t)
	insertTagSyncComics(t, "rollback-source-owner")
	oldSource := "source:https://e-hentai.org/g/42/ffffffffff"
	incomingTag := "artist:must-roll-back"
	if err := AddTagsToComic("rollback-source-owner", []string{oldSource}); err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`
		CREATE TRIGGER "fail_incoming_comic_tag"
		BEFORE INSERT ON "ComicTag"
		WHEN (SELECT "name" FROM "Tag" WHERE "id" = NEW."tagId") = 'artist:must-roll-back'
		BEGIN
			SELECT RAISE(ABORT, 'forced ComicTag failure');
		END
	`); err != nil {
		t.Fatal(err)
	}
	if err := AddTagsToComicReplacingMatching(
		"rollback-source-owner",
		[]string{incomingTag},
		IsEHentaiGallerySourceTag,
	); err == nil {
		t.Fatal("replacement unexpectedly succeeded despite injected association failure")
	}
	comic, err := GetComicByID("rollback-source-owner")
	if err != nil || comic == nil {
		t.Fatalf("load comic after rollback: %v", err)
	}
	assertTagNamesExactly(t, []string{oldSource}, comicTagNames(comic))
	assertGlobalTagCount(t, incomingTag, 0)
}

func TestClearAllComicTagsRollsBackOnGlobalCleanupFailure(t *testing.T) {
	setupTestDB(t)
	insertTagSyncComics(t, "clear-rollback-owner")
	originalTags := []string{"user:keep-after-failure", "artist:keep-after-failure"}
	if err := AddTagsToComic("clear-rollback-owner", originalTags); err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`
		CREATE TRIGGER "fail_clear_tag_cleanup"
		BEFORE DELETE ON "Tag"
		WHEN OLD."name" = 'user:keep-after-failure'
		BEGIN
			SELECT RAISE(ABORT, 'forced Tag cleanup failure');
		END
	`); err != nil {
		t.Fatal(err)
	}
	if err := ClearAllTagsFromComic("clear-rollback-owner"); err == nil {
		t.Fatal("clear unexpectedly succeeded despite injected global cleanup failure")
	}
	comic, err := GetComicByID("clear-rollback-owner")
	if err != nil || comic == nil {
		t.Fatalf("load comic after clear rollback: %v", err)
	}
	assertTagNamesExactly(t, originalTags, comicTagNames(comic))
}

func TestRemoveComicTagRollsBackOnGlobalCleanupFailure(t *testing.T) {
	setupTestDB(t)
	insertTagSyncComics(t, "remove-rollback-owner")
	tagName := "user:remove-must-roll-back"
	if err := AddTagsToComic("remove-rollback-owner", []string{tagName}); err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`
		CREATE TRIGGER "fail_remove_tag_cleanup"
		BEFORE DELETE ON "Tag"
		WHEN OLD."name" = 'user:remove-must-roll-back'
		BEGIN
			SELECT RAISE(ABORT, 'forced Tag cleanup failure');
		END
	`); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTagFromComic("remove-rollback-owner", tagName); err == nil {
		t.Fatal("remove unexpectedly succeeded despite injected global cleanup failure")
	}
	comic, err := GetComicByID("remove-rollback-owner")
	if err != nil || comic == nil {
		t.Fatalf("load comic after remove rollback: %v", err)
	}
	assertTagNamesExactly(t, []string{tagName}, comicTagNames(comic))
}

func TestOverrideGroupTagsRollsBackMemberOnAssociationFailure(t *testing.T) {
	setupTestDB(t)
	insertTagSyncComics(t, "override-rollback-member")
	groupID, err := CreateGroupWithItems("Override rollback group", "", []string{"override-rollback-member"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	parentTag := "artist:must-fail-override"
	if err := SetGroupTags(int(groupID), []string{parentTag}); err != nil {
		t.Fatal(err)
	}
	memberSource := "source:https://e-hentai.org/g/43/9999999999"
	originalTags := []string{memberSource, "user:member-favorite"}
	if err := AddTagsToComic("override-rollback-member", originalTags); err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`
		CREATE TRIGGER "fail_override_comic_tag"
		BEFORE INSERT ON "ComicTag"
		WHEN (SELECT "name" FROM "Tag" WHERE "id" = NEW."tagId") = 'artist:must-fail-override'
		BEGIN
			SELECT RAISE(ABORT, 'forced override association failure');
		END
	`); err != nil {
		t.Fatal(err)
	}
	total, synced, tagsSet, err := OverrideGroupTagsToVolumes(int(groupID))
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || synced != 0 || tagsSet != 1 {
		t.Fatalf("override result = total:%d synced:%d tags:%d", total, synced, tagsSet)
	}
	comic, err := GetComicByID("override-rollback-member")
	if err != nil || comic == nil {
		t.Fatalf("load member after override rollback: %v", err)
	}
	assertTagNamesExactly(t, originalTags, comicTagNames(comic))
}

func TestRenameTagMergePreservesEveryOwnerType(t *testing.T) {
	setupTestDB(t)
	insertTagSyncComics(t, "rename-owner-comic")
	groupID, err := CreateGroup("Rename owner group")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`
		INSERT INTO "Library" ("id", "name", "rootPath")
		VALUES ('rename-owner-library', 'Rename owner library', '/tmp/rename-owner-library');
		INSERT INTO "ComicSeries" ("id", "libraryId", "rootRelativePath", "title", "sortTitle")
		VALUES ('rename-owner-series', 'rename-owner-library', 'series', 'Series', 'Series');
	`); err != nil {
		t.Fatal(err)
	}
	oldTag := "artist:old spelling"
	newTag := "artist:canonical spelling"
	if err := AddTagsToComic("rename-owner-comic", []string{oldTag, newTag}); err != nil {
		t.Fatal(err)
	}
	if err := SetGroupTags(int(groupID), []string{oldTag}); err != nil {
		t.Fatal(err)
	}
	if err := SetSeriesTags("rename-owner-series", []string{oldTag}); err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`UPDATE "Comic" SET "genre" = ? WHERE "id" = 'rename-owner-comic'`, oldTag+", "+newTag); err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`UPDATE "ComicGroup" SET "genre" = ? WHERE "id" = ?`, oldTag, groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`UPDATE "ComicSeries" SET "genre" = ? WHERE "id" = 'rename-owner-series'`, oldTag); err != nil {
		t.Fatal(err)
	}
	if err := RenameTag(oldTag, newTag); err != nil {
		t.Fatal(err)
	}
	comic, err := GetComicByID("rename-owner-comic")
	if err != nil || comic == nil {
		t.Fatalf("load renamed comic tags: %v", err)
	}
	assertTagNamesExactly(t, []string{newTag}, comicTagNames(comic))
	if comic.Genre != newTag {
		t.Fatalf("comic Genre after rename = %q, want %q", comic.Genre, newTag)
	}
	groupTags, err := GetGroupTags(int(groupID))
	if err != nil {
		t.Fatal(err)
	}
	assertTagNamesExactly(t, []string{newTag}, storeTagNames(groupTags))
	group, err := GetGroupByID(int(groupID))
	if err != nil || group == nil || group.Genre != newTag {
		t.Fatalf("group Genre after rename = %#v, err=%v", group, err)
	}
	seriesTags, err := GetSeriesTags("rename-owner-series")
	if err != nil {
		t.Fatal(err)
	}
	assertTagNamesExactly(t, []string{newTag}, storeTagNames(seriesTags))
	assertSeriesGenre(t, "rename-owner-series", newTag)
	assertGlobalTagCount(t, oldTag, 0)
	if err := RenameTag(newTag, newTag); err != nil {
		t.Fatal(err)
	}
	assertGlobalTagCount(t, newTag, 1)
}

func TestRenameTagSimpleUpdatesEveryOwnerType(t *testing.T) {
	setupTestDB(t)
	insertTagSyncComics(t, "simple-rename-comic")
	groupID, err := CreateGroup("Simple rename group")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`
		INSERT INTO "Library" ("id", "name", "rootPath")
		VALUES ('simple-rename-library', 'Simple rename library', '/tmp/simple-rename-library');
		INSERT INTO "ComicSeries" ("id", "libraryId", "rootRelativePath", "title", "sortTitle")
		VALUES ('simple-rename-series', 'simple-rename-library', 'series', 'Series', 'Series');
	`); err != nil {
		t.Fatal(err)
	}

	oldTag := "group:old circle"
	newTag := "group:new circle"
	keptTag := "female:kept tag"
	if err := AddTagsToComic("simple-rename-comic", []string{oldTag, keptTag}); err != nil {
		t.Fatal(err)
	}
	if err := SetGroupTags(int(groupID), []string{oldTag, keptTag}); err != nil {
		t.Fatal(err)
	}
	if err := SetSeriesTags("simple-rename-series", []string{oldTag, keptTag}); err != nil {
		t.Fatal(err)
	}
	wantGenre := newTag + ", " + keptTag
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{query: `UPDATE "Comic" SET "genre" = ? WHERE "id" = 'simple-rename-comic'`, args: []any{oldTag + ", " + keptTag}},
		{query: `UPDATE "ComicGroup" SET "genre" = ? WHERE "id" = ?`, args: []any{oldTag + ", " + keptTag, groupID}},
		{query: `UPDATE "ComicSeries" SET "genre" = ? WHERE "id" = 'simple-rename-series'`, args: []any{oldTag + ", " + keptTag}},
	} {
		if _, err := DB().Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	if err := RenameTag(oldTag, newTag); err != nil {
		t.Fatal(err)
	}
	comic, err := GetComicByID("simple-rename-comic")
	if err != nil || comic == nil {
		t.Fatalf("load simply renamed comic: %v", err)
	}
	assertTagNamesExactly(t, []string{newTag, keptTag}, comicTagNames(comic))
	if comic.Genre != wantGenre {
		t.Fatalf("comic Genre after simple rename = %q, want %q", comic.Genre, wantGenre)
	}
	groupTags, err := GetGroupTags(int(groupID))
	if err != nil {
		t.Fatal(err)
	}
	assertTagNamesExactly(t, []string{newTag, keptTag}, storeTagNames(groupTags))
	group, err := GetGroupByID(int(groupID))
	if err != nil || group == nil || group.Genre != wantGenre {
		t.Fatalf("group Genre after simple rename = %#v, err=%v", group, err)
	}
	seriesTags, err := GetSeriesTags("simple-rename-series")
	if err != nil {
		t.Fatal(err)
	}
	assertTagNamesExactly(t, []string{newTag, keptTag}, storeTagNames(seriesTags))
	assertSeriesGenre(t, "simple-rename-series", wantGenre)
	assertGlobalTagCount(t, oldTag, 0)
	assertGlobalTagCount(t, newTag, 1)
}

func TestDeleteTagRemovesGenreFromEveryOwnerType(t *testing.T) {
	setupTestDB(t)
	insertTagSyncComics(t, "delete-tag-owner-comic")
	groupID, err := CreateGroup("Delete tag owner group")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`
		INSERT INTO "Library" ("id", "name", "rootPath")
		VALUES ('delete-tag-owner-library', 'Delete tag owner library', '/tmp/delete-tag-owner-library');
		INSERT INTO "ComicSeries" ("id", "libraryId", "rootRelativePath", "title", "sortTitle")
		VALUES ('delete-tag-owner-series', 'delete-tag-owner-library', 'series', 'Series', 'Series');
	`); err != nil {
		t.Fatal(err)
	}
	deletedTag := "female:obsolete tag"
	keptTag := "female:kept tag"
	if err := AddTagsToComic("delete-tag-owner-comic", []string{deletedTag, keptTag}); err != nil {
		t.Fatal(err)
	}
	if err := SetGroupTags(int(groupID), []string{deletedTag, keptTag}); err != nil {
		t.Fatal(err)
	}
	if err := SetSeriesTags("delete-tag-owner-series", []string{deletedTag, keptTag}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{query: `UPDATE "Comic" SET "genre" = ? WHERE "id" = 'delete-tag-owner-comic'`, args: []any{deletedTag + ", " + keptTag}},
		{query: `UPDATE "ComicGroup" SET "genre" = ? WHERE "id" = ?`, args: []any{deletedTag + ", " + keptTag, groupID}},
		{query: `UPDATE "ComicSeries" SET "genre" = ? WHERE "id" = 'delete-tag-owner-series'`, args: []any{deletedTag + ", " + keptTag}},
	} {
		if _, err := DB().Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := DeleteTag(deletedTag); err != nil {
		t.Fatal(err)
	}
	comic, err := GetComicByID("delete-tag-owner-comic")
	if err != nil || comic == nil || comic.Genre != keptTag {
		t.Fatalf("comic after tag delete = %#v, err=%v", comic, err)
	}
	group, err := GetGroupByID(int(groupID))
	if err != nil || group == nil || group.Genre != keptTag {
		t.Fatalf("group after tag delete = %#v, err=%v", group, err)
	}
	assertSeriesGenre(t, "delete-tag-owner-series", keptTag)
	assertGlobalTagCount(t, deletedTag, 0)
}

func TestGetAllTagsCountsEveryOwnerType(t *testing.T) {
	setupTestDB(t)
	insertTagSyncComics(t, "tag-count-comic")
	groupID, err := CreateGroup("Tag count group")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`
		INSERT INTO "Library" ("id", "name", "rootPath")
		VALUES ('tag-count-library', 'Tag count library', '/tmp/tag-count-library');
		INSERT INTO "ComicSeries" ("id", "libraryId", "rootRelativePath", "title", "sortTitle")
		VALUES ('tag-count-series', 'tag-count-library', 'series', 'Series', 'Series');
	`); err != nil {
		t.Fatal(err)
	}
	tagName := "source:https://e-hentai.org/g/59/9999999999"
	if err := AddTagsToComic("tag-count-comic", []string{tagName}); err != nil {
		t.Fatal(err)
	}
	if err := SetGroupTags(int(groupID), []string{tagName}); err != nil {
		t.Fatal(err)
	}
	if err := SetSeriesTags("tag-count-series", []string{tagName}); err != nil {
		t.Fatal(err)
	}
	tags, err := GetAllTags()
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if tag.Name == tagName {
			if tag.Count != 3 {
				t.Fatalf("all-owner usage count = %d, want 3", tag.Count)
			}
			return
		}
	}
	t.Fatalf("tag manager result missing %q", tagName)
}

func TestGroupMetadataAndTagsRollbackTogether(t *testing.T) {
	setupTestDB(t)
	groupID, err := CreateGroup("Atomic group")
	if err != nil {
		t.Fatal(err)
	}
	oldGenre := "artist:old group"
	newGenre := "artist:new group"
	if err := UpdateGroupMetadataAndTags(int(groupID), GroupMetadataUpdate{Genre: &oldGenre}, []string{oldGenre}); err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`
		CREATE TRIGGER "fail_atomic_group_tag"
		BEFORE INSERT ON "ComicGroupTag"
		WHEN (SELECT "name" FROM "Tag" WHERE "id" = NEW."tagId") = 'artist:new group'
		BEGIN
			SELECT RAISE(ABORT, 'forced atomic group tag failure');
		END
	`); err != nil {
		t.Fatal(err)
	}
	if err := UpdateGroupMetadataAndTags(int(groupID), GroupMetadataUpdate{Genre: &newGenre}, []string{newGenre}); err == nil {
		t.Fatal("atomic group update unexpectedly succeeded")
	}
	group, err := GetGroupByID(int(groupID))
	if err != nil || group == nil || group.Genre != oldGenre {
		t.Fatalf("group metadata did not roll back: %#v, err=%v", group, err)
	}
	tags, err := GetGroupTags(int(groupID))
	if err != nil {
		t.Fatal(err)
	}
	assertTagNamesExactly(t, []string{oldGenre}, storeTagNames(tags))
}

func TestSeriesMetadataAndTagsRollbackTogether(t *testing.T) {
	setupTestDB(t)
	if _, err := DB().Exec(`
		INSERT INTO "Library" ("id", "name", "rootPath")
		VALUES ('atomic-series-library', 'Atomic series library', '/tmp/atomic-series-library');
		INSERT INTO "ComicSeries" ("id", "libraryId", "rootRelativePath", "title", "sortTitle")
		VALUES ('atomic-series', 'atomic-series-library', 'series', 'Series', 'Series');
	`); err != nil {
		t.Fatal(err)
	}
	oldGenre := "artist:old series"
	newGenre := "artist:new series"
	if err := UpdateSeriesMetadataAndTags("atomic-series", SeriesMetadataUpdate{Genre: &oldGenre}, []string{oldGenre}); err != nil {
		t.Fatal(err)
	}
	if _, err := DB().Exec(`
		CREATE TRIGGER "fail_atomic_series_tag"
		BEFORE INSERT ON "ComicSeriesTag"
		WHEN (SELECT "name" FROM "Tag" WHERE "id" = NEW."tagId") = 'artist:new series'
		BEGIN
			SELECT RAISE(ABORT, 'forced atomic series tag failure');
		END
	`); err != nil {
		t.Fatal(err)
	}
	if err := UpdateSeriesMetadataAndTags("atomic-series", SeriesMetadataUpdate{Genre: &newGenre}, []string{newGenre}); err == nil {
		t.Fatal("atomic series update unexpectedly succeeded")
	}
	assertSeriesGenre(t, "atomic-series", oldGenre)
	tags, err := GetSeriesTags("atomic-series")
	if err != nil {
		t.Fatal(err)
	}
	assertTagNamesExactly(t, []string{oldGenre}, storeTagNames(tags))
}

func ehNamespacedTagFixture() []string {
	return []string{
		"artist:first artist",
		"artist:second artist",
		"female:first tag",
		"female:second tag",
		"male:example tag",
		"parody:fixture work",
		"character:fixture heroine",
		"language:english",
		"category:manga",
		"source:https://exhentai.org/g/2/abcdef0123",
	}
}

func insertTagSyncComics(t *testing.T, comicIDs ...string) {
	t.Helper()
	for _, comicID := range comicIDs {
		if _, err := DB().Exec(`
			INSERT INTO "Comic" ("id", "filename", "title") VALUES (?, ?, ?)
		`, comicID, comicID+".cbz", comicID); err != nil {
			t.Fatal(err)
		}
	}
}

func seedMemberTags(t *testing.T, comicIDs ...string) map[string]string {
	t.Helper()
	sources := make(map[string]string, len(comicIDs))
	for index, comicID := range comicIDs {
		source := []string{
			"source:https://e-hentai.org/g/11/1111111111",
			"source:https://exhentai.org/g/12/2222222222",
		}[index]
		sources[comicID] = source
		if err := AddTagsToComic(comicID, []string{
			source,
			"source:https://metadata.example/items/keep",
			"user:favorite",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return sources
}

func assertSyncedEHNamespacedTags(t *testing.T, parentTags []string, memberSources map[string]string, comicIDs ...string) {
	t.Helper()
	want := []string{
		"source:https://metadata.example/items/keep",
		"user:favorite",
	}
	for _, tag := range parentTags {
		if !IsEHentaiGallerySourceTag(tag) {
			want = append(want, tag)
		}
	}
	for _, comicID := range comicIDs {
		comic, err := GetComicByID(comicID)
		if err != nil || comic == nil {
			t.Fatalf("GetComicByID(%q) error = %v", comicID, err)
		}
		got := make(map[string]bool, len(comic.Tags))
		for _, tag := range comic.Tags {
			got[tag.Name] = true
		}
		wantForComic := append(append([]string(nil), want...), memberSources[comicID])
		if len(got) != len(wantForComic) {
			t.Fatalf("comic %s tag count = %d, want %d: %#v", comicID, len(got), len(wantForComic), got)
		}
		for _, tag := range wantForComic {
			if !got[tag] {
				t.Fatalf("comic %s missing tag %q: %#v", comicID, tag, got)
			}
		}
		for _, parentTag := range parentTags {
			if IsEHentaiGallerySourceTag(parentTag) && got[parentTag] {
				t.Fatalf("comic %s inherited parent gallery source %q: %#v", comicID, parentTag, got)
			}
		}
	}
}

func assertOverriddenEHNamespacedTags(t *testing.T, parentTags []string, memberSources map[string]string, comicIDs ...string) {
	t.Helper()
	parentContentTags := make([]string, 0, len(parentTags))
	for _, tag := range parentTags {
		if !IsEHentaiGallerySourceTag(tag) {
			parentContentTags = append(parentContentTags, tag)
		}
	}
	for _, comicID := range comicIDs {
		comic, err := GetComicByID(comicID)
		if err != nil || comic == nil {
			t.Fatalf("GetComicByID(%q) error = %v", comicID, err)
		}
		want := append(append([]string(nil), parentContentTags...), memberSources[comicID])
		assertTagNamesExactly(t, want, comicTagNames(comic))
	}
}

func comicTagNames(comic *ComicListItem) []string {
	names := make([]string, 0, len(comic.Tags))
	for _, tag := range comic.Tags {
		names = append(names, tag.Name)
	}
	return names
}

func storeTagNames(tags []Tag) []string {
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		names = append(names, tag.Name)
	}
	return names
}

func assertTagNamesExactly(t *testing.T, want, got []string) {
	t.Helper()
	wantSet := make(map[string]bool, len(want))
	for _, tag := range want {
		wantSet[tag] = true
	}
	gotSet := make(map[string]bool, len(got))
	for _, tag := range got {
		gotSet[tag] = true
	}
	if len(gotSet) != len(wantSet) {
		t.Fatalf("tags = %#v, want %#v", gotSet, wantSet)
	}
	for tag := range wantSet {
		if !gotSet[tag] {
			t.Fatalf("tags missing %q: got %#v", tag, gotSet)
		}
	}
}

func assertGlobalTagCount(t *testing.T, tagName string, want int) {
	t.Helper()
	var got int
	if err := DB().QueryRow(`SELECT COUNT(*) FROM "Tag" WHERE "name" = ?`, tagName).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("global Tag count for %q = %d, want %d", tagName, got, want)
	}
}

func assertSeriesGenre(t *testing.T, seriesID, want string) {
	t.Helper()
	var got string
	if err := DB().QueryRow(`SELECT COALESCE("genre", '') FROM "ComicSeries" WHERE "id" = ?`, seriesID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("series %q Genre = %q, want %q", seriesID, got, want)
	}
}
