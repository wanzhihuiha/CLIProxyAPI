package management

import (
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type codexQuotaRefreshRequest struct {
	AuthIndexSnake *string `json:"auth_index"`
	AuthIndexCamel *string `json:"authIndex"`
	AuthIDSnake    *string `json:"auth_id"`
	AuthIDCamel    *string `json:"authId"`
	All            *bool   `json:"all"`
}

// RefreshCodexQuota triggers proactive wham/usage quota refresh for one or all Codex auths.
func (h *Handler) RefreshCodexQuota(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "auth manager not initialized"})
		return
	}

	var body codexQuotaRefreshRequest
	if c.Request != nil && c.Request.Body != nil {
		if errBind := c.ShouldBindJSON(&body); errBind != nil && errBind != io.EOF {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
	}

	authID := firstNonEmptyString(body.AuthIDSnake, body.AuthIDCamel)
	authIndex := firstNonEmptyString(body.AuthIndexSnake, body.AuthIndexCamel)
	if authID != "" || authIndex != "" {
		h.refreshOneCodexQuota(c, authID, authIndex)
		return
	}

	results := make([]coreauth.CodexQuotaRefreshResult, 0)
	skipped := 0
	for _, auth := range h.authManager.List() {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			skipped++
			continue
		}
		result, _ := h.authManager.RefreshCodexQuota(c.Request.Context(), auth.ID)
		results = append(results, result)
	}
	c.JSON(http.StatusOK, gin.H{
		"results": results,
		"skipped": skipped,
	})
}

func (h *Handler) refreshOneCodexQuota(c *gin.Context, authID, authIndex string) {
	if authIndex != "" {
		auth := h.authByIndex(authIndex)
		if auth == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
			return
		}
		authID = auth.ID
	}
	if authID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing auth identifier"})
		return
	}
	if _, ok := h.authManager.GetByID(authID); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}
	result, _ := h.authManager.RefreshCodexQuota(c.Request.Context(), authID)
	c.JSON(http.StatusOK, gin.H{"results": []coreauth.CodexQuotaRefreshResult{result}})
}
