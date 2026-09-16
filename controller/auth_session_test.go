package controller

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAccountSwitchUsesSingleUseStateAndStartsFreshOIDCLogin(t *testing.T) {
	previousDB := model.DB
	previousRedis := common.RedisEnabled
	previousSecret := common.SessionSecret
	previousAddress := system_setting.ServerAddress
	previousOIDC := *system_setting.GetOIDCSettings()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.AuthFlow{}))
	model.DB = db
	common.RedisEnabled = false
	common.SessionSecret = "account-switch-test-secret"
	system_setting.ServerAddress = "https://account.example"
	*system_setting.GetOIDCSettings() = system_setting.OIDCSettings{
		Enabled: true, ClientId: "account-client",
		AuthorizationEndpoint: "https://sso.example/login/oauth/authorize",
		EndSessionEndpoint:    "https://sso.example/api/logout",
	}
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedis
		common.SessionSecret = previousSecret
		system_setting.ServerAddress = previousAddress
		*system_setting.GetOIDCSettings() = previousOIDC
	})

	user := &model.User{Username: "switch-user", Password: "unused", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1}
	require.NoError(t, db.Create(user).Error)
	bundle, err := service.CreateLoginSession(user.Id, "oidc", "127.0.0.1", "test-agent")
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/user/auth/switch-account", middleware.UserAuth(), StartAccountSwitch)
	router.GET("/api/user/auth/switch-account/callback", FinishAccountSwitch)
	start := httptest.NewRequest(http.MethodPost, "/api/user/auth/switch-account", nil)
	start.Header.Set("Authorization", "Bearer "+bundle.AccessToken)
	startResponse := httptest.NewRecorder()
	router.ServeHTTP(startResponse, start)
	require.Equal(t, http.StatusOK, startResponse.Code)
	var payload struct {
		RedirectURL string `json:"redirect_url"`
	}
	require.NoError(t, common.Unmarshal(startResponse.Body.Bytes(), &payload))
	logoutURL, err := url.Parse(payload.RedirectURL)
	require.NoError(t, err)
	assert.Equal(t, "https://sso.example/api/logout", logoutURL.Scheme+"://"+logoutURL.Host+logoutURL.Path)
	assert.Equal(t, "account-client", logoutURL.Query().Get("client_id"))
	assert.Equal(t, "https://account.example/api/user/auth/switch-account/callback", logoutURL.Query().Get("post_logout_redirect_uri"))
	require.NotEmpty(t, logoutURL.Query().Get("state"))

	callbackPath := "/api/user/auth/switch-account/callback?state=" + url.QueryEscape(logoutURL.Query().Get("state"))
	callbackResponse := httptest.NewRecorder()
	router.ServeHTTP(callbackResponse, httptest.NewRequest(http.MethodGet, callbackPath, nil))
	require.Equal(t, http.StatusSeeOther, callbackResponse.Code)
	authorizeURL, err := url.Parse(callbackResponse.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "https://sso.example/login/oauth/authorize", authorizeURL.Scheme+"://"+authorizeURL.Host+authorizeURL.Path)
	assert.Equal(t, "https://account.example/oauth/oidc", authorizeURL.Query().Get("redirect_uri"))
	require.NotEmpty(t, authorizeURL.Query().Get("state"))
	stored, err := model.GetUserSessionBySID(bundle.Session.SID)
	require.NoError(t, err)
	assert.Equal(t, model.UserSessionStatusRevoked, stored.Status)

	replay := httptest.NewRecorder()
	router.ServeHTTP(replay, httptest.NewRequest(http.MethodGet, callbackPath, nil))
	assert.Equal(t, http.StatusSeeOther, replay.Code)
	assert.Equal(t, "/sign-in?account_switch_error=1", replay.Header().Get("Location"))
}

func TestAuthLogoutRejectsRefreshCookieSessionMismatch(t *testing.T) {
	previousDB := model.DB
	previousRedis := common.RedisEnabled
	previousSecret := common.SessionSecret
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}))
	model.DB = db
	common.RedisEnabled = false
	common.SessionSecret = "auth-logout-mismatch-test-secret"
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedis
		common.SessionSecret = previousSecret
	})

	user := &model.User{
		Username: "logout-mismatch-user", Password: "unused", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, db.Create(user).Error)
	sessionA, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "agent-a")
	require.NoError(t, err)
	sessionB, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "agent-b")
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/auth/logout", nil)
	c.Request.Header.Set("Authorization", "Bearer "+sessionA.AccessToken)
	c.Request.Header.Set("X-Auth-Session", sessionA.Session.SID)
	c.Request.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: sessionB.RefreshToken})

	AuthLogout(c)

	assert.Equal(t, http.StatusConflict, recorder.Code)
	var response struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
	assert.Equal(t, "AUTH_SESSION_MISMATCH", response.Code)
	for _, sid := range []string{sessionA.Session.SID, sessionB.Session.SID} {
		stored, err := model.GetUserSessionBySID(sid)
		require.NoError(t, err)
		assert.Equal(t, model.UserSessionStatusActive, stored.Status)
	}
}

func TestWriteAuthSessionErrorMapsSessionGrowthLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name           string
		err            error
		expectedStatus int
		expectedCode   string
	}{
		{
			name:           "active session limit",
			err:            model.ErrUserSessionLimit,
			expectedStatus: http.StatusConflict,
			expectedCode:   "AUTH_SESSION_LIMIT",
		},
		{
			name:           "issuance limit",
			err:            model.ErrUserSessionIssuanceLimit,
			expectedStatus: http.StatusTooManyRequests,
			expectedCode:   "AUTH_SESSION_ISSUANCE_LIMIT",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			writeAuthSessionError(c, test.err)

			assert.Equal(t, test.expectedStatus, recorder.Code)
			var response struct {
				Success bool   `json:"success"`
				Code    string `json:"code"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			assert.False(t, response.Success)
			assert.Equal(t, test.expectedCode, response.Code)
		})
	}
}

func TestSessionLimitDoesNotRecordRejectedLoginAsSuccessful(t *testing.T) {
	previousDB := model.DB
	previousRedis := common.RedisEnabled
	previousActiveLimit := common.UserSessionActiveLimit
	previousIssuanceLimit := common.UserSessionIssuanceLimit
	previousIssuanceWindow := common.UserSessionIssuanceWindowSeconds
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.TwoFA{}, &model.PasskeyCredential{}))
	model.DB = db
	common.RedisEnabled = false
	common.UserSessionActiveLimit = 1
	common.UserSessionIssuanceLimit = 100
	common.UserSessionIssuanceWindowSeconds = int64(common.DefaultUserSessionIssuanceWindowSeconds)
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedis
		common.UserSessionActiveLimit = previousActiveLimit
		common.UserSessionIssuanceLimit = previousIssuanceLimit
		common.UserSessionIssuanceWindowSeconds = previousIssuanceWindow
	})

	const previousLastLoginAt = int64(123)
	user := &model.User{
		Username: "rejected-login-audit-user", Password: "unused", Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, LastLoginAt: previousLastLoginAt,
	}
	require.NoError(t, db.Create(user).Error)
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.UserSession{
		SID: "existing-active-session", UserID: user.Id, Version: 1, UserAuthVersion: user.AuthVersion,
		Status: model.UserSessionStatusActive, RefreshHash: "hash", LoginMethod: "password",
		CreatedAt: now, LastActiveAt: now, ExpiresAt: now + 3600,
	}).Error)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/login", nil)
	setupLogin(user, c)

	assert.Equal(t, http.StatusConflict, recorder.Code)
	var stored model.User
	require.NoError(t, db.First(&stored, user.Id).Error)
	assert.Equal(t, previousLastLoginAt, stored.LastLoginAt)
}
