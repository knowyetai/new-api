package router

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// accessTokenExemptRoutes are /api routes that never reach dashboard
// authentication, so a personal access token has no rule to satisfy there.
var accessTokenExemptRoutes = []string{
	// Anonymous pages and setup.
	"GET /api/setup",
	"POST /api/setup",
	"GET /api/status",
	"GET /api/uptime/status",
	"GET /api/notice",
	"GET /api/user-agreement",
	"GET /api/privacy-policy",
	"GET /api/about",
	"GET /api/home_page_content",
	"GET /api/ratio_config",
	"GET /api/user/groups",

	// Anonymous account entry points.
	"GET /api/verification",
	"GET /api/reset_password",
	"POST /api/user/reset",
	"POST /api/user/register",
	"GET /api/user/login/encryption-key",
	"POST /api/user/login",
	"POST /api/user/login/2fa",
	"POST /api/user/login/verify",
	"POST /api/user/login/passkey/begin",
	"POST /api/user/login/passkey/finish",
	"POST /api/user/passkey/login/begin",
	"POST /api/user/passkey/login/finish",
	"GET /api/oauth/wechat",
	"GET /api/oauth/telegram/login",
	"GET /api/oauth/telegram/bind/:flow_token",

	// Browser session cookie endpoints.
	"POST /api/user/auth/refresh",
	"POST /api/user/auth/logout",

	// Payment callbacks.
	"POST /api/stripe/webhook",
	"POST /api/creem/webhook",
	"POST /api/waffo/webhook",
	"POST /api/waffo-pancake/webhook/:env",
	"POST /api/user/lianlian/notify",
	"GET /api/user/epay/notify",
	"POST /api/user/epay/notify",
	"GET /api/subscription/epay/notify",
	"POST /api/subscription/epay/notify",
	"GET /api/subscription/epay/return",
	"POST /api/subscription/epay/return",

	// API key authentication (TokenAuthReadOnly).
	"GET /api/usage/token/",
	"GET /api/log/token",
}

// accessTokenHelperRoutes are declared through handlePermissionRoute outside
// channelPermissionRoutes.
var accessTokenHelperRoutes = map[string]authz.Permission{
	"GET /api/task_plugin_options": authz.TaskPluginBind,
	"GET /api/audit":               authz.AuditRead,
}

func newAccessTokenScopeTestEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// A permission route that is also in the static table panics here.
	require.NotPanics(t, func() {
		SetApiRouter(engine)
		SetDashboardRouter(engine)
		SetRelayRouter(engine)
		SetTaskPluginProtocolRouter(engine)
		SetVideoRouter(engine)
		SetTaskRouter(engine)
	})
	return engine
}

func TestAccessTokenRouteRulesCoverEveryDashboardRoute(t *testing.T) {
	engine := newAccessTokenScopeTestEngine(t)
	exempt := map[string]bool{}
	for _, key := range accessTokenExemptRoutes {
		exempt[key] = true
	}
	registered := map[string]bool{}
	panel := map[string]bool{}
	for _, route := range engine.Routes() {
		if !strings.HasPrefix(route.Path, "/api/") && !strings.HasPrefix(route.Path, "/pg/") {
			continue
		}
		key := route.Method + " " + route.Path
		registered[key] = true
		if !exempt[key] {
			panel[key] = true
		}
	}
	for _, key := range accessTokenExemptRoutes {
		assert.True(t, registered[key], "exempt route %s is no longer registered", key)
	}

	declared := map[string]int{}
	for _, key := range middleware.AccessTokenRouteRuleKeys() {
		declared[key]++
	}
	for key := range panel {
		assert.Equal(t, 1, declared[key], "panel route %s must be declared exactly once", key)
	}
	for key := range declared {
		assert.True(t, panel[key], "declared route %s is not a registered panel route", key)
	}

	permissionRoutes := map[string]authz.Permission{}
	for _, route := range channelPermissionRoutes {
		permissionRoutes[route.method+" "+joinPaths("/api/channel", route.path)] = route.permission
	}
	for key, permission := range accessTokenHelperRoutes {
		permissionRoutes[key] = permission
	}
	for key, permission := range permissionRoutes {
		rule, ok := middleware.AccessTokenRouteRule(key)
		require.True(t, ok, key)
		assert.Equal(t, "scope", rule.Kind(), key)
		assert.Equal(t, service.AccessTokenScopeOf(permission), rule.Scope(), key)
	}
}

func TestAccessTokenCatalogCoversEveryPermission(t *testing.T) {
	catalog := service.AccessTokenCatalog(1, common.RoleRootUser)
	scopes := map[string]bool{}
	resources := map[string]int{}
	for _, group := range catalog {
		for _, resource := range group.Resources {
			resources[resource.Resource]++
			for _, action := range resource.Actions {
				scopes[resource.Resource+":"+action.Action] = true
			}
		}
	}
	for _, permission := range authz.AllPermissions() {
		assert.True(t, scopes[service.AccessTokenScopeOf(permission)], "root catalog lacks %s", service.AccessTokenScopeOf(permission))
	}
	for resource, count := range resources {
		assert.Equal(t, 1, count, "resource %s appears in more than one catalog entry", resource)
	}
}

func TestModelCredentialProvisioningWithDedicatedAccessToken(t *testing.T) {
	const legacyFingerprintEnv = "MODEL_CREDENTIALS_LEGACY_TOKEN_SHA256"
	t.Setenv(legacyFingerprintEnv, "")
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousLoginEncryption := common.PasswordLoginEncryptionEnabled
	common.PasswordLoginEncryptionEnabled = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.PasswordLoginEncryptionEnabled = previousLoginEncryption
	})
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(
		&model.Option{}, &model.UserSession{}, &model.UserAccessToken{}, &model.AuthFlow{},
		&model.TwoFA{}, &model.PasskeyCredential{}, &model.AuditLog{},
		&model.CustomOAuthProvider{}, &model.UserOAuthBinding{}, &model.BillingUnit{}, &model.BillingUnitMember{},
	))
	require.NoError(t, model.EnsureLegacyAccessTokenRetireAt(common.GetTimestamp()))

	const (
		provisioningPath  = "/api/integrations/model-credentials"
		provisioningScope = "model_credentials:write"
		issuer            = "https://identity.example.invalid"
		password          = "provisioning-test-password"
		legacy            = "provisioning-legacy-test-token"
	)
	passwordHash, err := common.Password2Hash(password)
	require.NoError(t, err)
	legacyToken := legacy
	root := model.User{Username: "provisioning-root", Group: "default", Role: common.RoleRootUser,
		Status: common.UserStatusEnabled, AuthVersion: 1, AffCode: "test-root", Password: passwordHash, AccessToken: &legacyToken}
	admin := model.User{Username: "provisioning-admin", Group: "default", Role: common.RoleAdminUser,
		Status: common.UserStatusEnabled, AuthVersion: 1, AffCode: "test-admin"}
	require.NoError(t, model.DB.Create(&root).Error)
	require.NoError(t, model.DB.Create(&admin).Error)
	rootSession, err := service.CreateLoginSession(root.Id, "password", "127.0.0.1", "integration test")
	require.NoError(t, err)
	adminSession, err := service.CreateLoginSession(admin.Id, "password", "127.0.0.1", "integration test")
	require.NoError(t, err)
	provider := model.CustomOAuthProvider{Name: "Integration SSO", Slug: "integration-sso", Enabled: true,
		WellKnown: issuer + "/.well-known/openid-configuration", UserIdField: "sub"}
	require.NoError(t, model.DB.Create(&provider).Error)

	engine := newAccessTokenScopeTestEngine(t)
	type responseBody struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Data    struct {
			ProofToken string `json:"proof_token"`
			Token      string `json:"token"`
			UserID     int    `json:"user_id"`
			TokenID    int    `json:"token_id"`
			Key        string `json:"key"`
			Item       struct {
				ID int `json:"id"`
			} `json:"item"`
		} `json:"data"`
	}
	request := func(t *testing.T, method, path, bearer, proof string, body map[string]any, status int) responseBody {
		t.Helper()
		payload, err := common.Marshal(body)
		require.NoError(t, err)
		req := httptest.NewRequest(method, path, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if proof != "" {
			req.Header.Set("X-Security-Proof", proof)
		}
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		var result responseBody
		require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &result))
		require.Equal(t, status, rec.Code, "%s %s returned code %s", method, path, result.Code)
		return result
	}

	grant := map[string]any{"scopes": []string{provisioningScope}, "expires_at": common.GetTimestamp() + 7200}
	tokenRequest := map[string]any{"name": "Know Engine", "scopes": grant["scopes"], "expires_at": grant["expires_at"]}
	const tokenPath = "/api/user/access_tokens"
	result := request(t, http.MethodPost, tokenPath, adminSession.AccessToken, "", tokenRequest, http.StatusBadRequest)
	assert.Equal(t, "ACCESS_TOKEN_SCOPE_FORBIDDEN", result.Code)
	mixedGrant := map[string]any{"name": "Mixed grant", "scopes": []string{provisioningScope, "user:write"}}
	result = request(t, http.MethodPost, tokenPath, rootSession.AccessToken, "", mixedGrant, http.StatusBadRequest)
	assert.Equal(t, "ACCESS_TOKEN_SCOPE_INVALID", result.Code)

	proof := request(t, http.MethodPost, "/api/verify", rootSession.AccessToken, "",
		map[string]any{"method": "password", "scope": service.VerificationScopeAccessTokenGenerate, "context": grant, "password": password},
		http.StatusOK)
	require.True(t, proof.Success)
	require.NotEmpty(t, proof.Data.ProofToken)
	issued := request(t, http.MethodPost, tokenPath, rootSession.AccessToken, proof.Data.ProofToken, tokenRequest, http.StatusOK)
	require.True(t, issued.Success)
	require.True(t, strings.HasPrefix(issued.Data.Token, model.AccessTokenPrefix))
	require.Positive(t, issued.Data.Item.ID)
	dedicated := issued.Data.Token

	ordinary := model.AccessTokenPrefix + "provisioning-ordinary-test-token"
	ordinaryToken := model.UserAccessToken{TokenHash: model.AccessTokenFingerprint(ordinary)}
	require.NoError(t, ordinaryToken.SetScopes([]string{"user:write"}))
	require.NoError(t, model.CreateUserAccessToken(root.Id, &ordinaryToken, service.AccessTokenMaxPerUser))
	stale := model.AccessTokenPrefix + "provisioning-former-root-test-token"
	staleToken := model.UserAccessToken{TokenHash: model.AccessTokenFingerprint(stale)}
	require.NoError(t, staleToken.SetScopes([]string{provisioningScope}))
	require.NoError(t, model.CreateUserAccessToken(admin.Id, &staleToken, service.AccessTokenMaxPerUser))

	identity := map[string]any{"provider_id": provider.Id, "issuer": issuer, "subject": "casdoor-subject", "display_name": "SSO user"}
	for _, test := range []struct {
		name, bearer, code string
		status             int
	}{
		{"anonymous caller", "", "AUTH_UNAUTHORIZED", http.StatusUnauthorized},
		{"legacy root token", legacy, "ACCESS_TOKEN_SCOPE_DENIED", http.StatusForbidden},
		{"root token with user write", ordinary, "ACCESS_TOKEN_SCOPE_DENIED", http.StatusForbidden},
		{"token owned by a former root", stale, "AUTH_INSUFFICIENT_PRIVILEGE", http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := request(t, http.MethodPost, provisioningPath, test.bearer, "", identity, test.status)
			assert.Equal(t, test.code, result.Code)
		})
	}
	result = request(t, http.MethodGet, "/api/verify/methods", dedicated, "", nil, http.StatusForbidden)
	assert.Equal(t, "ACCESS_TOKEN_SCOPE_DENIED", result.Code)
	var bindings int64
	require.NoError(t, model.DB.Model(&model.UserOAuthBinding{}).Count(&bindings).Error)
	assert.Zero(t, bindings)

	first := request(t, http.MethodPost, provisioningPath, dedicated, "", identity, http.StatusOK)
	require.True(t, first.Success)
	require.Positive(t, first.Data.UserID)
	require.Positive(t, first.Data.TokenID)
	require.True(t, strings.HasPrefix(first.Data.Key, "sk-"))
	for _, bearer := range []string{dedicated, rootSession.AccessToken} {
		again := request(t, http.MethodPost, provisioningPath, bearer, "", identity, http.StatusOK)
		require.True(t, again.Success)
		assert.Equal(t, first.Data.UserID, again.Data.UserID)
		assert.Equal(t, first.Data.TokenID, again.Data.TokenID)
		assert.True(t, first.Data.Key == again.Data.Key, "provisioning changed the model credential")
	}
	require.NoError(t, model.DB.Model(&model.UserOAuthBinding{}).Count(&bindings).Error)
	assert.EqualValues(t, 1, bindings)

	modelRequest := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelRequest.Header.Set("Authorization", "Bearer "+first.Data.Key)
	modelResponse := httptest.NewRecorder()
	engine.ServeHTTP(modelResponse, modelRequest)
	assert.Equal(t, http.StatusOK, modelResponse.Code)

	var audits []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("route = ? AND category = ? AND success = ?",
		provisioningPath, model.AuditCategoryAccessToken, true).Find(&audits).Error)
	require.Len(t, audits, 2)
	auditData, err := common.Marshal(audits)
	require.NoError(t, err)
	assert.False(t, bytes.Contains(auditData, []byte(dedicated)) || bytes.Contains(auditData, []byte(first.Data.Key)),
		"provisioning audit includes a credential")

	require.NoError(t, model.DB.Model(&model.UserAccessToken{}).Where("id = ?", issued.Data.Item.ID).
		Update("expires_at", common.GetTimestamp()-1).Error)
	result = request(t, http.MethodPost, provisioningPath, dedicated, "", identity, http.StatusUnauthorized)
	assert.Equal(t, "ACCESS_TOKEN_EXPIRED", result.Code)
	_, err = model.DeleteUserAccessToken(root.Id, issued.Data.Item.ID)
	require.NoError(t, err)
	result = request(t, http.MethodPost, provisioningPath, dedicated, "", identity, http.StatusUnauthorized)
	assert.Equal(t, "AUTH_UNAUTHORIZED", result.Code)

	t.Run("existing service credential survives the dashboard transition", func(t *testing.T) {
		deadline := model.LegacyAccessTokenRetireAt()
		option := model.DB.Model(&model.Option{}).Where(&model.Option{Key: "LegacyAccessTokenRetireAt"})
		t.Cleanup(func() {
			require.NoError(t, option.Update("value", strconv.FormatInt(deadline, 10)).Error)
			require.NoError(t, model.EnsureLegacyAccessTokenRetireAt(common.GetTimestamp()))
		})

		t.Setenv(legacyFingerprintEnv, model.AccessTokenFingerprint(legacy))
		before := request(t, http.MethodPost, provisioningPath, legacy, "", identity, http.StatusOK)
		require.True(t, before.Success)
		require.NoError(t, option.Update("value", strconv.FormatInt(common.GetTimestamp()-1, 10)).Error)
		require.NoError(t, model.EnsureLegacyAccessTokenRetireAt(common.GetTimestamp()))
		after := request(t, http.MethodPost, provisioningPath, legacy, "", identity, http.StatusOK)
		require.True(t, after.Success)
		assert.Equal(t, first.Data.UserID, after.Data.UserID)
		assert.Equal(t, first.Data.TokenID, after.Data.TokenID)

		otherRoute := request(t, http.MethodGet, "/api/verify/methods", legacy, "", nil, http.StatusUnauthorized)
		assert.Equal(t, "ACCESS_TOKEN_LEGACY_RETIRED", otherRoute.Code)
		otherToken := request(t, http.MethodPost, provisioningPath, strings.ToUpper(legacy), "", identity, http.StatusUnauthorized)
		assert.Equal(t, "ACCESS_TOKEN_LEGACY_RETIRED", otherToken.Code)

		require.NoError(t, model.DB.Model(&root).Update("role", common.RoleAdminUser).Error)
		downgraded := request(t, http.MethodPost, provisioningPath, legacy, "", identity, http.StatusForbidden)
		assert.Equal(t, "AUTH_INSUFFICIENT_PRIVILEGE", downgraded.Code)
		require.NoError(t, model.DB.Model(&root).Update("role", common.RoleRootUser).Error)

		t.Setenv(legacyFingerprintEnv, "")
		unpinned := request(t, http.MethodPost, provisioningPath, legacy, "", identity, http.StatusUnauthorized)
		assert.Equal(t, "ACCESS_TOKEN_LEGACY_RETIRED", unpinned.Code)
		t.Setenv(legacyFingerprintEnv, model.AccessTokenFingerprint(legacy))

		require.NoError(t, model.DB.Model(&root).Update("access_token", strings.ToUpper(legacy)).Error)
		rotated := request(t, http.MethodPost, provisioningPath, legacy, "", identity, http.StatusUnauthorized)
		assert.Equal(t, "AUTH_UNAUTHORIZED", rotated.Code)
		require.NoError(t, model.DB.Model(&root).Update("access_token", legacy).Error)
		_, err := model.RevokeUserAccessToken(root.Id)
		require.NoError(t, err)
		revoked := request(t, http.MethodPost, provisioningPath, legacy, "", identity, http.StatusUnauthorized)
		assert.Equal(t, "AUTH_UNAUTHORIZED", revoked.Code)
	})
}

func TestAccessTokenVerificationScopesAreAllListed(t *testing.T) {
	files, err := filepath.Glob("../service/*.go")
	require.NoError(t, err)
	fileSet := token.NewFileSet()
	var scopes []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, file, nil, 0)
		require.NoError(t, err)
		for _, declaration := range parsed.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, spec := range general.Specs {
				value := spec.(*ast.ValueSpec)
				for i, name := range value.Names {
					if !strings.HasPrefix(name.Name, "VerificationScope") || i >= len(value.Values) {
						continue
					}
					literal, ok := value.Values[i].(*ast.BasicLit)
					require.True(t, ok, name.Name)
					scope, err := strconv.Unquote(literal.Value)
					require.NoError(t, err)
					scopes = append(scopes, scope)
				}
			}
		}
	}
	require.NotEmpty(t, scopes)
	for _, scope := range scopes {
		_, listed := service.AccessTokenVerificationScope(scope)
		assert.True(t, listed, "verification scope %s has no access token rule", scope)
	}
}
