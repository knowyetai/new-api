package controller

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func teamFeature(c *gin.Context, name string) bool {
	if os.Getenv(name) != "true" {
		c.JSON(403, gin.H{"success": false, "message": "TEAM_FEATURE_DISABLED", "code": "TEAM_FEATURE_DISABLED"})
		return false
	}
	return true
}
func teamFailure(c *gin.Context, err error) {
	status := http.StatusServiceUnavailable
	message := "TEAM_SERVICE_UNAVAILABLE"
	switch {
	case errors.Is(err, model.ErrTeamUnavailable):
		status = 404
		message = err.Error()
	case errors.Is(err, model.ErrTeamInvalid):
		status = 400
		message = err.Error()
	case errors.Is(err, model.ErrTeamConflict), errors.Is(err, model.ErrTeamInvitationExpired):
		status = 409
		message = err.Error()
	case errors.Is(err, model.ErrTeamLimit):
		status = 429
		message = err.Error()
	}
	c.JSON(status, gin.H{"success": false, "message": message, "code": message})
}
func teamAudit(c *gin.Context, action string, details map[string]any) {
	model.RecordOperationAuditLog(c.GetInt("id"), c.GetInt("role"), action, c.ClientIP(), action, details, nil, nil, c)
}
func CreateSelfServiceTeam(c *gin.Context) {
	if !teamFeature(c, "BILLING_TEAM_SELF_SERVICE_ENABLED") {
		return
	}
	var req struct {
		Name       string `json:"name"`
		RequestKey string `json:"request_key"`
	}
	if common.DecodeJson(c.Request.Body, &req) != nil {
		teamFailure(c, model.ErrTeamInvalid)
		return
	}
	unit, err := model.CreateSelfServiceBillingUnit(c.GetInt("id"), req.Name, req.RequestKey)
	if err != nil {
		teamFailure(c, err)
		return
	}
	teamAudit(c, "billing_team.create", map[string]any{"unit_id": unit.Id})
	common.ApiSuccess(c, unit)
}
func CreateTeamInvitation(c *gin.Context) {
	if !teamFeature(c, "BILLING_TEAM_SELF_SERVICE_ENABLED") {
		return
	}
	unit, ok := managedBillingUnit(c)
	if !ok {
		return
	}
	var req struct {
		Username string `json:"username"`
	}
	if common.DecodeJson(c.Request.Body, &req) != nil {
		teamFailure(c, model.ErrTeamInvalid)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	recipient, err := service.ResolveTeamInvitee(ctx, req.Username)
	if err != nil {
		teamFailure(c, err)
		return
	}
	invitation, err := model.InviteBillingUnitMember(unit.Id, c.GetInt("id"), recipient)
	if err != nil {
		teamFailure(c, err)
		return
	}
	teamAudit(c, "billing_team.invite", map[string]any{"unit_id": unit.Id, "invitation_id": invitation.Id})
	common.ApiSuccess(c, invitation)
}
func ListTeamInvitations(c *gin.Context) {
	query := model.DB.Model(&model.BillingUnitInvitation{})
	if c.Param("id") != "" {
		unit, ok := managedBillingUnit(c)
		if !ok {
			return
		}
		query = query.Where("billing_unit_id = ?", unit.Id)
	} else {
		// Filter by identity digest so SQL collations cannot expose another subject.
		bindings := []model.UserOAuthBinding{}
		if err := model.DB.Where("user_id = ?", c.GetInt("id")).Find(&bindings).Error; err != nil {
			teamFailure(c, err)
			return
		}
		keys := []string{model.BillingInvitationRecipientKey(c.GetInt("id"), 0, "")}
		for _, b := range bindings {
			keys = append(keys, model.BillingInvitationRecipientKey(0, b.ProviderId, b.ProviderUserId))
		}
		query = query.Where("recipient_key IN ?", keys)
	}
	page := common.GetPageQuery(c)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		teamFailure(c, err)
		return
	}
	rows := []model.BillingUnitInvitation{}
	if err := query.Order("id desc").Offset(page.GetStartIdx()).Limit(page.GetPageSize()).Find(&rows).Error; err != nil {
		teamFailure(c, err)
		return
	}
	type item struct {
		model.BillingUnitInvitation
		TeamName string `json:"team_name"`
	}
	result := []item{}
	for _, row := range rows {
		if c.Param("id") == "" {
			allowed, err := model.IsBillingInvitationRecipient(model.DB, &row, c.GetInt("id"))
			if err != nil {
				teamFailure(c, err)
				return
			}
			if !allowed {
				continue
			}
		}
		if row.Status == "pending" && row.ExpiresAt <= time.Now().Unix() {
			row.Status = "expired"
		}
		var unit model.BillingUnit
		if err := model.DB.First(&unit, row.BillingUnitId).Error; err != nil {
			teamFailure(c, err)
			return
		}
		result = append(result, item{row, unit.Name})
	}
	common.ApiSuccess(c, gin.H{"items": result, "total": total})
}
func RespondTeamInvitation(c *gin.Context) {
	if !teamFeature(c, "BILLING_TEAM_SELF_SERVICE_ENABLED") {
		return
	}
	id, err := strconv.Atoi(c.Param("invitation_id"))
	if err != nil || id <= 0 {
		teamFailure(c, model.ErrTeamInvalid)
		return
	}
	var req struct {
		ExpectedUnitId *int `json:"expected_unit_id"`
	}
	if c.Param("action") == "accept" {
		if common.DecodeJson(c.Request.Body, &req) != nil || req.ExpectedUnitId == nil || *req.ExpectedUnitId < 0 {
			teamFailure(c, model.ErrTeamInvalid)
			return
		}
	}
	expected := 0
	if req.ExpectedUnitId != nil {
		expected = *req.ExpectedUnitId
	}
	if err = model.RespondBillingInvitation(id, c.GetInt("id"), c.Param("action"), expected); err != nil {
		teamFailure(c, err)
		return
	}
	teamAudit(c, "billing_team.invitation."+c.Param("action"), map[string]any{"invitation_id": id})
	common.ApiSuccess(c, nil)
}
func LeaveMyBillingTeam(c *gin.Context) {
	var req struct {
		ExpectedUnitId int `json:"expected_unit_id"`
	}
	if common.DecodeJson(c.Request.Body, &req) != nil || req.ExpectedUnitId <= 0 {
		teamFailure(c, model.ErrTeamInvalid)
		return
	}
	if err := model.LeaveBillingUnit(c.GetInt("id"), req.ExpectedUnitId); err != nil {
		teamFailure(c, err)
		return
	}
	teamAudit(c, "billing_team.leave", map[string]any{"unit_id": req.ExpectedUnitId})
	common.ApiSuccess(c, nil)
}
func GetTeamUsageSummary(c *gin.Context) {
	unit, ok := managedBillingUnit(c)
	if !ok {
		return
	}
	now := time.Now().UTC()
	startDefault := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	start, err := strconv.ParseInt(c.DefaultQuery("start", strconv.FormatInt(startDefault, 10)), 10, 64)
	if err != nil {
		teamFailure(c, model.ErrTeamInvalid)
		return
	}
	end, err := strconv.ParseInt(c.DefaultQuery("end", strconv.FormatInt(now.Unix()+1, 10)), 10, 64)
	if err != nil || start < 0 || end <= start || end-start > 366*86400 {
		teamFailure(c, model.ErrTeamInvalid)
		return
	}
	type row struct {
		UserId           int    `json:"user_id"`
		Quota            int64  `json:"quota"`
		PromptTokens     int64  `json:"prompt_tokens"`
		CompletionTokens int64  `json:"completion_tokens"`
		Requests         int64  `json:"requests"`
		Username         string `json:"username"`
		DisplayName      string `json:"display_name"`
	}
	rows := []row{}
	page := common.GetPageQuery(c)
	var total int64
	query := model.LOG_DB.Model(&model.Log{}).Where("billing_unit_id = ? AND type = ? AND created_at >= ? AND created_at < ?", unit.Id, model.LogTypeConsume, start, end)
	if err = query.Distinct("user_id").Count(&total).Error; err != nil {
		teamFailure(c, err)
		return
	}
	if err = query.Select("user_id, SUM(quota) AS quota, SUM(prompt_tokens) AS prompt_tokens, SUM(completion_tokens) AS completion_tokens, COUNT(*) AS requests").Group("user_id").Order("user_id").Offset(page.GetStartIdx()).Limit(page.GetPageSize()).Scan(&rows).Error; err != nil {
		teamFailure(c, err)
		return
	}
	for i := range rows {
		var u model.User
		if err = model.DB.Select("id, username, display_name").First(&u, rows[i].UserId).Error; err == nil {
			rows[i].Username = u.Username
			rows[i].DisplayName = u.DisplayName
		}
	}
	common.ApiSuccess(c, gin.H{"items": rows, "total": total, "start": start, "end": end})
}

func SelectOwnedBillingTeam(c *gin.Context) {
	if !teamFeature(c, "BILLING_TEAM_SELF_SERVICE_ENABLED") {
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	var req struct {
		ExpectedUnitId *int `json:"expected_unit_id"`
	}
	if err != nil || id <= 0 || common.DecodeJson(c.Request.Body, &req) != nil || req.ExpectedUnitId == nil || *req.ExpectedUnitId < 0 {
		teamFailure(c, model.ErrTeamInvalid)
		return
	}
	if err = model.SelectOwnedBillingUnit(c.GetInt("id"), id, *req.ExpectedUnitId); err != nil {
		teamFailure(c, err)
		return
	}
	teamAudit(c, "billing_team.select", map[string]any{"unit_id": id})
	common.ApiSuccess(c, nil)
}
func UpdateTeamBillingPreference(c *gin.Context) {
	var req struct {
		Enabled *bool `json:"personal_billing_fallback"`
	}
	if common.DecodeJson(c.Request.Body, &req) != nil || req.Enabled == nil {
		teamFailure(c, model.ErrTeamInvalid)
		return
	}
	if *req.Enabled && !teamFeature(c, "BILLING_TEAM_PERSONAL_FALLBACK_ENABLED") {
		return
	}
	if err := model.UpdatePersonalBillingFallback(c.GetInt("id"), *req.Enabled); err != nil {
		teamFailure(c, err)
		return
	}
	teamAudit(c, "billing_team.preference", map[string]any{"personal_billing_fallback": *req.Enabled})
	common.ApiSuccess(c, nil)
}
func RequestTeamEpay(c *gin.Context) {
	if !teamFeature(c, "BILLING_TEAM_TOPUP_ENABLED") || !requirePaymentCompliance(c) {
		return
	}
	unit, ok := managedBillingUnit(c)
	if !ok {
		return
	}
	c.Set("team_topup_unit_id", unit.Id)
	c.Set("team_topup_payer_id", unit.PayerUserId)
	RequestEpay(c)
}
func GetTeamTopups(c *gin.Context) {
	unit, ok := managedBillingUnit(c)
	if !ok {
		return
	}
	page := common.GetPageQuery(c)
	rows := []model.TopUp{}
	var total int64
	query := model.DB.Model(&model.TopUp{}).Where("billing_unit_id = ?", unit.Id)
	if err := query.Count(&total).Error; err != nil {
		teamFailure(c, err)
		return
	}
	if err := query.Order("id desc").Offset(page.GetStartIdx()).Limit(page.GetPageSize()).Find(&rows).Error; err != nil {
		teamFailure(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"items": rows, "total": total})
}

func QuoteTeamEpay(c *gin.Context) {
	if !teamFeature(c, "BILLING_TEAM_TOPUP_ENABLED") || !requirePaymentCompliance(c) {
		return
	}
	unit, ok := managedBillingUnit(c)
	if !ok {
		return
	}
	c.Set("team_topup_payer_id", unit.PayerUserId)
	RequestAmount(c)
}
