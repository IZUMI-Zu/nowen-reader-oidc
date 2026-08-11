package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nowen-reader/nowen-reader/internal/middleware"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

// TagHandler handles tag-related API endpoints.
type TagHandler struct{}

// NewTagHandler creates a new TagHandler.
func NewTagHandler() *TagHandler {
	return &TagHandler{}
}

// GET /api/tags — List tags visible to the current user. scope=comics returns
// only tags usable by the comic-list filter. Without a scope, administrators
// retain the all-owner Tag Manager view while readers remain ComicTag-scoped.
func (h *TagHandler) ListTags(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}

	var (
		tags []store.TagWithCount
		err  error
	)
	comicScope := c.Query("scope") == "comics"
	if user.Role == "admin" && comicScope {
		tags, err = store.GetAllComicTags()
	} else if user.Role == "admin" {
		tags, err = store.GetAllTags()
	} else {
		var libraryIDs []string
		libraryIDs, err = store.GetUserAccessibleLibraryIDs(user.ID)
		if err == nil {
			tags, err = store.GetComicTagsForLibraries(libraryIDs)
		}
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch tags"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tags": tags})
}

// PUT /api/tags/color — Update tag color
func (h *TagHandler) UpdateTagColor(c *gin.Context) {
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Name == "" || body.Color == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and color required"})
		return
	}

	if err := store.UpdateTagColor(body.Name, body.Color); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update tag color"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// PUT /api/tags/rename — Rename (or merge) a tag
func (h *TagHandler) RenameTag(c *gin.Context) {
	var body struct {
		OldName string `json:"oldName"`
		NewName string `json:"newName"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.OldName == "" || body.NewName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "oldName and newName required"})
		return
	}

	if err := store.RenameTag(body.OldName, body.NewName); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to rename tag"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// DELETE /api/tags — Delete a tag entirely (removes from all comics)
func (h *TagHandler) DeleteTag(c *gin.Context) {
	var body struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name required"})
		return
	}

	if err := store.DeleteTag(body.Name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete tag"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// POST /api/tags/merge — Merge multiple tags into one
func (h *TagHandler) MergeTags(c *gin.Context) {
	var body struct {
		SourceNames []string `json:"sourceNames"`
		TargetName  string   `json:"targetName"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.SourceNames) == 0 || body.TargetName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sourceNames and targetName required"})
		return
	}

	for _, src := range body.SourceNames {
		if src != body.TargetName {
			if err := store.RenameTag(src, body.TargetName); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to merge tags"})
				return
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}
