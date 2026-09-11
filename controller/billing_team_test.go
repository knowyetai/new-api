package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type legacyTeam struct {
	Id          int    `gorm:"primaryKey"`
	Name        string `gorm:"type:varchar(128)"`
	OwnerUserId int    `gorm:"index"`
	PayerUserId int    `gorm:"uniqueIndex"`
}

func (legacyTeam) TableName() string { return "billing_units" }

type legacyTeamTopup struct {
	Id      int `gorm:"primaryKey"`
	UserId  int
	Amount  int64
	Money   float64
	TradeNo string `gorm:"type:varchar(255);uniqueIndex"`
	Status  string
}

func (legacyTeamTopup) TableName() string { return "top_ups" }

func TestBillingTeamSelfService(t *testing.T) {
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
			db, fixtureDSN := newAuditTestDatabase(t, kind, os.Getenv(env))
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
			// Initialize native dialect-dependent columns, as normal startup does.
			originalSQLite, originalMaster := common.SQLitePath, common.IsMasterNode
			common.IsMasterNode = false
			if kind == "sqlite" {
				common.SQLitePath = fixtureDSN
				t.Setenv("SQL_DSN", "local")
			} else {
				t.Setenv("SQL_DSN", fixtureDSN)
			}
			t.Setenv("LOG_SQL_DSN", "")
			require.NoError(t, model.InitDB())
			common.SQLitePath, common.IsMasterNode = originalSQLite, originalMaster
			db = model.DB
			t.Cleanup(func() {
				native, err := db.DB()
				if err == nil {
					_ = native.Close()
				}
			})

			require.NoError(t, db.AutoMigrate(&legacyTeam{}, &legacyTeamTopup{}))
			legacy := legacyTeam{Name: "legacy", OwnerUserId: 999, PayerUserId: 998}
			require.NoError(t, db.Create(&legacy).Error)
			order := legacyTeamTopup{UserId: 998, Amount: 3, Money: 3, TradeNo: "legacy", Status: common.TopUpStatusSuccess}
			require.NoError(t, db.Create(&order).Error)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.BillingUnit{}, &model.BillingUnitMember{}, &model.BillingUnitInvitation{}, &model.TopUp{}, &model.UserOAuthBinding{}, &model.CustomOAuthProvider{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}))
				require.NoError(t, logDB.AutoMigrate(&model.Log{}, &model.AuditLog{}))
			}
			var previous model.BillingUnit
			require.NoError(t, db.First(&previous, legacy.Id).Error)
			assert.Nil(t, previous.CreationKey)
			assert.Equal(t, "legacy", previous.Name)
			var previousOrder model.TopUp
			require.NoError(t, db.First(&previousOrder, order.Id).Error)
			assert.Zero(t, previousOrder.BillingUnitId)
			assert.EqualValues(t, 3, previousOrder.Amount)
			users := make([]model.User, 4)
			for i, name := range []string{"owner", "alice", "bob", "outsider"} {
				users[i] = model.User{Username: name, DisplayName: name, AffCode: name, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 1000, Group: "default"}
				require.NoError(t, db.Create(&users[i]).Error)
			}
			owner, alice, bob, outsider := users[0].Id, users[1].Id, users[2].Id, users[3].Id
			t.Setenv("BILLING_TEAM_SELF_SERVICE_ENABLED", "true")
			t.Setenv("BILLING_TEAM_PERSONAL_FALLBACK_ENABLED", "true")
			t.Setenv("BILLING_TEAM_CASDOOR_PROVIDER_ID", "")
			router := gin.New()
			actor := owner
			router.Use(func(c *gin.Context) { c.Set("id", actor); c.Set("role", common.RoleCommonUser) })
			router.POST("/teams", CreateSelfServiceTeam)
			router.GET("/teams", ListBillingUnits)
			router.GET("/self", GetMyBillingUnit)
			router.GET("/teams/:id", GetBillingUnit)
			router.GET("/teams/:id/members", BillingUnitMembers)
			router.POST("/teams/:id/invitations", CreateTeamInvitation)
			router.GET("/teams/:id/invitations", ListTeamInvitations)
			router.GET("/invitations", ListTeamInvitations)
			router.POST("/invitations/:invitation_id/:action", RespondTeamInvitation)
			router.GET("/teams/:id/summary", GetTeamUsageSummary)
			router.PUT("/teams/:id/members/:user_id", BillingUnitMembers)
			request := func(method, path, body string) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
				r.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(w, r)
				return w
			}
			w := request("POST", "/teams", `{"name":"Research","request_key":"create-research","owner_user_id":999,"payer_user_id":998}`)
			require.Equal(t, 200, w.Code, w.Body.String())
			var team model.BillingUnit
			require.NoError(t, db.Where("owner_user_id = ?", owner).First(&team).Error)
			payer, err := model.GetUserById(team.PayerUserId, false)
			require.NoError(t, err)
			assert.Zero(t, payer.Quota)
			assert.Empty(t, payer.Password)
			assert.NotEqual(t, owner, payer.Id)
			duplicate, err := model.CreateSelfServiceBillingUnit(owner, "Research", "create-research")
			require.NoError(t, err)
			assert.Equal(t, team.Id, duplicate.Id)
			second, err := model.CreateSelfServiceBillingUnit(owner, "Second", "create-second")
			require.NoError(t, err)
			membersResponse := request("GET", fmt.Sprintf("/teams/%d/members", team.Id), "")
			require.Equal(t, 200, membersResponse.Code, membersResponse.Body.String())
			assert.Contains(t, membersResponse.Body.String(), `"username":"owner"`)
			current, err := model.ResolveBillingUnit(owner)
			require.NoError(t, err)
			assert.Equal(t, team.Id, current.Id)
			assert.Equal(t, 403, request("PUT", fmt.Sprintf("/teams/%d/members/%d", team.Id, alice), "").Code)
			w = request("POST", fmt.Sprintf("/teams/%d/invitations", team.Id), `{"username":"alice"}`)
			require.Equal(t, 200, w.Code, w.Body.String())
			var invite model.BillingUnitInvitation
			require.NoError(t, db.Where("billing_unit_id = ?", team.Id).First(&invite).Error)
			current, err = model.ResolveBillingUnit(alice)
			require.NoError(t, err)
			assert.Nil(t, current)
			actor = outsider
			assert.Equal(t, 404, request("POST", fmt.Sprintf("/invitations/%d/accept", invite.Id), `{"expected_unit_id":0}`).Code)
			assert.Contains(t, request("GET", "/invitations", "").Body.String(), `"total":0`)
			actor = alice
			assert.Contains(t, request("GET", "/invitations", "").Body.String(), `"total":1`)
			assert.Equal(t, 400, request("POST", fmt.Sprintf("/invitations/%d/accept", invite.Id), `{}`).Code)
			assert.Equal(t, 200, request("POST", fmt.Sprintf("/invitations/%d/accept", invite.Id), `{"expected_unit_id":0}`).Code)
			require.NoError(t, model.RespondBillingInvitation(invite.Id, alice, "accept", 0))
			next, err := model.InviteBillingUnitMember(second.Id, owner, model.TeamInvitee{UserId: alice, Username: "alice"})
			require.NoError(t, err)
			assert.ErrorIs(t, model.RespondBillingInvitation(next.Id, alice, "accept", 0), model.ErrTeamConflict)
			require.NoError(t, model.RespondBillingInvitation(next.Id, alice, "accept", team.Id))
			current, err = model.ResolveBillingUnit(alice)
			require.NoError(t, err)
			assert.Equal(t, second.Id, current.Id)
			require.NoError(t, model.LeaveBillingUnit(alice, second.Id))
			require.NoError(t, model.RespondBillingInvitation(next.Id, alice, "accept", team.Id))
			current, err = model.ResolveBillingUnit(alice)
			require.NoError(t, err)
			assert.Nil(t, current, "accept replay cannot rejoin after leaving")
			for _, action := range []string{"reject", "revoke", "expire"} {
				inv, e := model.InviteBillingUnitMember(team.Id, owner, model.TeamInvitee{UserId: bob, Username: "bob"})
				require.NoError(t, e)
				if action == "expire" {
					require.NoError(t, db.Model(inv).Update("expires_at", time.Now().Unix()-1).Error)
					assert.ErrorIs(t, model.RespondBillingInvitation(inv.Id, bob, "accept", 0), model.ErrTeamInvitationExpired)
				} else {
					who := bob
					if action == "revoke" {
						who = owner
					}
					require.NoError(t, model.RespondBillingInvitation(inv.Id, who, action, 0))
					assert.ErrorIs(t, model.RespondBillingInvitation(inv.Id, bob, "accept", 0), model.ErrTeamConflict)
				}
			}
			// Stable external identity: a same-spelled username and differently cased subject never authorize acceptance.
			ext, err := model.InviteBillingUnitMember(team.Id, owner, model.TeamInvitee{ProviderId: 7, Subject: "Subject-A", Username: "external"})
			require.NoError(t, err)
			require.NoError(t, db.Create(&model.UserOAuthBinding{UserId: outsider, ProviderId: 7, ProviderUserId: "subject-a"}).Error)
			assert.ErrorIs(t, model.RespondBillingInvitation(ext.Id, outsider, "accept", 0), model.ErrTeamUnavailable)
			actor = outsider
			assert.Contains(t, request("GET", "/invitations", "").Body.String(), `"total":0`)
			require.NoError(t, db.Where("user_id = ? AND provider_id = ?", outsider, 7).Delete(&model.UserOAuthBinding{}).Error)
			require.NoError(t, db.Create(&model.UserOAuthBinding{UserId: alice, ProviderId: 7, ProviderUserId: "Subject-A"}).Error)
			require.NoError(t, model.RespondBillingInvitation(ext.Id, alice, "accept", 0))
			// Cross-instance transactions serialize competing choices; SQLite can reject lock contention safely.
			require.NoError(t, model.LeaveBillingUnit(alice, team.Id))
			first, err := model.InviteBillingUnitMember(team.Id, owner, model.TeamInvitee{UserId: alice, Username: "alice"})
			require.NoError(t, err)
			next, err = model.InviteBillingUnitMember(second.Id, owner, model.TeamInvitee{UserId: alice, Username: "alice"})
			require.NoError(t, err)
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for _, id := range []int{first.Id, next.Id} {
				wg.Go(func() { results <- model.RespondBillingInvitation(id, alice, "accept", 0) })
			}
			wg.Wait()
			close(results)
			success := 0
			for e := range results {
				if e == nil {
					success++
				}
			}
			assert.Equal(t, 1, success)
			// Funding selection, opt-in fallback, fixed payer through refund and independent token quota.
			require.NoError(t, db.Model(&model.User{}).Where("id = ?", team.PayerUserId).Update("quota", 50).Error)
			token := model.Token{UserId: alice, Key: "team-alice", Status: 1, RemainQuota: 1000, ExpiredTime: -1}
			require.NoError(t, db.Create(&token).Error)
			newSession := func() (*relaycommon.RelayInfo, *gin.Context) {
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx.Set("token_quota", 1000)
				return &relaycommon.RelayInfo{UserId: alice, BillingUserId: team.PayerUserId, BillingUnitId: team.Id, TokenId: token.Id, TokenKey: token.Key, ForcePreConsume: true, RequestId: common.GetRandomString(12), OriginModelName: "mock"}, ctx
			}
			info, ctx := newSession()
			_, apiErr := service.NewBillingSession(ctx, info, 100)
			require.NotNil(t, apiErr)
			assert.Equal(t, "team_quota_personal_fallback_disabled", string(apiErr.GetErrorCode()))
			require.NoError(t, model.UpdatePersonalBillingFallback(alice, true))
			require.NoError(t, db.Model(&model.User{}).Where("id = ?", alice).Update("quota", 0).Error)
			info, ctx = newSession()
			_, apiErr = service.NewBillingSession(ctx, info, 100)
			require.NotNil(t, apiErr)
			assert.Equal(t, "team_and_personal_quota_insufficient", string(apiErr.GetErrorCode()))
			info, ctx = newSession()
			info.BillingUserId, info.BillingUnitId = alice, 0
			_, apiErr = service.NewBillingSession(ctx, info, 100)
			require.NotNil(t, apiErr)
			assert.Equal(t, "personal_quota_insufficient", string(apiErr.GetErrorCode()))
			require.NoError(t, db.Model(&model.User{}).Where("id = ?", alice).Update("quota", 1000).Error)
			info, ctx = newSession()
			session, apiErr := service.NewBillingSession(ctx, info, 100)
			require.Nil(t, apiErr)
			assert.Equal(t, alice, info.BillingUserId)
			assert.Zero(t, info.BillingUnitId)
			require.NoError(t, session.Settle(70))
			user, err := model.GetUserById(alice, false)
			require.NoError(t, err)
			assert.Equal(t, 930, user.Quota)
			payer, err = model.GetUserById(team.PayerUserId, false)
			require.NoError(t, err)
			assert.Equal(t, 50, payer.Quota)
			require.NoError(t, db.Model(&model.User{}).Where("id = ?", team.PayerUserId).Update("status", common.UserStatusDisabled).Error)
			info, ctx = newSession()
			_, apiErr = service.NewBillingSession(ctx, info, 100)
			require.NotNil(t, apiErr)
			assert.Equal(t, team.PayerUserId, info.BillingUserId)
			require.NoError(t, db.Model(&model.User{}).Where("id = ?", team.PayerUserId).Updates(map[string]any{"status": common.UserStatusEnabled, "quota": 200}).Error)
			info, ctx = newSession()
			session, apiErr = service.NewBillingSession(ctx, info, 100)
			require.Nil(t, apiErr)
			assert.Equal(t, team.PayerUserId, info.BillingUserId)
			session.Refund(ctx)
			require.Eventually(t, func() bool { u, e := model.GetUserById(team.PayerUserId, false); return e == nil && u.Quota == 200 }, 3*time.Second, 10*time.Millisecond)

			// Token exhaustion and database faults cannot spend the personal wallet.
			require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Update("remain_quota", 1).Error)
			info, ctx = newSession()
			_, apiErr = service.NewBillingSession(ctx, info, 100)
			require.NotNil(t, apiErr)
			assert.Equal(t, team.PayerUserId, info.BillingUserId)
			require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Update("remain_quota", 930).Error)
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("team_test_db_failure", func(tx *gorm.DB) {
				if tx.Statement.Table == "users" {
					tx.AddError(errors.New("synthetic database failure"))
				}
			}))
			info, ctx = newSession()
			_, apiErr = service.NewBillingSession(ctx, info, 100)
			require.NoError(t, db.Callback().Update().Remove("team_test_db_failure"))
			require.NotNil(t, apiErr)
			assert.Equal(t, team.PayerUserId, info.BillingUserId)
			user, err = model.GetUserById(alice, false)
			require.NoError(t, err)
			assert.Equal(t, 930, user.Quota)
			t.Setenv("BILLING_TEAM_PERSONAL_FALLBACK_ENABLED", "false")
			info, ctx = newSession()
			_, apiErr = service.NewBillingSession(ctx, info, 300)
			require.NotNil(t, apiErr)
			assert.Equal(t, team.PayerUserId, info.BillingUserId)
			assert.Equal(t, "team_quota_insufficient", string(apiErr.GetErrorCode()))
			// Two requests competing for the last team balance reserve at most once.
			sessions := make(chan *service.BillingSession, 2)
			for range 2 {
				wg.Go(func() {
					i, c := newSession()
					session, e := service.NewBillingSession(c, i, 150)
					if e == nil {
						sessions <- session
					}
				})
			}
			wg.Wait()
			close(sessions)
			success = 0
			for session := range sessions {
				success++
				session.Refund(ctx)
			}
			assert.Equal(t, 1, success)
			require.Eventually(t, func() bool { u, e := model.GetUserById(team.PayerUserId, false); return e == nil && u.Quota == 200 }, 3*time.Second, 10*time.Millisecond)
			t.Setenv("BILLING_TEAM_PERSONAL_FALLBACK_ENABLED", "true")
			// Team payment is bound to its original wallet, and repeated callbacks credit exactly once.
			topup := model.TopUp{UserId: team.PayerUserId, BillingUnitId: team.Id, OperatorUserId: owner, Amount: 1, Money: 1, TradeNo: "team-order", PaymentMethod: "alipay", PaymentProvider: model.PaymentProviderEpay, Status: common.TopUpStatusPending}
			require.NoError(t, db.Create(&topup).Error)
			require.NoError(t, model.SelectOwnedBillingUnit(owner, second.Id, team.Id))
			done, err := model.RechargeEpay(topup.TradeNo, "alipay", "")
			require.NoError(t, err)
			assert.False(t, done)
			done, err = model.RechargeEpay(topup.TradeNo, "alipay", "")
			require.NoError(t, err)
			assert.True(t, done)
			payer, err = model.GetUserById(team.PayerUserId, false)
			require.NoError(t, err)
			assert.Equal(t, 200+int(common.QuotaPerUnit), payer.Quota)
			require.NoError(t, logDB.Create(&model.Log{UserId: alice, BillingUserId: team.PayerUserId, BillingUnitId: team.Id, Type: model.LogTypeConsume, Quota: 70, PromptTokens: 20, CompletionTokens: 10, CreatedAt: time.Now().Unix()}).Error)
			actor = owner
			w = request("GET", fmt.Sprintf("/teams/%d/summary", team.Id), "")
			require.Equal(t, 200, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), `"quota":70`)
			actor = alice
			assert.Equal(t, 404, request("GET", fmt.Sprintf("/teams/%d/summary", team.Id), "").Code)
			verifyTeamDirectory(t, owner, outsider, team.Id)
			verifyTeamPayment(t, owner, outsider, team.Id, team.PayerUserId)
			t.Setenv("BILLING_TEAM_SELF_SERVICE_ENABLED", "false")
			assert.Equal(t, 403, request("POST", "/teams", `{"name":"closed","request_key":"create-closed"}`).Code)
		})
	}
}

// The provider is the external boundary; lookup and identity binding use real SQL.
func verifyTeamDirectory(t *testing.T, owner, user, unit int) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		if !ok || id != "directory" || secret != "local-directory-test" {
			w.WriteHeader(401)
			return
		}
		assert.Equal(t, "local-it/external", r.URL.Query().Get("id"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"local-it","name":"external","id":"external-stable-subject","displayName":"External User"}}`))
	}))
	defer server.Close()
	provider := model.CustomOAuthProvider{Name: "team-directory", Slug: "team-directory", Enabled: true, UserIdField: "sub", WellKnown: server.URL + "/.well-known/openid-configuration"}
	require.NoError(t, model.DB.Create(&provider).Error)
	t.Setenv("BILLING_TEAM_CASDOOR_PROVIDER_ID", strconv.Itoa(provider.Id))
	t.Setenv("BILLING_TEAM_CASDOOR_ORGANIZATION", "local-it")
	t.Setenv("BILLING_TEAM_DIRECTORY_CLIENT_ID", "directory")
	t.Setenv("BILLING_TEAM_DIRECTORY_CLIENT_SECRET", "local-directory-test")
	recipient, err := service.ResolveTeamInvitee(context.Background(), "external")
	require.NoError(t, err)
	assert.Zero(t, recipient.UserId)
	invitation, err := model.InviteBillingUnitMember(unit, owner, recipient)
	require.NoError(t, err)
	assert.ErrorIs(t, model.RespondBillingInvitation(invitation.Id, user, "accept", 0), model.ErrTeamUnavailable)
	require.NoError(t, model.DB.Create(&model.UserOAuthBinding{UserId: user, ProviderId: provider.Id, ProviderUserId: "external-stable-subject"}).Error)
	require.NoError(t, model.RespondBillingInvitation(invitation.Id, user, "accept", 0))
	t.Setenv("BILLING_TEAM_DIRECTORY_CLIENT_SECRET", "wrong")
	_, err = service.ResolveTeamInvitee(context.Background(), "external")
	require.Error(t, err)
	_, err = service.ResolveTeamInvitee(context.Background(), "../external")
	assert.ErrorIs(t, err, model.ErrTeamInvalid)
}

func verifyTeamPayment(t *testing.T, owner, outsider, unit, payer int) {
	t.Helper()
	confirmPaymentComplianceForTest(t)
	oldAddress, oldID, oldKey, oldMethods := operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey, operation_setting.PayMethods
	t.Cleanup(func() {
		operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey, operation_setting.PayMethods = oldAddress, oldID, oldKey, oldMethods
	})
	operation_setting.PayAddress = "https://payment.example.test"
	operation_setting.EpayId = "local"
	operation_setting.EpayKey = "local-payment-test"
	operation_setting.PayMethods = []map[string]string{{"type": "alipay"}}
	t.Setenv("BILLING_TEAM_TOPUP_ENABLED", "true")
	r := gin.New()
	actor := owner
	r.Use(func(c *gin.Context) { c.Set("id", actor); c.Set("role", common.RoleCommonUser) })
	r.POST("/teams/:id/topups", RequestTeamEpay)
	r.POST("/notify", EpayNotify)
	request := func(method, path, body, contentType string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", contentType)
		r.ServeHTTP(w, req)
		return w
	}
	path := fmt.Sprintf("/teams/%d/topups", unit)
	actor = outsider
	require.Equal(t, 404, request("POST", path, `{"amount":10,"payment_method":"alipay"}`, "application/json").Code)
	actor = owner
	body := fmt.Sprintf(`{"amount":%d,"payment_method":"alipay","payer_user_id":%d}`, max(getMinTopup(), 10), outsider)
	response := request("POST", path, body, "application/json")
	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), `"message":"success"`)
	var order model.TopUp
	require.NoError(t, model.DB.Where("billing_unit_id = ?", unit).Order("id desc").First(&order).Error)
	assert.Equal(t, payer, order.UserId)
	assert.Equal(t, owner, order.OperatorUserId)
	before, err := model.GetUserById(payer, false)
	require.NoError(t, err)
	notify := func(money, status string, tamper bool) string {
		params := epay.GenerateParams(map[string]string{"pid": "local", "out_trade_no": order.TradeNo, "trade_no": "external-trade", "type": "alipay", "money": money, "trade_status": status}, operation_setting.EpayKey)
		if tamper {
			params["money"] = "0.01"
		}
		values := url.Values{}
		for k, v := range params {
			values.Set(k, v)
		}
		return request("POST", "/notify", values.Encode(), "application/x-www-form-urlencoded").Body.String()
	}
	money := fmt.Sprintf("%.2f", order.Money)
	assert.Equal(t, "fail", notify(money, epay.StatusTradeSuccess, true))
	assert.Equal(t, "fail", notify("0.01", epay.StatusTradeSuccess, false))
	notify(money, "TRADE_CLOSED", false)
	after, err := model.GetUserById(payer, false)
	require.NoError(t, err)
	assert.Equal(t, before.Quota, after.Quota)
	assert.Equal(t, "success", notify(money, epay.StatusTradeSuccess, false))
	assert.Equal(t, "success", notify(money, epay.StatusTradeSuccess, false))
	after, err = model.GetUserById(payer, false)
	require.NoError(t, err)
	assert.Equal(t, before.Quota+int(order.Amount)*int(common.QuotaPerUnit), after.Quota)
}
