package store

import (
	"database/sql"
	"strings"
)

func GetSeriesTags(seriesID string) ([]Tag, error) {
	rows, err := db.Query(`
		SELECT t."id", t."name", t."color"
		FROM "Tag" t
		JOIN "ComicSeriesTag" cst ON cst."tagId" = t."id"
		WHERE cst."seriesId" = ?
		ORDER BY t."name"
	`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := []Tag{}
	for rows.Next() {
		var tag Tag
		if err := rows.Scan(&tag.ID, &tag.Name, &tag.Color); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

func SetSeriesTags(seriesID string, tagNames []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setSeriesTags(tx, seriesID, tagNames); err != nil {
		return err
	}
	return tx.Commit()
}

func setSeriesTags(database tagDatabase, seriesID string, tagNames []string) error {
	rows, err := database.Query(`SELECT "tagId" FROM "ComicSeriesTag" WHERE "seriesId" = ?`, seriesID)
	if err != nil {
		return err
	}
	var previousTagIDs []int
	for rows.Next() {
		var tagID int
		if err := rows.Scan(&tagID); err != nil {
			rows.Close()
			return err
		}
		previousTagIDs = append(previousTagIDs, tagID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := database.Exec(`DELETE FROM "ComicSeriesTag" WHERE "seriesId" = ?`, seriesID); err != nil {
		return err
	}
	for _, rawName := range tagNames {
		name := strings.TrimSpace(rawName)
		if name == "" {
			continue
		}
		var tagID int
		err := database.QueryRow(`SELECT "id" FROM "Tag" WHERE "name" = ?`, name).Scan(&tagID)
		if err == sql.ErrNoRows {
			result, createErr := database.Exec(`INSERT INTO "Tag" ("name", "color") VALUES (?, '')`, name)
			if createErr != nil {
				return createErr
			}
			id, idErr := result.LastInsertId()
			if idErr != nil {
				return idErr
			}
			tagID = int(id)
		} else if err != nil {
			return err
		}
		if _, err := database.Exec(`INSERT OR IGNORE INTO "ComicSeriesTag" ("seriesId", "tagId") VALUES (?, ?)`, seriesID, tagID); err != nil {
			return err
		}
	}
	for _, tagID := range previousTagIDs {
		if err := deleteTagIfUnreferenced(database, tagID); err != nil {
			return err
		}
	}
	return nil
}

func SyncSeriesTagsToItems(seriesID string) (total, synced, tagsCount int, err error) {
	tags, err := GetSeriesTags(seriesID)
	if err != nil {
		return 0, 0, 0, err
	}
	ids, err := GetSeriesMemberComicIDs(seriesID)
	if err != nil {
		return 0, 0, 0, err
	}
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		// Keep the series gallery identity on the series. Each member may map to
		// a different EH/EX gallery and must retain its own source tag.
		if IsEHentaiGallerySourceTag(tag.Name) {
			continue
		}
		names = append(names, tag.Name)
	}
	for _, comicID := range ids {
		if err := AddTagsToComic(comicID, names); err == nil {
			synced++
		}
	}
	return len(ids), synced, len(names), nil
}
