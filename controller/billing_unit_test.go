package controller

import (
	"bytes"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type legacyBillingLog struct {
	Id     int `gorm:"primaryKey"`
	UserId int
	Quota  int
}

func (legacyBillingLog) TableName() string { return "logs" }

type legacyIntegrationBinding struct {
	Id             int `gorm:"primaryKey"`
	UserId         int
	ProviderId     int
	ProviderUserId string `gorm:"type:varchar(256)"`
	CreatedAt      time.Time
}

func (legacyIntegrationBinding) TableName() string { return "user_oauth_bindings" }

func TestBillingUnitAPIAndSettlement(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			env := "TEST_MYSQL_DSN"
			if kind == "postgres" {
				env = "TEST_POSTGRES_DSN"
			}
			if kind != "sqlite" && os.Getenv(env) == "" {
				t.Skip("local database DSN required")
			}
			oldDB, oldLog := model.DB, model.LOG_DB
			oldMain, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
			oldRedis, oldBatch := common.RedisEnabled, common.BatchUpdateEnabled
			t.Cleanup(func() {
				model.DB, model.LOG_DB = oldDB, oldLog
				common.SetDatabaseTypes(oldMain, oldLogType)
				common.RedisEnabled, common.BatchUpdateEnabled = oldRedis, oldBatch
			})
			db, _ := newAuditTestDatabase(t, kind, os.Getenv(env))
			logDB, _ := newAuditTestDatabase(t, kind, os.Getenv(env))
			model.DB, model.LOG_DB = db, logDB
			common.RedisEnabled = false
			common.BatchUpdateEnabled = false
			typ := common.DatabaseTypeSQLite
			if kind == "mysql" {
				typ = common.DatabaseTypeMySQL
			}
			if kind == "postgres" {
				typ = common.DatabaseTypePostgreSQL
			}
			common.SetDatabaseTypes(typ, typ)
			versionSQL := "SELECT version()"
			if kind == "sqlite" {
				versionSQL = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(versionSQL).Scan(&version).Error)
			t.Log("database", version)
			require.NoError(t, db.AutoMigrate(&legacyIntegrationBinding{}))
			require.NoError(t, db.Create(&legacyIntegrationBinding{Id: 100, UserId: 2, ProviderId: 1, ProviderUserId: "bound-before-upgrade"}).Error)
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserOAuthBinding{}, &model.CustomOAuthProvider{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}))
			require.NoError(t, logDB.AutoMigrate(&legacyBillingLog{}))
			require.NoError(t, logDB.Create(&legacyBillingLog{UserId: 2, Quota: 17}).Error)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.BillingUnit{}, &model.BillingUnitMember{}))
				require.NoError(t, logDB.AutoMigrate(&model.Log{}, &model.AuditLog{}))
			}
			var historical model.Log
			require.NoError(t, logDB.First(&historical).Error)
			assert.Equal(t, 17, historical.Quota)
			assert.Zero(t, historical.BillingUserId)
			for _, u := range []model.User{{Id: 1, Username: "owner", Role: 1, Status: 1, Quota: 0, AffCode: "owner"}, {Id: 2, Username: "alice", Role: 1, Status: 1, Quota: 50, AffCode: "alice"}, {Id: 3, Username: "bob", Role: 1, Status: 1, Quota: 0, AffCode: "bob"}, {Id: 4, Username: "payer", Role: 1, Status: 1, Quota: 1000, AffCode: "payer"}, {Id: 5, Username: "other", Role: 1, Status: 1, Quota: 0, AffCode: "other"}} {
				require.NoError(t, db.Create(&u).Error)
			}
			router := gin.New()
			actor, role := 1, common.RoleAdminUser
			router.Use(func(c *gin.Context) { c.Set("id", actor); c.Set("role", role) })
			router.POST("/units", CreateBillingUnit)
			router.GET("/units/:id", GetBillingUnit)
			router.PUT("/units/:id/members/:user_id", BillingUnitMembers)
			router.DELETE("/units/:id/members/:user_id", BillingUnitMembers)
			router.GET("/units/:id/usage", BillingUnitUsage)
			request := func(method, path, body string) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(w, req)
				return w
			}
			assert.Equal(t, 200, request("POST", "/units", `{"name":"team","owner_user_id":1,"payer_user_id":4}`).Code)
			assert.Equal(t, 409, request("POST", "/units", `{"name":"duplicate","owner_user_id":1,"payer_user_id":4}`).Code)
			role = common.RoleCommonUser
			assert.Equal(t, 403, request("POST", "/units", `{}`).Code)
			assert.Equal(t, 200, request("PUT", "/units/1/members/2", "").Code)
			assert.Equal(t, 200, request("PUT", "/units/1/members/2", "").Code)
			assert.Equal(t, 409, request("PUT", "/units/1/members/4", "").Code)
			actor = 5
			assert.Equal(t, 404, request("GET", "/units/1", "").Code)
			assert.Equal(t, 404, request("PUT", "/units/1/members/3", "").Code)
			actor = 1
			unit, err := model.ResolveBillingUnit(2)
			require.NoError(t, err)
			require.Equal(t, 4, unit.PayerUserId)
			token := model.Token{UserId: 2, Key: "local-unit-test", Status: 1, Name: "alice", RemainQuota: 1000, ExpiredTime: -1}
			require.NoError(t, db.Create(&token).Error)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Set("token_quota", 1000)
			info := &relaycommon.RelayInfo{UserId: 2, BillingUserId: 4, BillingUnitId: unit.Id, TokenId: token.Id, TokenKey: token.Key, ForcePreConsume: true, RequestId: "unit-test", OriginModelName: "mock"}
			session, apiErr := service.NewBillingSession(ctx, info, 200)
			require.Nil(t, apiErr)
			assert.Equal(t, 200, request("DELETE", "/units/1/members/2", "").Code)
			require.NoError(t, session.Settle(140))
			require.NoError(t, session.Settle(140))
			payer, err := model.GetUserById(4, false)
			require.NoError(t, err)
			assert.Equal(t, 860, payer.Quota)
			alice, err := model.GetUserById(2, false)
			require.NoError(t, err)
			assert.Equal(t, 50, alice.Quota)
			unit, err = model.ResolveBillingUnit(2)
			require.NoError(t, err)
			assert.Nil(t, unit)
			// A failure refunds the original payer even after membership changes.
			info.RequestId = "unit-refund"
			session, apiErr = service.NewBillingSession(ctx, info, 100)
			require.Nil(t, apiErr)
			session.Refund(ctx)
			require.Eventually(t, func() bool { u, e := model.GetUserById(4, false); return e == nil && u.Quota == 860 }, time.Second*3, time.Millisecond*20)
			session.Refund(ctx)
			// Source exhaustion cannot fall back to the consumer wallet.
			_, apiErr = service.NewBillingSession(ctx, info, 900)
			require.NotNil(t, apiErr)
			require.NoError(t, logDB.Create(&model.Log{UserId: 2, BillingUserId: 4, BillingUnitId: 1, Type: model.LogTypeConsume, Quota: 140}).Error)
			require.NoError(t, logDB.Create(&model.Log{UserId: 5, BillingUserId: 5, Type: model.LogTypeConsume, Quota: 999}).Error)
			visible := request("GET", "/units/1/usage", "")
			assert.Equal(t, 200, visible.Code)
			assert.Contains(t, visible.Body.String(), `"total":1`)
			assert.NotContains(t, visible.Body.String(), "999")
			actor = 5
			assert.Equal(t, 404, request("GET", "/units/1/usage", "").Code)
			// Subscription funding belongs to the payer, never the consumer.
			plan := model.SubscriptionPlan{Title: "shared", Currency: "USD", Enabled: true}
			require.NoError(t, db.Create(&plan).Error)
			sub := model.UserSubscription{UserId: 4, PlanId: plan.Id, AmountTotal: 1000, Status: "active", StartTime: time.Now().Unix(), EndTime: time.Now().Add(time.Hour).Unix()}
			require.NoError(t, db.Create(&sub).Error)
			require.NoError(t, db.Model(&model.User{}).Where("id = ?", 4).Update("setting", `{"billing_preference":"subscription_only"}`).Error)
			info.RequestId = "unit-subscription"
			session, apiErr = service.NewBillingSession(ctx, info, 100)
			require.Nil(t, apiErr)
			require.Equal(t, sub.Id, info.SubscriptionId)
			require.NoError(t, session.Settle(140))
			require.NoError(t, db.First(&sub, sub.Id).Error)
			assert.EqualValues(t, 140, sub.AmountUsed)
			payer, err = model.GetUserById(4, false)
			require.NoError(t, err)
			assert.Equal(t, 860, payer.Quota)

			// Explicit fixture IDs do not advance PostgreSQL sequences.
			if kind == "postgres" {
				require.NoError(t, db.Exec("SELECT setval(pg_get_serial_sequence('user_oauth_bindings','id'), (SELECT MAX(id) FROM user_oauth_bindings))").Error)
				require.NoError(t, db.Exec("SELECT setval(pg_get_serial_sequence('users','id'), (SELECT MAX(id) FROM users))").Error)
			}
			// Trusted SSO provisioning reuses native identities and never resurrects credentials.
			provider := model.CustomOAuthProvider{Name: "Local", Slug: "local", Enabled: true, WellKnown: "http://127.0.0.1:18001/.well-known/openid-configuration", UserIdField: "sub"}
			require.NoError(t, db.Create(&provider).Error)
			router.POST("/credential", ResolveModelCredential)
			payload := fmt.Sprintf(`{"provider_id":%d,"issuer":"http://127.0.0.1:18001","subject":"native-alice","display_name":" 张三 "}`, provider.Id)
			assert.Equal(t, 403, request("POST", "/credential", payload).Code)
			role = common.RoleRootUser
			require.Equal(t, 200, request("POST", "/credential", payload).Code)
			first, err := model.ResolveIntegrationCredential(provider.Id, "http://127.0.0.1:18001", "native-alice")
			require.NoError(t, err)
			second, err := model.ResolveIntegrationCredential(provider.Id, "http://127.0.0.1:18001", "native-alice")
			require.NoError(t, err)
			assert.Equal(t, first.Id, second.Id)
			assert.Equal(t, first.UserId, second.UserId)
			var bindings int64
			require.NoError(t, db.Model(&model.UserOAuthBinding{}).Where("provider_id = ?", provider.Id).Count(&bindings).Error)
			assert.EqualValues(t, 2, bindings)

			// Profile synchronization is independent of identity and billing.
			before, err := model.GetUserById(first.UserId, false)
			require.NoError(t, err)
			assert.Equal(t, "张三", before.DisplayName)
			renamed := strings.Replace(payload, " 张三 ", "李四", 1)
			require.Equal(t, 200, request("POST", "/credential", renamed).Code)
			require.NoError(t, model.SyncIntegrationDisplayName(provider.Id, "native-alice", "王五"))
			for _, name := range []string{"", "  "} {
				_, err = model.ResolveIntegrationCredential(provider.Id, "http://127.0.0.1:18001", "native-alice", name)
				require.NoError(t, err)
				require.NoError(t, model.SyncIntegrationDisplayName(provider.Id, "native-alice", name))
			}
			after, err := model.GetUserById(first.UserId, false)
			require.NoError(t, err)
			assert.Equal(t, "王五", after.DisplayName)
			after.DisplayName = before.DisplayName
			assert.Equal(t, before, after)
			again, err := model.ResolveIntegrationCredential(provider.Id, "http://127.0.0.1:18001", "native-alice")
			require.NoError(t, err)
			assert.Equal(t, first, again)
			other, err := model.ResolveIntegrationCredential(provider.Id, "http://127.0.0.1:18001", "native-bob", "王五")
			require.NoError(t, err)
			assert.NotEqual(t, first.UserId, other.UserId)
			assert.NotEqual(t, first.Id, other.Id)
			require.NoError(t, model.SyncIntegrationDisplayName(provider.Id, "native-bob", strings.Repeat("名", 25)))
			otherUser, err := model.GetUserById(other.UserId, false)
			require.NoError(t, err)
			assert.Equal(t, strings.Repeat("名", 20), otherUser.DisplayName)
			ordinaryBefore, err := model.GetUserById(2, false)
			require.NoError(t, err)
			require.NoError(t, model.SyncIntegrationDisplayName(provider.Id, "bound-before-upgrade", "不可覆盖"))
			ordinaryAfter, err := model.GetUserById(2, false)
			require.NoError(t, err)
			assert.Equal(t, ordinaryBefore, ordinaryAfter)
			require.NoError(t, db.Model(&model.User{}).Where("id = ?", other.UserId).Update("status", common.UserStatusDisabled).Error)
			require.Error(t, model.SyncIntegrationDisplayName(provider.Id, "native-bob", "禁止更新"))
			_, err = model.ResolveIntegrationCredential(provider.Id, "http://wrong.local", "native-alice")
			require.Error(t, err)
			require.NoError(t, db.Model(first).Update("status", 2).Error)
			assert.Equal(t, 409, request("POST", "/credential", payload).Code)
			require.NoError(t, db.Delete(first).Error)
			assert.Equal(t, 409, request("POST", "/credential", payload).Code)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.UserOAuthBinding{}))
			}
			var binding model.UserOAuthBinding
			require.NoError(t, db.Where("provider_id = ? AND provider_user_id = ?", provider.Id, "native-alice").First(&binding).Error)
			assert.Equal(t, first.Id, binding.ModelTokenId)
			var oldBinding model.UserOAuthBinding
			require.NoError(t, db.First(&oldBinding, 100).Error)
			assert.Equal(t, "bound-before-upgrade", oldBinding.ProviderUserId)
			assert.Zero(t, oldBinding.ModelTokenId)

		})
	}
}

// Exercise the actual post-provider login handler and returned browser profile.
func TestIntegrationDisplayNameOAuthLogin(t *testing.T) {
	user, _ := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}, &model.CustomOAuthProvider{}))
	provider := model.CustomOAuthProvider{Name: "Local", Slug: "local", Enabled: true, WellKnown: "http://127.0.0.1:18001/.well-known/openid-configuration", UserIdField: "sub"}
	require.NoError(t, model.DB.Create(&provider).Error)
	binding := model.UserOAuthBinding{UserId: user.Id, ProviderId: provider.Id, ProviderUserId: "stable-subject"}
	require.NoError(t, model.DB.Create(&binding).Error)
	token, err := model.ResolveIntegrationCredential(provider.Id, "http://127.0.0.1:18001", "stable-subject", "原名")
	require.NoError(t, err)
	for _, name := range []string{"新显示名", ""} {
		response := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(response)
		ctx.Request = httptest.NewRequest("GET", "/api/oauth/local", nil)
		handleOAuthLogin(ctx, oauth.NewGenericOAuthProvider(&provider), &oauth.OAuthUser{ProviderUserID: "stable-subject", DisplayName: name}, &model.AuthFlow{Payload: "{}"})
		var result struct {
			Success bool `json:"success"`
			Data    struct {
				User struct {
					DisplayName string `json:"display_name"`
					Id          int    `json:"id"`
					Username    string `json:"username"`
				} `json:"user"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
		require.True(t, result.Success)
		assert.Equal(t, "新显示名", result.Data.User.DisplayName)
		assert.Equal(t, user.Id, result.Data.User.Id)
		assert.Equal(t, user.Username, result.Data.User.Username)
	}
	again, err := model.ResolveIntegrationCredential(provider.Id, "http://127.0.0.1:18001", "stable-subject")
	require.NoError(t, err)
	assert.Equal(t, token, again)
}
