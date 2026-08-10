package store

import (
	"database/sql"
	"log"
	"strings"
)

// ============================================================
// P2: 系列级标签管理
// ============================================================

// GetGroupTags 获取系列的所有标签。
func GetGroupTags(groupID int) ([]Tag, error) {
	rows, err := db.Query(`
		SELECT t."id", t."name", t."color"
		FROM "Tag" t
		INNER JOIN "ComicGroupTag" cgt ON cgt."tagId" = t."id"
		WHERE cgt."groupId" = ?
		ORDER BY t."name" ASC
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color); err != nil {
			continue
		}
		tags = append(tags, t)
	}
	if tags == nil {
		tags = []Tag{}
	}
	return tags, nil
}

// SetGroupTags 设置系列的标签（替换所有现有标签）。
// tagNames: 标签名称列表，不存在的标签会自动创建。
func SetGroupTags(groupID int, tagNames []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setGroupTags(tx, groupID, tagNames); err != nil {
		return err
	}
	return tx.Commit()
}

func setGroupTags(database tagDatabase, groupID int, tagNames []string) error {
	rows, err := database.Query(`SELECT "tagId" FROM "ComicGroupTag" WHERE "groupId" = ?`, groupID)
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

	if _, err := database.Exec(`DELETE FROM "ComicGroupTag" WHERE "groupId" = ?`, groupID); err != nil {
		return err
	}

	// 确保标签存在并获取 ID
	for _, name := range tagNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		// 查找或创建标签
		var tagID int
		err := database.QueryRow(`SELECT "id" FROM "Tag" WHERE "name" = ?`, name).Scan(&tagID)
		if err == sql.ErrNoRows {
			// 创建新标签
			res, err := database.Exec(`INSERT INTO "Tag" ("name", "color") VALUES (?, '')`, name)
			if err != nil {
				return err
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			tagID = int(id)
		} else if err != nil {
			return err
		}
		// 添加关联
		if _, err := database.Exec(`INSERT OR IGNORE INTO "ComicGroupTag" ("groupId", "tagId") VALUES (?, ?)`, groupID, tagID); err != nil {
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

// SyncGroupTagsToVolumes 将系列级标签同步到系列内所有卷。
// 仅添加卷中缺少的标签，不删除卷已有的标签。
func SyncGroupTagsToVolumes(groupID int) (totalVolumes, syncedVolumes, tagsCount int, err error) {
	group, e := GetGroupByID(groupID)
	if e != nil {
		err = e
		return
	}
	if group == nil || len(group.Comics) == 0 {
		return
	}

	// 获取系列级标签名称
	groupTags, e := GetGroupTags(groupID)
	if e != nil {
		err = e
		return
	}

	var tagNames []string
	for _, t := range groupTags {
		// A gallery source identifies the group metadata result itself. Copying
		// it to every member would overwrite each comic's own exact gallery
		// identity and make later source-based lookups resolve the wrong work.
		if IsEHentaiGallerySourceTag(t.Name) {
			continue
		}
		tagNames = append(tagNames, t.Name)
	}

	totalVolumes = len(group.Comics)
	tagsCount = len(tagNames)

	// 为每本漫画添加缺少的标签
	for _, comic := range group.Comics {
		if e := AddTagsToComic(comic.ComicID, tagNames); e != nil {
			log.Printf("[SyncGroupTags] 同步漫画 %s 标签失败: %v", comic.ComicID, e)
			continue
		}
		syncedVolumes++
	}

	return
}

// OverrideGroupTagsToVolumes 将系列级标签覆盖到系列内所有卷。
// 先清除卷的所有标签，再设置为系列标签，返回处理统计信息。
func OverrideGroupTagsToVolumes(groupID int) (totalVolumes, syncedVolumes, tagsSet int, err error) {
	group, e := GetGroupByID(groupID)
	if e != nil {
		err = e
		return
	}
	if group == nil || len(group.Comics) == 0 {
		return
	}

	// 获取系列级标签名称
	groupTags, e := GetGroupTags(groupID)
	if e != nil {
		err = e
		return
	}

	var tagNames []string
	for _, t := range groupTags {
		if IsEHentaiGallerySourceTag(t.Name) {
			continue
		}
		tagNames = append(tagNames, t.Name)
	}

	totalVolumes = len(group.Comics)

	// 对每本漫画：保留实体自己的 EH/EX gallery 身份，覆盖其余标签。
	for _, comic := range group.Comics {
		if e := ReplaceAllTagsOnComicPreservingMatching(comic.ComicID, tagNames, IsEHentaiGallerySourceTag); e != nil {
			log.Printf("[OverrideGroupTags] 设置漫画 %s 标签失败: %v", comic.ComicID, e)
			continue
		}
		syncedVolumes++
	}
	tagsSet = len(tagNames)

	return
}
