package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// ResolveModelCredential accepts assertions only from the trusted root backend.
// The returned credential is never a dashboard credential or a payer Token.
func ResolveModelCredential(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if c.GetInt("role") < common.RoleRootUser {
		c.JSON(403, gin.H{"success": false, "message": "root required"})
		return
	}
	var req struct {
		ProviderID int    `json:"provider_id"`
		Issuer     string `json:"issuer"`
		Subject    string `json:"subject"`
	}
	if common.DecodeJson(c.Request.Body, &req) != nil || req.ProviderID <= 0 || req.Issuer == "" || req.Subject == "" || len(req.Subject) > 256 {
		c.JSON(400, gin.H{"success": false, "message": "invalid identity"})
		return
	}
	token, err := model.ResolveIntegrationCredential(req.ProviderID, req.Issuer, req.Subject)
	if err != nil {
		c.JSON(409, gin.H{"success": false, "message": "model identity unavailable"})
		return
	}
	model.RecordOperationAuditLog(c.GetInt("id"), c.GetInt("role"), "model credential resolved", c.ClientIP(), "integration.credential", map[string]any{"provider_id": req.ProviderID, "user_id": token.UserId, "token_id": token.Id}, nil, nil, c)
	common.ApiSuccess(c, gin.H{"user_id": token.UserId, "token_id": token.Id, "key": "sk-" + token.Key})
}
