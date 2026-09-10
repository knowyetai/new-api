package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
)

func CreateBillingUnit(c *gin.Context) {
	if c.GetInt("role") < common.RoleAdminUser {
		c.JSON(403, gin.H{"success": false, "message": "admin required"})
		return
	}
	var req struct {
		Name        string `json:"name"`
		OwnerUserId int    `json:"owner_user_id"`
		PayerUserId int    `json:"payer_user_id"`
	}
	if common.DecodeJson(c.Request.Body, &req) != nil {
		c.JSON(400, gin.H{"success": false, "message": "invalid request"})
		return
	}
	unit, err := model.CreateBillingUnit(req.Name, req.OwnerUserId, req.PayerUserId)
	if err != nil {
		c.JSON(409, gin.H{"success": false, "message": "billing unit creation rejected"})
		return
	}
	model.RecordOperationAuditLog(c.GetInt("id"), c.GetInt("role"), "billing unit created", c.ClientIP(), "billing_unit.create", map[string]any{"unit_id": unit.Id, "owner_user_id": unit.OwnerUserId, "payer_user_id": unit.PayerUserId}, nil, nil, c)
	common.ApiSuccess(c, unit)
}
func ListBillingUnits(c *gin.Context) {
	units := []model.BillingUnit{}
	query := model.DB.Model(&model.BillingUnit{})
	if c.GetInt("role") < common.RoleAdminUser {
		query = query.Where("owner_user_id = ?", c.GetInt("id"))
	}
	page := common.GetPageQuery(c)
	var total int64
	if query.Count(&total).Error != nil || query.Order("id").Offset(page.GetStartIdx()).Limit(page.GetPageSize()).Find(&units).Error != nil {
		c.JSON(503, gin.H{"success": false, "message": "query unavailable"})
		return
	}
	common.ApiSuccess(c, gin.H{"items": units, "total": total})
}
func managedBillingUnit(c *gin.Context) (*model.BillingUnit, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(400, gin.H{"success": false, "message": "invalid id"})
		return nil, false
	}
	var unit model.BillingUnit
	query := model.DB.Where("id = ?", id)
	if c.GetInt("role") < common.RoleAdminUser {
		query = query.Where("owner_user_id = ?", c.GetInt("id"))
	}
	if query.First(&unit).Error != nil {
		c.JSON(404, gin.H{"success": false, "message": "billing unit unavailable"})
		return nil, false
	}
	return &unit, true
}
func GetBillingUnit(c *gin.Context) {
	unit, ok := managedBillingUnit(c)
	if !ok {
		return
	}
	user, err := model.GetUserById(unit.PayerUserId, false)
	if err != nil {
		c.JSON(503, gin.H{"success": false, "message": "account unavailable"})
		return
	}
	common.ApiSuccess(c, gin.H{"unit": unit, "enabled": user.Status == common.UserStatusEnabled, "balance_quota": user.Quota, "used_quota": user.UsedQuota})
}
func BillingUnitMembers(c *gin.Context) {
	unit, ok := managedBillingUnit(c)
	if !ok {
		return
	}
	if c.Request.Method == http.MethodGet {
		members := []model.BillingUnitMember{}
		page := common.GetPageQuery(c)
		var total int64
		query := model.DB.Model(&model.BillingUnitMember{}).Where("billing_unit_id = ?", unit.Id)
		if query.Count(&total).Error != nil || query.Order("user_id").Offset(page.GetStartIdx()).Limit(page.GetPageSize()).Find(&members).Error != nil {
			c.JSON(503, gin.H{"success": false, "message": "query unavailable"})
			return
		}
		common.ApiSuccess(c, gin.H{"items": members, "total": total})
		return
	}
	uid, err := strconv.Atoi(c.Param("user_id"))
	if err != nil || uid <= 0 {
		c.JSON(400, gin.H{"success": false, "message": "invalid member"})
		return
	}
	if err = model.SetBillingUnitMember(unit.Id, uid, c.Request.Method == http.MethodDelete); err != nil {
		c.JSON(409, gin.H{"success": false, "message": "membership change rejected"})
		return
	}
	model.RecordOperationAuditLog(c.GetInt("id"), c.GetInt("role"), "billing unit membership changed", c.ClientIP(), "billing_unit.member", map[string]any{"unit_id": unit.Id, "member_user_id": uid, "removed": c.Request.Method == http.MethodDelete}, nil, nil, c)
	common.ApiSuccess(c, nil)
}
func BillingUnitUsage(c *gin.Context) {
	unit, ok := managedBillingUnit(c)
	if !ok {
		return
	}
	type item struct {
		UserId           int    `json:"user_id"`
		TokenId          int    `json:"token_id"`
		Quota            int    `json:"quota"`
		PromptTokens     int    `json:"prompt_tokens"`
		CompletionTokens int    `json:"completion_tokens"`
		CreatedAt        int64  `json:"created_at"`
		RequestId        string `json:"request_id"`
	}
	items := []item{}
	page := common.GetPageQuery(c)
	var total int64
	query := model.LOG_DB.Model(&model.Log{}).Where("billing_unit_id = ? AND type = ?", unit.Id, model.LogTypeConsume)
	if query.Count(&total).Error != nil || query.Select("user_id, token_id, quota, prompt_tokens, completion_tokens, created_at, request_id").Order("id desc").Offset(page.GetStartIdx()).Limit(page.GetPageSize()).Scan(&items).Error != nil {
		c.JSON(503, gin.H{"success": false, "message": "usage unavailable"})
		return
	}
	common.ApiSuccess(c, gin.H{"items": items, "total": total})
}

func SetBillingUnitEnabled(c *gin.Context) {
	unit, ok := managedBillingUnit(c)
	if !ok {
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if common.DecodeJson(c.Request.Body, &req) != nil || req.Enabled == nil {
		c.JSON(400, gin.H{"success": false, "message": "enabled is required"})
		return
	}
	status := common.UserStatusDisabled
	if *req.Enabled {
		status = common.UserStatusEnabled
	}
	user := model.User{Id: unit.PayerUserId, Status: status}
	if err := user.Update(false); err != nil {
		c.JSON(409, gin.H{"success": false, "message": "billing account status outcome unknown; query unit status"})
		return
	}
	model.RecordOperationAuditLog(c.GetInt("id"), c.GetInt("role"), "billing unit status changed", c.ClientIP(), "billing_unit.status", map[string]any{"unit_id": unit.Id, "enabled": user.Status == common.UserStatusEnabled}, nil, nil, c)
	common.ApiSuccess(c, gin.H{"enabled": user.Status == common.UserStatusEnabled})
}
func GetMyBillingUnit(c *gin.Context) {
	unit, err := model.ResolveBillingUnit(c.GetInt("id"))
	if err != nil {
		c.JSON(503, gin.H{"success": false, "message": "billing identity unavailable"})
		return
	}
	if unit == nil {
		common.ApiSuccess(c, gin.H{"billing_unit_id": nil, "payment_mode": "personal"})
		return
	}
	common.ApiSuccess(c, gin.H{"billing_unit_id": unit.Id, "name": unit.Name, "payment_mode": "unit"})
}
