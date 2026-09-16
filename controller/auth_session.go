package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const accountSwitchFlowTTL = 5 * time.Minute

func StartAccountSwitch(c *gin.Context) {
	setAuthNoStore(c)
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "code": "AUTH_SESSION_REQUIRED", "message": "authentication required"})
		return
	}
	settings := system_setting.GetOIDCSettings()
	if !settings.Enabled || strings.TrimSpace(settings.ClientId) == "" || strings.TrimSpace(settings.EndSessionEndpoint) == "" || strings.TrimSpace(settings.AuthorizationEndpoint) == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "ACCOUNT_SWITCH_UNAVAILABLE", "message": "account switching is unavailable"})
		return
	}
	target, err := url.Parse(settings.EndSessionEndpoint)
	if err != nil || (target.Scheme != "https" && target.Scheme != "http") || target.Host == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "ACCOUNT_SWITCH_UNAVAILABLE", "message": "account switching is unavailable"})
		return
	}
	state, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose:   model.AuthFlowPurposeAccountSwitch,
		Provider:  "oidc",
		Intent:    model.AuthFlowIntentLogin,
		UserId:    identity.UserID,
		SessionId: identity.SessionID,
		ExpiresAt: time.Now().Add(accountSwitchFlowTTL),
	})
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	callback := strings.TrimRight(system_setting.ServerAddress, "/") + "/api/user/auth/switch-account/callback"
	query := target.Query()
	query.Set("client_id", settings.ClientId)
	query.Set("post_logout_redirect_uri", callback)
	query.Set("state", state)
	target.RawQuery = query.Encode()
	c.JSON(http.StatusOK, gin.H{"redirect_url": target.String()})
}

func FinishAccountSwitch(c *gin.Context) {
	setAuthNoStore(c)
	state := strings.TrimSpace(c.Query("state"))
	flow, err := model.ConsumeAuthFlow(state, model.AuthFlowMatch{
		Purpose:  model.AuthFlowPurposeAccountSwitch,
		Provider: "oidc",
		Intent:   model.AuthFlowIntentLogin,
	})
	if err != nil {
		c.Redirect(http.StatusSeeOther, "/sign-in?account_switch_error=1")
		return
	}
	if _, err := model.RevokeUserSession(flow.UserId, flow.SessionId, "account_switch"); err != nil {
		writeAuthSessionError(c, err)
		return
	}
	service.ClearRefreshCookie(c)
	oauthState, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose:   model.AuthFlowPurposeOAuth,
		Provider:  "oidc",
		Intent:    model.AuthFlowIntentLogin,
		Payload:   "{}",
		ExpiresAt: time.Now().Add(10 * time.Minute),
	})
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	settings := system_setting.GetOIDCSettings()
	target, err := url.Parse(settings.AuthorizationEndpoint)
	if err != nil || target.Host == "" {
		c.Redirect(http.StatusSeeOther, "/sign-in?account_switch_error=1")
		return
	}
	query := target.Query()
	query.Set("client_id", settings.ClientId)
	query.Set("redirect_uri", strings.TrimRight(system_setting.ServerAddress, "/")+"/oauth/oidc")
	query.Set("response_type", "code")
	query.Set("scope", "openid profile email")
	query.Set("state", oauthState)
	target.RawQuery = query.Encode()
	c.Redirect(http.StatusSeeOther, target.String())
}

func RefreshAuth(c *gin.Context) {
	setAuthNoStore(c)
	rawRefreshToken, err := c.Cookie(service.RefreshCookieName)
	if err != nil || rawRefreshToken == "" {
		service.ClearRefreshCookie(c)
		writeAuthSessionError(c, service.ErrRefreshTokenInvalid)
		return
	}
	bundle, user, err := service.RefreshLoginSession(rawRefreshToken, c.GetHeader("X-Auth-Session"), c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		if errors.Is(err, service.ErrRefreshTokenInvalid) || errors.Is(err, service.ErrLoginSessionRevoked) {
			service.ClearRefreshCookie(c)
		}
		writeAuthSessionError(c, err)
		return
	}
	service.WriteRefreshCookie(c, bundle.RefreshToken)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"access_token":      bundle.AccessToken,
			"token_type":        bundle.TokenType,
			"access_expires_at": bundle.AccessExpiresAt,
			"user":              buildSelfUserData(user),
			"session":           bundle.Session,
		},
	})
}

func AuthLogout(c *gin.Context) {
	setAuthNoStore(c)
	expectedSID := strings.TrimSpace(c.GetHeader("X-Auth-Session"))
	rawRefreshToken, cookieErr := c.Cookie(service.RefreshCookieName)
	cookieSID, hasCookieSID := service.RefreshTokenSID(rawRefreshToken)
	if expectedSID != "" && cookieErr == nil && hasCookieSID && cookieSID != expectedSID {
		writeAuthSessionError(c, service.ErrLoginSessionMismatch)
		return
	}

	if rawAccessToken, ok := dashboardBearer(c.GetHeader("Authorization")); ok {
		if identity, err := service.ParseAccessToken(rawAccessToken); err == nil {
			if expectedSID != "" && expectedSID != identity.SessionID {
				writeAuthSessionError(c, service.ErrLoginSessionMismatch)
				return
			}
			if _, err := model.RevokeUserSession(identity.UserID, identity.SessionID, "logout"); err != nil {
				writeAuthSessionError(c, err)
				return
			}
			cookieCleared := false
			if cookieErr == nil && hasCookieSID && cookieSID == identity.SessionID {
				if err := service.RevokeByRefreshToken(rawRefreshToken, identity.SessionID, "logout"); err != nil {
					writeAuthSessionError(c, err)
					return
				}
				service.ClearRefreshCookie(c)
				cookieCleared = true
			}
			c.JSON(http.StatusOK, gin.H{
				"success": true,
				"message": "",
				"data":    gin.H{"revoked_sid": identity.SessionID, "cookie_cleared": cookieCleared},
			})
			return
		}
	}
	if cookieErr != nil || rawRefreshToken == "" {
		service.ClearRefreshCookie(c)
		c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
		return
	}
	if err := service.RevokeByRefreshToken(rawRefreshToken, expectedSID, "logout"); err != nil {
		writeAuthSessionError(c, err)
		return
	}
	service.ClearRefreshCookie(c)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": ""})
}

func GetLoginSessions(c *gin.Context) {
	identity, ok := requireBrowserSession(c)
	if !ok {
		return
	}
	sessions, err := service.ListLoginSessions(identity.UserID, identity.SessionID)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": sessions})
}

func DeleteLoginSession(c *gin.Context) {
	identity, ok := requireBrowserSession(c)
	if !ok {
		return
	}
	sid := strings.TrimSpace(c.Param("sid"))
	if sid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "code": "AUTH_SESSION_ID_REQUIRED", "message": "session id is required"})
		return
	}
	revoked, err := model.RevokeUserSession(identity.UserID, sid, "user_revoked")
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	if !revoked {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "code": "AUTH_SESSION_NOT_FOUND", "message": "session not found"})
		return
	}
	if rawRefreshToken, cookieErr := c.Cookie(service.RefreshCookieName); cookieErr == nil {
		cookieSID, ok := service.RefreshTokenSID(rawRefreshToken)
		if ok && cookieSID == sid {
			service.ClearRefreshCookie(c)
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"revoked_sid": sid, "current": sid == identity.SessionID}})
}

func RevokeOtherLoginSessions(c *gin.Context) {
	identity, ok := requireBrowserSession(c)
	if !ok {
		return
	}
	count, err := model.RevokeOtherUserSessions(identity.UserID, identity.SessionID, "user_revoked_others")
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": gin.H{"revoked_count": count}})
}

func requireBrowserSession(c *gin.Context) (service.AuthIdentity, bool) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"code":    "AUTH_SESSION_REQUIRED",
			"message": "a dashboard login session is required",
		})
		return service.AuthIdentity{}, false
	}
	return identity, true
}

func writeAuthSessionError(c *gin.Context, err error) {
	status, code := service.AuthSessionErrorCode(err)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		status, code = http.StatusUnauthorized, "AUTH_UNAUTHORIZED"
	}
	if status == http.StatusInternalServerError {
		// The response body only carries the generic AUTH_INTERNAL_ERROR
		// code; without this log the underlying Redis/database/session
		// failure is indistinguishable from the client side.
		logger.LogError(c.Request.Context(), fmt.Sprintf("auth session internal error (%s %s): %v", c.Request.Method, c.Request.URL.Path, err))
	}
	c.JSON(status, gin.H{"success": false, "code": code, "message": http.StatusText(status)})
}

func setAuthNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
}

func authRotationData(bundle *service.AuthBundle) gin.H {
	return gin.H{
		"access_token":      bundle.AccessToken,
		"token_type":        bundle.TokenType,
		"access_expires_at": bundle.AccessExpiresAt,
		"session":           bundle.Session,
	}
}

func dashboardBearer(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}
