package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/lianlianpay"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const lianlianTestUserID = 77

const lianlianTestIdempotencyKey = "lianlian-test-create-order"

const lianlianTestCreatePayResponse = `{"ret_code":"0000","ret_msg":"交易成功","payload":"https://qr.alipay.com/bax1","mch_id":"2020042200284052","platform_txno":"LL20261016001","txn_seqno":"{{trade_no}}","order_amount":"{{order_amount}}"}`

type lianlianTestEnv struct {
	db              *gorm.DB
	gateway         *httptest.Server
	merchantPubKey  string
	platformPrivKey string
	queryResponse   string
	queryResponses  map[string]string
	signResponses   bool
	lastPath        string
	lastBody        []byte
	lastSignature   string
	requests        int
}

func newTestRSAKeyPEM(t *testing.T) (privateKeyPEM string, publicKeyPEM string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}))
}

func newLianlianTestEnv(t *testing.T, gatewayResponse string) *lianlianTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	confirmPaymentComplianceForTest(t)
	initModelListColumnNames(t)

	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	oldRedis, oldBatch := common.RedisEnabled, common.BatchUpdateEnabled
	oldQuotaPerUnit := common.QuotaPerUnit
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.SetDatabaseTypes(oldMainType, oldLogType)
		common.RedisEnabled, common.BatchUpdateEnabled = oldRedis, oldBatch
		common.QuotaPerUnit = oldQuotaPerUnit
	})

	dir := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(dir, "main.db")), &gorm.Config{})
	require.NoError(t, err)
	logDB, err := gorm.Open(sqlite.Open(filepath.Join(dir, "log.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TopUp{}, &model.Option{}))
	require.NoError(t, logDB.AutoMigrate(&model.Log{}))
	model.DB, model.LOG_DB = db, logDB
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	common.QuotaPerUnit = 500000
	t.Cleanup(func() {
		for _, handle := range []*gorm.DB{db, logDB} {
			native, dbErr := handle.DB()
			if dbErr == nil {
				_ = native.Close()
			}
		}
	})

	require.NoError(t, db.Create(&model.User{
		Id:        lianlianTestUserID,
		Username:  "lianlian_topup_user",
		Email:     "lianlian@example.test",
		AffCode:   "lianlian-topup-aff",
		Quota:     1000,
		Status:    common.UserStatusEnabled,
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Unix(),
	}).Error)

	env := &lianlianTestEnv{
		db:             db,
		queryResponses: make(map[string]string),
		signResponses:  true,
	}
	merchantPriv, merchantPub := newTestRSAKeyPEM(t)
	platformPriv, platformPub := newTestRSAKeyPEM(t)
	env.merchantPubKey = merchantPub
	env.platformPrivKey = platformPriv

	env.gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.requests++
		env.lastPath = r.URL.Path
		env.lastSignature = r.Header.Get(lianlianpay.HeaderSignatureData)
		body, readErr := io.ReadAll(r.Body)
		require.NoError(t, readErr)
		env.lastBody = body
		var requestPayload struct {
			OrderAmount string `json:"order_amount"`
			TxnSeqno    string `json:"txn_seqno"`
		}
		require.NoError(t, common.Unmarshal(body, &requestPayload))
		responseBody := gatewayResponse
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, lianlianpay.OrderQueryPath) {
			responseBody = env.queryResponse
			if configured, ok := env.queryResponses[requestPayload.TxnSeqno]; ok {
				responseBody = configured
			}
		} else {
			responseBody = strings.ReplaceAll(responseBody, "{{trade_no}}", requestPayload.TxnSeqno)
			responseBody = strings.ReplaceAll(responseBody, "{{order_amount}}", requestPayload.OrderAmount)
		}
		if env.signResponses {
			signature, signErr := lianlianpay.Sign(env.platformPrivKey, []byte(responseBody))
			require.NoError(t, signErr)
			w.Header().Set(lianlianpay.HeaderSignatureData, signature)
		}
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(env.gateway.Close)

	oldLianlian := setting.LianlianEnabled
	oldEnv := setting.LianlianEnv
	oldGateway := setting.LianlianGateway
	oldMchID := setting.LianlianMchId
	oldPrivateKey := setting.LianlianPrivateKey
	oldPublicCert := setting.LianlianPlatformPublicCert
	oldBusiType := setting.LianlianBusiType
	oldRiskCategory := setting.LianlianRiskCategory
	oldMinTopUp := operation_setting.MinTopUp
	oldCallback := operation_setting.CustomCallbackAddress
	oldPrice := operation_setting.Price
	oldExchangeRate := operation_setting.USDExchangeRate
	oldDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	oldRatios := common.TopupGroupRatio2JSONString()
	t.Cleanup(func() {
		setting.LianlianEnabled = oldLianlian
		setting.LianlianEnv = oldEnv
		setting.LianlianGateway = oldGateway
		setting.LianlianMchId = oldMchID
		setting.LianlianPrivateKey = oldPrivateKey
		setting.LianlianPlatformPublicCert = oldPublicCert
		setting.LianlianBusiType = oldBusiType
		setting.LianlianRiskCategory = oldRiskCategory
		operation_setting.MinTopUp = oldMinTopUp
		operation_setting.CustomCallbackAddress = oldCallback
		operation_setting.Price = oldPrice
		operation_setting.USDExchangeRate = oldExchangeRate
		operation_setting.GetGeneralSetting().QuotaDisplayType = oldDisplayType
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(oldRatios))
	})

	setting.LianlianEnabled = true
	setting.LianlianEnv = lianlianpay.EnvTest
	setting.LianlianGateway = env.gateway.URL + "/mch"
	setting.LianlianMchId = "2020042200284052"
	setting.LianlianPrivateKey = merchantPriv
	setting.LianlianPlatformPublicCert = platformPub
	setting.LianlianBusiType = lianlianpay.DefaultBusiType
	setting.LianlianRiskCategory = lianlianpay.DefaultRiskCategory
	operation_setting.MinTopUp = 1
	operation_setting.CustomCallbackAddress = "https://api.example.test"
	operation_setting.Price = 7.3
	operation_setting.USDExchangeRate = 7.3
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeCNY
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1}`))
	return env
}

func lianlianTestRouter(userId int) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", userId)
	})
	router.POST("/lianlian/pay", RequestLianlianPay)
	router.POST("/lianlian/amount", RequestLianlianAmount)
	router.POST("/lianlian/notify", LianlianNotify)
	router.GET("/lianlian/order/:tradeNo", GetLianlianOrder)
	router.POST("/lianlian/order/:tradeNo/cancel", CancelLianlianOrder)
	router.GET("/topup/info", GetTopUpInfo)
	router.GET("/option", GetOptions)
	router.PUT("/option", UpdateOption)
	router.POST("/option/lianlian/save", SaveLianlianSettings)
	return router
}

func lianlianTestPost(t *testing.T, router *gin.Engine, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	return lianlianTestPostWithHeaders(t, router, path, body, nil)
}

func lianlianTestPostWithHeaders(t *testing.T, router *gin.Engine, path string, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

func postLianlianPay(t *testing.T, router *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	return lianlianTestPostWithHeaders(t, router, "/lianlian/pay", body, map[string]string{
		lianlianIdempotencyKeyHeader: lianlianTestIdempotencyKey,
	})
}

func postLianlianNotify(t *testing.T, router *gin.Engine, body string, signature string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/lianlian/notify", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(lianlianpay.HeaderSignatureData, signature)
	router.ServeHTTP(recorder, request)
	return recorder
}

func lianlianNotifyBody(t *testing.T, env *lianlianTestEnv, tradeNo string, amount string) (string, string) {
	t.Helper()
	return lianlianNotifyBodyFor(t, env, tradeNo, amount, setting.LianlianMchId, lianlianpay.PayTypeAlipayNative)
}

func lianlianNotifyBodyFor(t *testing.T, env *lianlianTestEnv, tradeNo string, amount string, mchID string, payType string) (string, string) {
	t.Helper()
	body := fmt.Sprintf(
		`{"mch_id":%q,"txn_seqno":%q,"order_amount":%q,"result_pay":"SUCCESS","platform_txno":"LL2026101600PLAT","pay_method_infos":[{"pay_type":%q,"amount":%q}]}`,
		mchID, tradeNo, amount, payType, amount,
	)
	signature, err := lianlianpay.Sign(env.platformPrivKey, []byte(body))
	require.NoError(t, err)
	return body, signature
}

func TestRequestLianlianPayCreatesOrderAndQRCode(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	operation_setting.Price = 99

	recorder := postLianlianPay(t, router, `{"amount":1000,"payment_method":" alipay "}`)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"message":"success"`)
	require.Contains(t, recorder.Body.String(), `"qr_code":"https://qr.alipay.com/bax1"`)
	assert.NotContains(t, recorder.Body.String(), "money_cents")

	var response struct {
		Data struct {
			TradeNo              string  `json:"trade_no"`
			ProviderTradeNo      string  `json:"provider_trade_no"`
			QRCode               string  `json:"qr_code"`
			Money                float64 `json:"money"`
			Amount               int64   `json:"amount"`
			AmountUnit           string  `json:"amount_unit"`
			RequestID            string  `json:"request_id"`
			RequestedAmountCents int64   `json:"requested_amount_cents"`
			RequestedAmountUnit  string  `json:"requested_amount_unit"`
			PayableAmountCents   int64   `json:"payable_amount_cents"`
			PayableAmountUnit    string  `json:"payable_amount_unit"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.NotEmpty(t, response.Data.TradeNo)
	assert.InDelta(t, 10, response.Data.Money, 1e-9)
	assert.Equal(t, int64(1000), response.Data.Amount)
	assert.Equal(t, "CNY_CENT", response.Data.AmountUnit)
	assert.Equal(t, int64(1000), response.Data.RequestedAmountCents)
	assert.Equal(t, "CNY_CENT", response.Data.RequestedAmountUnit)
	assert.Equal(t, int64(1000), response.Data.PayableAmountCents)
	assert.Equal(t, "CNY_CENT", response.Data.PayableAmountUnit)
	assert.Equal(t, "LL20261016001", response.Data.ProviderTradeNo)
	assert.Equal(t, lianlianTestIdempotencyKey, response.Data.RequestID)

	order := model.GetTopUpByTradeNo(response.Data.TradeNo)
	require.NotNil(t, order)
	assert.Equal(t, model.PaymentProviderLianlian, order.PaymentProvider)
	assert.Equal(t, "alipay", order.PaymentMethod)
	assert.Equal(t, int64(1000), order.Amount)
	assert.Equal(t, model.AmountUnitCNYCent, order.AmountUnit)
	assert.InDelta(t, 1000, order.Money, 1e-9)
	assert.Equal(t, "LL20261016001", order.ProviderTradeNo)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
	require.NotNil(t, order.LianlianRequestID)
	assert.Equal(t, lianlianTestIdempotencyKey, *order.LianlianRequestID)

	assert.Equal(t, "/mch"+lianlianpay.CreatePayPath, env.lastPath)
	assert.NotEmpty(t, env.lastSignature)
	verified, err := lianlianpay.Verify(env.merchantPubKey, env.lastBody, env.lastSignature)
	require.NoError(t, err)
	assert.True(t, verified)
	assert.Contains(t, string(env.lastBody), `"order_amount":"10.00"`)
	assert.Contains(t, string(env.lastBody), `"pay_expire":15`)
	assert.Contains(t, string(env.lastBody), `"pay_type":"ALIPAY_NATIVE"`)
	assert.Contains(t, string(env.lastBody), `"notify_url":"https://api.example.test/api/user/lianlian/notify"`)
	var createRequest struct {
		RiskItem   string            `json:"risk_item"`
		DeviceInfo map[string]string `json:"device_info"`
	}
	require.NoError(t, common.Unmarshal(env.lastBody, &createRequest))
	assert.Equal(t, "new-api-user-77", createRequest.DeviceInfo["device_id"])
	assert.Contains(t, createRequest.RiskItem, `"user_info_mercht_userlogin":"lianlian_topup_user"`)
	assert.Contains(t, createRequest.RiskItem, `"user_info_mail":"lianlian@example.test"`)
	assert.Contains(t, createRequest.RiskItem, `"user_info_dt_registe":"20260102110405"`)

	query := httptest.NewRecorder()
	router.ServeHTTP(query, httptest.NewRequest(http.MethodGet, "/lianlian/order/"+response.Data.TradeNo, nil))
	assert.Contains(t, query.Body.String(), `"status":"pending"`)
	assert.Contains(t, query.Body.String(), `"provider_trade_no":"LL20261016001"`)
	assert.Contains(t, query.Body.String(), `"amount":1000`)
	assert.Contains(t, query.Body.String(), `"amount_unit":"CNY_CENT"`)
	assert.Contains(t, query.Body.String(), `"money":10`)
	assert.NotContains(t, query.Body.String(), "money_cents")
}

func TestRequestLianlianPayReplaysIdempotentCreate(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	body := `{"amount":1000,"payment_method":"alipay"}`

	first := postLianlianPay(t, router, body)
	second := postLianlianPay(t, router, body)
	require.Contains(t, first.Body.String(), `"message":"success"`)
	require.Contains(t, second.Body.String(), `"message":"success"`)

	var firstResponse, secondResponse struct {
		Data struct {
			TradeNo string `json:"trade_no"`
			QRCode  string `json:"qr_code"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(first.Body.Bytes(), &firstResponse))
	require.NoError(t, common.Unmarshal(second.Body.Bytes(), &secondResponse))
	assert.NotEmpty(t, firstResponse.Data.TradeNo)
	assert.Equal(t, firstResponse.Data.TradeNo, secondResponse.Data.TradeNo)
	assert.Equal(t, firstResponse.Data.QRCode, secondResponse.Data.QRCode)
	assert.Equal(t, 1, env.requests)

	var orderCount int64
	require.NoError(t, env.db.Model(&model.TopUp{}).Count(&orderCount).Error)
	assert.Equal(t, int64(1), orderCount)
}

func TestRequestLianlianPayRejectsConflictingIdempotencyKey(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	require.Contains(t,
		postLianlianPay(t, router, `{"amount":1000,"payment_method":"alipay"}`).Body.String(),
		`"message":"success"`,
	)

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "amount", body: `{"amount":2000,"payment_method":"alipay"}`},
		{name: "payment method", body: `{"amount":1000,"payment_method":"wxpay"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := postLianlianPay(t, router, tc.body)
			assert.Contains(t, recorder.Body.String(), `"code":"LIANLIAN_IDEMPOTENCY_CONFLICT"`)
		})
	}
	assert.Equal(t, 1, env.requests)
}

func TestRequestLianlianPayValidatesIdempotencyKey(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	body := `{"amount":1000,"payment_method":"alipay"}`

	missing := lianlianTestPost(t, router, "/lianlian/pay", body)
	assert.Contains(t, missing.Body.String(), `"code":"LIANLIAN_REQUEST_ID_INVALID"`)

	tooLong := lianlianTestPostWithHeaders(t, router, "/lianlian/pay", body, map[string]string{
		lianlianIdempotencyKeyHeader: strings.Repeat("a", 65),
	})
	assert.Contains(t, tooLong.Body.String(), `"code":"LIANLIAN_REQUEST_ID_INVALID"`)

	mismatch := lianlianTestPostWithHeaders(t, router, "/lianlian/pay",
		`{"amount":1000,"payment_method":"alipay","request_id":"body-key"}`,
		map[string]string{lianlianIdempotencyKeyHeader: "header-key"},
	)
	assert.Contains(t, mismatch.Body.String(), `"code":"LIANLIAN_IDEMPOTENCY_KEY_MISMATCH"`)
	assert.Zero(t, env.requests)

	fromBody := lianlianTestPost(t, router, "/lianlian/pay",
		`{"amount":1000,"payment_method":"alipay","request_id":"body-key"}`,
	)
	assert.Contains(t, fromBody.Body.String(), `"message":"success"`)
	assert.Contains(t, fromBody.Body.String(), `"request_id":"body-key"`)
	assert.Equal(t, 1, env.requests)
}

func TestRequestLianlianPayRejectsUnsignedGatewayResponse(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	env.signResponses = false
	router := lianlianTestRouter(lianlianTestUserID)

	recorder := postLianlianPay(t, router, `{"amount":1000,"payment_method":"alipay"}`)
	assert.Contains(t, recorder.Body.String(), `"message":"error"`)
	assert.NotContains(t, recorder.Body.String(), `"qr_code"`)

	var orders []model.TopUp
	require.NoError(t, env.db.Where("payment_provider = ?", model.PaymentProviderLianlian).Find(&orders).Error)
	require.Len(t, orders, 1)
	assert.Equal(t, common.TopUpStatusFailed, orders[0].Status)
}

func TestRequestLianlianPayRejectsMismatchedGatewayOrder(t *testing.T) {
	testCases := []struct {
		name     string
		response string
	}{
		{
			name:     "merchant",
			response: `{"ret_code":"0000","ret_msg":"交易成功","payload":"https://qr.alipay.com/bax1","mch_id":"different-merchant","platform_txno":"LL20261016001","txn_seqno":"{{trade_no}}","order_amount":"{{order_amount}}"}`,
		},
		{
			name:     "order number",
			response: `{"ret_code":"0000","ret_msg":"交易成功","payload":"https://qr.alipay.com/bax1","mch_id":"2020042200284052","platform_txno":"LL20261016001","txn_seqno":"different-order","order_amount":"{{order_amount}}"}`,
		},
		{
			name:     "amount",
			response: `{"ret_code":"0000","ret_msg":"交易成功","payload":"https://qr.alipay.com/bax1","mch_id":"2020042200284052","platform_txno":"LL20261016001","txn_seqno":"{{trade_no}}","order_amount":"9.99"}`,
		},
		{
			name:     "provider trade number",
			response: fmt.Sprintf(`{"ret_code":"0000","ret_msg":"交易成功","payload":"https://qr.alipay.com/bax1","mch_id":"2020042200284052","platform_txno":%q,"txn_seqno":"{{trade_no}}","order_amount":"{{order_amount}}"}`, strings.Repeat("x", 129)),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			env := newLianlianTestEnv(t, tc.response)
			router := lianlianTestRouter(lianlianTestUserID)

			recorder := postLianlianPay(t, router, `{"amount":1000,"payment_method":"alipay"}`)
			assert.Contains(t, recorder.Body.String(), `"message":"error"`)
			assert.NotContains(t, recorder.Body.String(), `"qr_code"`)

			var orders []model.TopUp
			require.NoError(t, env.db.Where("payment_provider = ?", model.PaymentProviderLianlian).Find(&orders).Error)
			require.Len(t, orders, 1)
			assert.Equal(t, common.TopUpStatusFailed, orders[0].Status)
		})
	}
}

func TestLianlianUsesCNYAmountWhenQuotaDisplayIsTokens(t *testing.T) {
	newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeTokens

	recorder := lianlianTestPost(t, router, "/lianlian/amount", `{"amount":1000,"payment_method":"wxpay"}`)
	assert.JSONEq(t, `{"message":"success","data":{"amount_cents":1000,"amount_unit":"CNY_CENT"}}`, recorder.Body.String())
}

func TestTopUpResponsesExposeEffectiveAmountUnit(t *testing.T) {
	encoded, err := common.Marshal(gin.H{"items": topUpResponses([]*model.TopUp{
		{Amount: 1050, Money: 1050, PaymentProvider: model.PaymentProviderLianlian},
		{Amount: 1, Money: 1, AmountUnit: model.AmountUnitCNYCent, PaymentProvider: model.PaymentProviderLianlian},
		{Amount: 10, PaymentProvider: model.PaymentProviderEpay},
	})})
	require.NoError(t, err)

	var response struct {
		Items []struct {
			Amount          int64            `json:"amount"`
			AmountUnit      model.AmountUnit `json:"amount_unit"`
			Money           float64          `json:"money"`
			PaymentProvider string           `json:"payment_provider"`
		} `json:"items"`
	}
	require.NoError(t, common.Unmarshal(encoded, &response))
	require.Len(t, response.Items, 3)
	assert.Equal(t, int64(1050), response.Items[0].Amount)
	assert.Empty(t, response.Items[0].AmountUnit)
	assert.InDelta(t, 10.5, response.Items[0].Money, 1e-9)
	assert.Equal(t, model.PaymentProviderLianlian, response.Items[0].PaymentProvider)
	assert.Equal(t, int64(1), response.Items[1].Amount)
	assert.InDelta(t, 0.01, response.Items[1].Money, 1e-9)
	assert.Equal(t, model.AmountUnitCNYCent, response.Items[1].AmountUnit)
	assert.Equal(t, int64(10), response.Items[2].Amount)
	assert.Equal(t, model.AmountUnitUSD, response.Items[2].AmountUnit)
	assert.Equal(t, model.PaymentProviderEpay, response.Items[2].PaymentProvider)
}

func TestRequestLianlianPayMarksOrderFailedWhenGatewayRejects(t *testing.T) {
	env := newLianlianTestEnv(t, `{"ret_code":"1001","ret_msg":"商户号不存在"}`)
	router := lianlianTestRouter(lianlianTestUserID)

	recorder := postLianlianPay(t, router, `{"amount":1000,"payment_method":"alipay"}`)
	assert.Contains(t, recorder.Body.String(), `"message":"error"`)
	assert.NotContains(t, recorder.Body.String(), `"qr_code"`)

	var orders []model.TopUp
	require.NoError(t, env.db.Where("payment_provider = ?", model.PaymentProviderLianlian).Find(&orders).Error)
	require.Len(t, orders, 1)
	assert.Equal(t, common.TopUpStatusFailed, orders[0].Status)
}

func TestRequestLianlianPayMarksOrderFailedWhenCreateOutcomeIsUncertain(t *testing.T) {
	env := newLianlianTestEnv(t, `{"ret_code":"0000","ret_msg":"受理成功","payload":""}`)
	router := lianlianTestRouter(lianlianTestUserID)

	recorder := postLianlianPay(t, router, `{"amount":1000,"payment_method":"alipay"}`)
	assert.Contains(t, recorder.Body.String(), `"message":"error"`)
	assert.NotContains(t, recorder.Body.String(), `"qr_code"`)

	var orders []model.TopUp
	require.NoError(t, env.db.Where("payment_provider = ?", model.PaymentProviderLianlian).Find(&orders).Error)
	require.Len(t, orders, 1)
	assert.Equal(t, common.TopUpStatusFailed, orders[0].Status)
}

func TestRequestLianlianPayMarksOrderFailedWhenGatewayIsUnavailable(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	env.gateway.Close()
	router := lianlianTestRouter(lianlianTestUserID)

	recorder := postLianlianPay(t, router, `{"amount":1000,"payment_method":"alipay"}`)
	assert.Contains(t, recorder.Body.String(), `"message":"error"`)
	assert.NotContains(t, recorder.Body.String(), `"qr_code"`)

	var orders []model.TopUp
	require.NoError(t, env.db.Where("payment_provider = ?", model.PaymentProviderLianlian).Find(&orders).Error)
	require.Len(t, orders, 1)
	assert.Equal(t, common.TopUpStatusFailed, orders[0].Status)
}

func TestRequestLianlianAmountTreatsAmountAsCNYCents(t *testing.T) {
	newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)

	info := httptest.NewRecorder()
	router.ServeHTTP(info, httptest.NewRequest(http.MethodGet, "/topup/info", nil))
	assert.Contains(t, info.Body.String(), `"lianlian_min_topup":1`)
	assert.Contains(t, info.Body.String(), `"lianlian_amount_unit":"CNY_CENT"`)

	recorder := lianlianTestPost(t, router, "/lianlian/amount", `{"amount":1000,"payment_method":"wxpay"}`)
	assert.JSONEq(t, `{"message":"success","data":{"amount_cents":1000,"amount_unit":"CNY_CENT"}}`, recorder.Body.String())

	recorder = lianlianTestPost(t, router, "/lianlian/amount", `{"amount_cents":1,"payment_method":"wxpay"}`)
	assert.JSONEq(t, `{"message":"success","data":{"amount_cents":1,"amount_unit":"CNY_CENT"}}`, recorder.Body.String())

	recorder = lianlianTestPost(t, router, "/lianlian/amount", `{"amount":1,"amount_cents":2,"payment_method":"wxpay"}`)
	assert.Contains(t, recorder.Body.String(), "amount 与 amount_cents 不一致")

	setting.LianlianEnabled = false
	recorder = lianlianTestPost(t, router, "/lianlian/amount", `{"amount":1000,"payment_method":"wxpay"}`)
	assert.Contains(t, recorder.Body.String(), "充值服务未启用")
	setting.LianlianEnabled = true

	recorder = lianlianTestPost(t, router, "/lianlian/amount", `{"amount":0,"payment_method":"wxpay"}`)
	assert.Contains(t, recorder.Body.String(), "充值金额不能小于 0.01 元")
	recorder = lianlianTestPost(t, router, "/lianlian/amount", `{"amount":1000,"payment_method":"card"}`)
	assert.Contains(t, recorder.Body.String(), "支付方式不存在")
	recorder = lianlianTestPost(t, router, "/lianlian/amount", `{"amount":100000000,"payment_method":"wxpay"}`)
	assert.Contains(t, recorder.Body.String(), "支付金额超出范围")
}

func TestSaveLianlianSettingsValidatesAndPersistsAtomically(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	privateKey, publicKey := newTestRSAKeyPEM(t)

	oldOptions := common.OptionMap
	common.OptionMap = map[string]string{}
	t.Cleanup(func() { common.OptionMap = oldOptions })

	body, err := common.Marshal(saveLianlianSettingsRequest{
		Enabled:           true,
		Env:               lianlianpay.EnvProd,
		Gateway:           "https://gateway.example.test/mch/",
		MerchantID:        "202601010000000001",
		PrivateKey:        privateKey,
		PlatformPublicKey: publicKey,
		BusiType:          "100001",
		RiskCategory:      "2001",
	})
	require.NoError(t, err)
	recorder := lianlianTestPost(t, router, "/option/lianlian/save", string(body))
	require.Contains(t, recorder.Body.String(), `"success":true`)
	assert.Contains(t, recorder.Body.String(), `"private_key_configured":true`)

	assert.True(t, setting.LianlianEnabled)
	assert.Equal(t, lianlianpay.EnvProd, setting.LianlianEnv)
	assert.Equal(t, "https://gateway.example.test/mch", setting.LianlianGateway)
	assert.Equal(t, "202601010000000001", setting.LianlianMchId)
	assert.Equal(t, strings.TrimSpace(privateKey), setting.LianlianPrivateKey)
	assert.Equal(t, strings.TrimSpace(publicKey), setting.LianlianPlatformPublicCert)
	assert.Equal(t, "2001", setting.LianlianRiskCategory)

	var saved model.Option
	require.NoError(t, env.db.Where("key = ?", "LianlianRiskCategory").First(&saved).Error)
	assert.Equal(t, "2001", saved.Value)

	options := httptest.NewRecorder()
	router.ServeHTTP(options, httptest.NewRequest(http.MethodGet, "/option", nil))
	assert.Contains(t, options.Body.String(), `"key":"LianlianPrivateKeyConfigured","value":"true"`)
	assert.NotContains(t, options.Body.String(), "BEGIN RSA PRIVATE KEY")

	invalidBody, err := common.Marshal(saveLianlianSettingsRequest{
		Enabled:    false,
		Env:        lianlianpay.EnvProd,
		MerchantID: "another-merchant",
		PrivateKey: "not-a-pem",
	})
	require.NoError(t, err)
	recorder = lianlianTestPost(t, router, "/option/lianlian/save", string(invalidBody))
	assert.Contains(t, recorder.Body.String(), `"success":false`)
	assert.Equal(t, "202601010000000001", setting.LianlianMchId)

	clearBody, err := common.Marshal(saveLianlianSettingsRequest{
		Enabled:           false,
		Env:               lianlianpay.EnvProd,
		Gateway:           setting.LianlianGateway,
		MerchantID:        setting.LianlianMchId,
		PlatformPublicKey: setting.LianlianPlatformPublicCert,
		BusiType:          setting.LianlianBusiType,
		RiskCategory:      setting.LianlianRiskCategory,
		ClearPrivateKey:   true,
	})
	require.NoError(t, err)
	recorder = lianlianTestPost(t, router, "/option/lianlian/save", string(clearBody))
	require.Contains(t, recorder.Body.String(), `"success":true`)
	assert.Contains(t, recorder.Body.String(), `"private_key_configured":false`)
	assert.Empty(t, setting.LianlianPrivateKey)

	options = httptest.NewRecorder()
	router.ServeHTTP(options, httptest.NewRequest(http.MethodGet, "/option", nil))
	assert.Contains(t, options.Body.String(), `"key":"LianlianPrivateKeyConfigured","value":"false"`)
}

func TestSaveLianlianSettingsBlocksIdentityChangeWithOpenOrders(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         "USR77NOCONFIGCHANGE",
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}).Error)
	privateKey, publicKey := newTestRSAKeyPEM(t)
	body, err := common.Marshal(saveLianlianSettingsRequest{
		Enabled:           true,
		Env:               setting.LianlianEnv,
		Gateway:           setting.LianlianGateway,
		MerchantID:        "202601010000000002",
		PrivateKey:        privateKey,
		PlatformPublicKey: publicKey,
		BusiType:          setting.LianlianBusiType,
		RiskCategory:      setting.LianlianRiskCategory,
	})
	require.NoError(t, err)

	recorder := lianlianTestPost(t, router, "/option/lianlian/save", string(body))
	assert.Contains(t, recorder.Body.String(), `"success":false`)
	assert.Equal(t, "2020042200284052", setting.LianlianMchId)
	assert.NotEqual(t, publicKey, setting.LianlianPlatformPublicCert)
}

func TestGenericOptionUpdateCannotBypassLianlianValidation(t *testing.T) {
	newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	originalMchID := setting.LianlianMchId

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPut,
		"/option",
		strings.NewReader(`{"key":"LianlianMchId","value":"bypass"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	assert.Contains(t, recorder.Body.String(), `"success":false`)
	assert.Equal(t, originalMchID, setting.LianlianMchId)
}

func TestRequestLianlianPayRejectsGatewayWithoutMchSuffix(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	setting.LianlianGateway = "https://openapi.example.test"

	recorder := postLianlianPay(t, router, `{"amount":1000,"payment_method":"alipay"}`)
	assert.Contains(t, recorder.Body.String(), "支付服务暂时不可用")
	assert.Zero(t, env.requests)

	var orders []model.TopUp
	require.NoError(t, env.db.Find(&orders).Error)
	assert.Empty(t, orders)
}

func TestRequestLianlianPayRejectsNonHTTPSNotifyURL(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	operation_setting.CustomCallbackAddress = "http://api.example.test"

	recorder := postLianlianPay(t, router, `{"amount":1000,"payment_method":"alipay"}`)
	assert.Contains(t, recorder.Body.String(), "支付服务暂时不可用")
	assert.Zero(t, env.requests)

	var orders []model.TopUp
	require.NoError(t, env.db.Find(&orders).Error)
	assert.Empty(t, orders)
}

func TestLianlianNotifyCreditsQuotaAndIsIdempotent(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	tradeNo := "USR77NONOTIFY"
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   "alipay",
		PaymentProvider: model.PaymentProviderLianlian,
		Status:          common.TopUpStatusPending,
	}).Error)
	body, signature := lianlianNotifyBody(t, env, tradeNo, "10.00")
	notify := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/lianlian/notify", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(lianlianpay.HeaderSignatureData, signature)
		router.ServeHTTP(recorder, request)
		return recorder
	}

	require.Equal(t, lianlianpay.SuccessResponse, notify().Body.String())
	user, err := model.GetUserById(lianlianTestUserID, false)
	require.NoError(t, err)
	assert.Equal(t, 1000+684932, user.Quota)
	order := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, order)
	assert.Equal(t, "LL2026101600PLAT", order.ProviderTradeNo)

	require.Equal(t, lianlianpay.SuccessResponse, notify().Body.String())
	user, err = model.GetUserById(lianlianTestUserID, false)
	require.NoError(t, err)
	assert.Equal(t, 1000+684932, user.Quota)
}

func TestLianlianNotifyUsesExchangeRateAtSettlement(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	tradeNo := "USR77NOEXCHANGERATE"
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		Status:          common.TopUpStatusPending,
	}).Error)
	operation_setting.USDExchangeRate = 8

	body, signature := lianlianNotifyBody(t, env, tradeNo, "10.00")
	recorder := postLianlianNotify(t, router, body, signature)
	require.Equal(t, lianlianpay.SuccessResponse, recorder.Body.String())

	user, err := model.GetUserById(lianlianTestUserID, false)
	require.NoError(t, err)
	assert.Equal(t, 1000+625000, user.Quota)
}

func TestLianlianNotifyRejectsOrderWithoutAmountUnit(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	tradeNo := "USR77NOLEGACYUNIT"
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		Status:          common.TopUpStatusPending,
	}).Error)
	operation_setting.USDExchangeRate = 8

	body, signature := lianlianNotifyBody(t, env, tradeNo, "10.00")
	recorder := postLianlianNotify(t, router, body, signature)
	require.Equal(t, lianlianpay.FailResponse, recorder.Body.String())

	user, err := model.GetUserById(lianlianTestUserID, false)
	require.NoError(t, err)
	assert.Equal(t, 1000, user.Quota)
	order := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
}

func TestLianlianNotifyRejectsInvalidRequests(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	tradeNo := "USR77NOINVALID"
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   "alipay",
		PaymentProvider: model.PaymentProviderLianlian,
		Status:          common.TopUpStatusPending,
	}).Error)

	postSigned := func(body string, signature string) string {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/lianlian/notify", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(lianlianpay.HeaderSignatureData, signature)
		router.ServeHTTP(recorder, request)
		return recorder.Body.String()
	}

	validBody, validSignature := lianlianNotifyBody(t, env, tradeNo, "10.00")
	tampered := strings.Replace(validBody, `"10.00"`, `"9.99"`, 1)
	assert.Equal(t, lianlianpay.FailResponse, postSigned(tampered, validSignature))
	assert.Equal(t, lianlianpay.FailResponse, postSigned(validBody, "AAAA"))

	mismatchBody, mismatchSignature := lianlianNotifyBody(t, env, tradeNo, "9.99")
	assert.Equal(t, lianlianpay.FailResponse, postSigned(mismatchBody, mismatchSignature))
	order := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusPending, order.Status)

	missingBody, missingSignature := lianlianNotifyBody(t, env, "USR77NOMISSING", "10.00")
	assert.Equal(t, lianlianpay.FailResponse, postSigned(missingBody, missingSignature))

	missingAmountBody := fmt.Sprintf(
		`{"mch_id":%q,"txn_seqno":%q,"result_pay":"SUCCESS","platform_txno":"LLNOAMOUNT","pay_method_infos":[{"pay_type":%q}]}`,
		setting.LianlianMchId,
		tradeNo,
		lianlianpay.PayTypeAlipayNative,
	)
	missingAmountSignature, err := lianlianpay.Sign(env.platformPrivKey, []byte(missingAmountBody))
	require.NoError(t, err)
	assert.Equal(t, lianlianpay.FailResponse, postSigned(missingAmountBody, missingAmountSignature))

	missingStatusBody := fmt.Sprintf(
		`{"mch_id":%q,"txn_seqno":%q,"order_amount":"10.00","platform_txno":"LLNOSTATUS","pay_method_infos":[{"pay_type":%q,"amount":"10.00"}]}`,
		setting.LianlianMchId,
		tradeNo,
		lianlianpay.PayTypeAlipayNative,
	)
	missingStatusSignature, err := lianlianpay.Sign(env.platformPrivKey, []byte(missingStatusBody))
	require.NoError(t, err)
	assert.Equal(t, lianlianpay.FailResponse, postSigned(missingStatusBody, missingStatusSignature))
}

func TestLianlianNotifyRequiresProviderTradeNo(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	tradeNo := "USR77NOPROVIDERTX"
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		Status:          common.TopUpStatusPending,
	}).Error)

	body := fmt.Sprintf(
		`{"mch_id":%q,"txn_seqno":%q,"order_amount":"10.00","txn_status":"SUCCESS","pay_method_infos":[{"pay_type":%q,"amount":"10.00"}]}`,
		setting.LianlianMchId,
		tradeNo,
		lianlianpay.PayTypeAlipayNative,
	)
	signature, err := lianlianpay.Sign(env.platformPrivKey, []byte(body))
	require.NoError(t, err)

	recorder := postLianlianNotify(t, router, body, signature)
	assert.Equal(t, lianlianpay.FailResponse, recorder.Body.String())
	assert.Equal(t, common.TopUpStatusPending, model.GetTopUpByTradeNo(tradeNo).Status)
	user, err := model.GetUserById(lianlianTestUserID, false)
	require.NoError(t, err)
	assert.Equal(t, 1000, user.Quota)
}

func TestLianlianNotifyRejectsMerchantMismatch(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	tradeNo := "USR77NOMCHMISMATCH"
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		Status:          common.TopUpStatusPending,
	}).Error)

	body, signature := lianlianNotifyBodyFor(
		t,
		env,
		tradeNo,
		"10.00",
		"another-merchant",
		lianlianpay.PayTypeAlipayNative,
	)
	recorder := postLianlianNotify(t, router, body, signature)

	assert.Equal(t, lianlianpay.FailResponse, recorder.Body.String())
	order := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
}

func TestLianlianNotifyRejectsPaymentMethodMismatch(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	tradeNo := "USR77NOPAYMETHODMISMATCH"
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		Status:          common.TopUpStatusPending,
	}).Error)

	body, signature := lianlianNotifyBodyFor(
		t,
		env,
		tradeNo,
		"10.00",
		setting.LianlianMchId,
		lianlianpay.PayTypeWechatNative,
	)
	recorder := postLianlianNotify(t, router, body, signature)

	assert.Equal(t, lianlianpay.FailResponse, recorder.Body.String())
	order := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
}

func TestLianlianNotifySettlesAfterTopUpDisabled(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	tradeNo := "USR77NODISABLED"
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   "alipay",
		PaymentProvider: model.PaymentProviderLianlian,
		Status:          common.TopUpStatusPending,
	}).Error)

	body, signature := lianlianNotifyBody(t, env, tradeNo, "10.00")
	setting.LianlianEnabled = false

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/lianlian/notify", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(lianlianpay.HeaderSignatureData, signature)
	router.ServeHTTP(recorder, request)
	assert.Equal(t, lianlianpay.SuccessResponse, recorder.Body.String())
	order := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusSuccess, order.Status)
}

func TestLianlianNotifyCanSettleRecoverableOrderStates(t *testing.T) {
	for _, initialStatus := range []string{
		common.TopUpStatusFailed,
		common.TopUpStatusExpired,
		common.TopUpStatusCancelled,
	} {
		t.Run(initialStatus, func(t *testing.T) {
			env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
			router := lianlianTestRouter(lianlianTestUserID)
			tradeNo := "USR77NORECOVER" + strings.ToUpper(initialStatus)
			require.NoError(t, env.db.Create(&model.TopUp{
				UserId:          lianlianTestUserID,
				Amount:          1000,
				AmountUnit:      model.AmountUnitCNYCent,
				Money:           1000,
				TradeNo:         tradeNo,
				PaymentMethod:   lianlianpay.PayMethodAlipay,
				PaymentProvider: model.PaymentProviderLianlian,
				Status:          initialStatus,
			}).Error)

			body, signature := lianlianNotifyBody(t, env, tradeNo, "10.00")
			recorder := postLianlianNotify(t, router, body, signature)
			assert.Equal(t, lianlianpay.SuccessResponse, recorder.Body.String())

			order := model.GetTopUpByTradeNo(tradeNo)
			require.NotNil(t, order)
			assert.Equal(t, common.TopUpStatusSuccess, order.Status)
		})
	}
}

func TestGetLianlianOrderOnlyReturnsOwnOrders(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	require.NoError(t, env.db.Create(&model.User{
		Id:       lianlianTestUserID + 1,
		Username: "other_lianlian_user",
		AffCode:  "other-lianlian-aff",
		Status:   common.UserStatusEnabled,
	}).Error)
	tradeNo := "USR77NOOTHER"
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   "alipay",
		PaymentProvider: model.PaymentProviderLianlian,
		Status:          common.TopUpStatusPending,
	}).Error)

	get := func(userId int) string {
		recorder := httptest.NewRecorder()
		router := lianlianTestRouter(userId)
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/lianlian/order/"+tradeNo, nil))
		return recorder.Body.String()
	}

	assert.Contains(t, get(lianlianTestUserID), `"message":"success"`)
	assert.Contains(t, get(lianlianTestUserID+1), "订单不存在")

	cancel := httptest.NewRecorder()
	lianlianTestRouter(lianlianTestUserID+1).
		ServeHTTP(cancel, httptest.NewRequest(http.MethodPost, "/lianlian/order/"+tradeNo+"/cancel", nil))
	assert.Contains(t, cancel.Body.String(), "订单不存在")
	assert.Equal(t, common.TopUpStatusPending, model.GetTopUpByTradeNo(tradeNo).Status)
}

func TestCancelLianlianOrderBlocksPayment(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	tradeNo := "USR77NOCANCEL"
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}).Error)

	status := httptest.NewRecorder()
	router.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/lianlian/order/"+tradeNo, nil))
	assert.Contains(t, status.Body.String(), `"status":"pending"`)
	assert.Contains(t, status.Body.String(), `"can_pay":true`)

	cancel := httptest.NewRecorder()
	router.ServeHTTP(cancel, httptest.NewRequest(http.MethodPost, "/lianlian/order/"+tradeNo+"/cancel", nil))
	assert.Contains(t, cancel.Body.String(), `"message":"success"`)
	assert.Contains(t, cancel.Body.String(), `"status":"cancelled"`)
	assert.Equal(t, common.TopUpStatusCancelled, model.GetTopUpByTradeNo(tradeNo).Status)
	assert.Zero(t, env.requests)

	status = httptest.NewRecorder()
	router.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/lianlian/order/"+tradeNo, nil))
	assert.Contains(t, status.Body.String(), `"status":"cancelled"`)
	assert.Contains(t, status.Body.String(), `"can_pay":false`)
	assert.Contains(t, status.Body.String(), "订单已取消，请重新创建订单")

	cancel = httptest.NewRecorder()
	router.ServeHTTP(cancel, httptest.NewRequest(http.MethodPost, "/lianlian/order/"+tradeNo+"/cancel", nil))
	assert.Contains(t, cancel.Body.String(), `"message":"success"`)
	assert.Contains(t, cancel.Body.String(), `"status":"cancelled"`)
}

func TestCancelLianlianOrderCannotOverwritePaidOrder(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	tradeNo := "USR77NOPAIDCANCEL"
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusSuccess,
	}).Error)

	cancel := httptest.NewRecorder()
	router.ServeHTTP(cancel, httptest.NewRequest(http.MethodPost, "/lianlian/order/"+tradeNo+"/cancel", nil))
	assert.Contains(t, cancel.Body.String(), `"message":"error"`)
	assert.Equal(t, common.TopUpStatusSuccess, model.GetTopUpByTradeNo(tradeNo).Status)
}

func TestLianlianOrderExpiresAfterTTL(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	createdAt := time.Now().Add(-time.Duration(lianlianOrderTTLSeconds+1) * time.Second).Unix()

	for _, status := range []string{common.TopUpStatusPending, common.TopUpStatusCancelled} {
		t.Run(status, func(t *testing.T) {
			tradeNo := "USR77NOEXPIRE" + strings.ToUpper(status)
			require.NoError(t, env.db.Create(&model.TopUp{
				UserId:          lianlianTestUserID,
				Amount:          1000,
				AmountUnit:      model.AmountUnitCNYCent,
				Money:           1000,
				TradeNo:         tradeNo,
				PaymentMethod:   lianlianpay.PayMethodAlipay,
				PaymentProvider: model.PaymentProviderLianlian,
				CreateTime:      createdAt,
				Status:          status,
			}).Error)

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/lianlian/order/"+tradeNo, nil))

			assert.Contains(t, recorder.Body.String(), `"status":"expired"`)
			assert.Contains(t, recorder.Body.String(), `"can_pay":false`)
			assert.Equal(t, common.TopUpStatusExpired, model.GetTopUpByTradeNo(tradeNo).Status)
		})
	}
}

func TestCancelLianlianOrderRejectsExpiredOrder(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	tradeNo := "USR77NOCANCELEXPIRED"
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		CreateTime:      time.Now().Add(-time.Duration(lianlianOrderTTLSeconds+1) * time.Second).Unix(),
		Status:          common.TopUpStatusPending,
	}).Error)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/lianlian/order/"+tradeNo+"/cancel", nil))

	assert.Contains(t, recorder.Body.String(), `"message":"error"`)
	assert.Equal(t, common.TopUpStatusExpired, model.GetTopUpByTradeNo(tradeNo).Status)
}

func TestCancelLianlianOrderRejectsDifferentUser(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	tradeNo := "USR77NOOWNERCANCEL"
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		OperatorUserId:  lianlianTestUserID + 1,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}).Error)

	recorder := httptest.NewRecorder()
	lianlianTestRouter(lianlianTestUserID+1).
		ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/lianlian/order/"+tradeNo+"/cancel", nil))

	assert.Contains(t, recorder.Body.String(), "订单不存在")
	assert.Equal(t, common.TopUpStatusPending, model.GetTopUpByTradeNo(tradeNo).Status)
}

func TestRequestLianlianPaySkipsWalletCapacityThatWouldOverflow(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	router := lianlianTestRouter(lianlianTestUserID)
	require.NoError(t, env.db.Model(&model.User{}).
		Where("id = ?", lianlianTestUserID).
		Update("quota", common.MaxWalletQuota-100_000).Error)

	recorder := postLianlianPay(t, router, `{"amount":2000,"payment_method":"alipay"}`)
	assert.Contains(t, recorder.Body.String(), "top-up quota limit exceeded")
	assert.Zero(t, env.requests)
}

func TestUpdateLianlianProviderTradeNoDoesNotOverwriteSettlementStatus(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	tradeNo := "USR77NOSETTLEMENTRACE"
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		Status:          common.TopUpStatusSuccess,
	}).Error)

	require.NoError(t, model.UpdateTopUpProviderTradeNo(tradeNo, "LLRACE1"))
	order := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusSuccess, order.Status)
	assert.Equal(t, "LLRACE1", order.ProviderTradeNo)
	assert.ErrorIs(t, model.UpdateTopUpProviderTradeNo(tradeNo, "LLRACE2"), model.ErrLianlianProviderTradeNoMismatch)
}

func TestManualCompleteLianlianTopUpUsesAmountCents(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	tradeNo := "USR77NOMANUAL"
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		Status:          common.TopUpStatusPending,
	}).Error)

	require.NoError(t, model.ManualCompleteTopUp(tradeNo, "127.0.0.1"))
	user, err := model.GetUserById(lianlianTestUserID, false)
	require.NoError(t, err)
	assert.Equal(t, 1000+684932, user.Quota)
}

func TestReconcileLianlianTopUpsSettlesPaidPendingOrder(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	tradeNo := "USR77NORECONCILE"
	env.queryResponse = fmt.Sprintf(
		`{"ret_code":"0000","ret_msg":"成功","mch_id":%q,"pay_type":"ALIPAY_NATIVE","txn_seqno":%q,"platform_txno":"LLRECON1","order_amount":"10.00","txn_status":"SUCCESS","pay_method_infos":[{"pay_type":"ALIPAY_NATIVE","amount":"10.00"}]}`,
		setting.LianlianMchId,
		tradeNo,
	)
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		CreateTime:      time.Now().Add(-10 * time.Minute).Unix(),
		Status:          common.TopUpStatusPending,
	}).Error)

	summary, err := ReconcileLianlianTopUpsOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Scanned)
	assert.Equal(t, 1, summary.Settled)
	assert.Equal(t, 0, summary.Failed)
	assert.Equal(t, "/query"+lianlianpay.OrderQueryPath, env.lastPath)

	order := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusSuccess, order.Status)
	assert.Equal(t, "LLRECON1", order.ProviderTradeNo)
	user, err := model.GetUserById(lianlianTestUserID, false)
	require.NoError(t, err)
	assert.Equal(t, 1000+684932, user.Quota)
}

func TestReconcileLianlianTopUpsSettlesPaidCancelledOrder(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	tradeNo := "USR77NOPAIDCANCELLED"
	env.queryResponse = fmt.Sprintf(
		`{"ret_code":"0000","ret_msg":"成功","mch_id":%q,"pay_type":"ALIPAY_NATIVE","txn_seqno":%q,"platform_txno":"LLCANCELLEDPAID","order_amount":"10.00","txn_status":"SUCCESS","pay_method_infos":[{"pay_type":"ALIPAY_NATIVE","amount":"10.00"}]}`,
		setting.LianlianMchId,
		tradeNo,
	)
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		CreateTime:      time.Now().Add(-10 * time.Minute).Unix(),
		Status:          common.TopUpStatusCancelled,
	}).Error)

	summary, err := ReconcileLianlianTopUpsOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Settled)

	order := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusSuccess, order.Status)
	assert.Equal(t, "LLCANCELLEDPAID", order.ProviderTradeNo)
}

func TestReconcileLianlianTopUpsExpiresCancelledOrder(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	tradeNo := "USR77NOEXPIREDCANCEL"
	env.queryResponse = fmt.Sprintf(
		`{"ret_code":"2003","ret_msg":"订单已过期","mch_id":%q,"txn_seqno":%q}`,
		setting.LianlianMchId,
		tradeNo,
	)
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		CreateTime:      time.Now().Add(-10 * time.Minute).Unix(),
		Status:          common.TopUpStatusCancelled,
	}).Error)

	summary, err := ReconcileLianlianTopUpsOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Closed)

	order := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusExpired, order.Status)
}

func TestReconcileLianlianTopUpsExpiresUnpaidOrderAfterTTL(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	tradeNo := "USR77NOLOCALEXPIRY"
	env.queryResponse = fmt.Sprintf(
		`{"ret_code":"0000","ret_msg":"成功","mch_id":%q,"pay_type":"ALIPAY_NATIVE","txn_seqno":%q,"txn_status":"PROCESSING"}`,
		setting.LianlianMchId,
		tradeNo,
	)
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		CreateTime:      time.Now().Add(-time.Duration(lianlianOrderTTLSeconds+1) * time.Second).Unix(),
		Status:          common.TopUpStatusCancelled,
	}).Error)

	summary, err := ReconcileLianlianTopUpsOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Closed)

	order := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusExpired, order.Status)
}

func TestReconcileLianlianTopUpsClosesTerminalOrders(t *testing.T) {
	testCases := []struct {
		name           string
		tradeSuffix    string
		queryResponse  string
		expectedStatus string
	}{
		{
			name:           "payment failure without amount",
			tradeSuffix:    "FAILED",
			queryResponse:  `{"ret_code":"0000","ret_msg":"成功","mch_id":%q,"pay_type":"ALIPAY_NATIVE","txn_seqno":%q,"platform_txno":"LLFAILED","txn_status":"FAILURE"}`,
			expectedStatus: common.TopUpStatusFailed,
		},
		{
			name:           "provider expired",
			tradeSuffix:    "EXPIRED",
			queryResponse:  `{"ret_code":"2003","ret_msg":"订单已过期","mch_id":%q,"txn_seqno":%q}`,
			expectedStatus: common.TopUpStatusExpired,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
			tradeNo := "USR77NOTERM" + tc.tradeSuffix
			env.queryResponse = fmt.Sprintf(tc.queryResponse, setting.LianlianMchId, tradeNo)
			require.NoError(t, env.db.Create(&model.TopUp{
				UserId:          lianlianTestUserID,
				Amount:          1000,
				AmountUnit:      model.AmountUnitCNYCent,
				Money:           1000,
				TradeNo:         tradeNo,
				PaymentMethod:   lianlianpay.PayMethodAlipay,
				PaymentProvider: model.PaymentProviderLianlian,
				CreateTime:      time.Now().Add(-10 * time.Minute).Unix(),
				Status:          common.TopUpStatusPending,
			}).Error)

			summary, err := ReconcileLianlianTopUpsOnce(context.Background())
			require.NoError(t, err)
			assert.Equal(t, 1, summary.Scanned)
			assert.Equal(t, 1, summary.Closed)
			assert.Zero(t, summary.Failed)
			order := model.GetTopUpByTradeNo(tradeNo)
			require.NotNil(t, order)
			assert.Equal(t, tc.expectedStatus, order.Status)
		})
	}
}

func TestReconcileLianlianTopUpsReportsPerOrderFailures(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	tradeNo := "USR77NORECONCILEFAILURE"
	env.queryResponse = fmt.Sprintf(
		`{"ret_code":"0000","ret_msg":"成功","mch_id":"different-merchant","txn_seqno":%q,"txn_status":"SUCCESS"}`,
		tradeNo,
	)
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		CreateTime:      time.Now().Add(-10 * time.Minute).Unix(),
		Status:          common.TopUpStatusPending,
	}).Error)

	summary, err := ReconcileLianlianTopUpsOnce(context.Background())
	require.Error(t, err)
	assert.Equal(t, 1, summary.Scanned)
	assert.Equal(t, 1, summary.Failed)
}

func TestReconcileLianlianTopUpsRequiresProviderTradeNo(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	tradeNo := "USR77NORECONCILENOTX"
	env.queryResponse = fmt.Sprintf(
		`{"ret_code":"0000","ret_msg":"成功","mch_id":%q,"pay_type":"ALIPAY_NATIVE","txn_seqno":%q,"order_amount":"10.00","txn_status":"SUCCESS","pay_method_infos":[{"pay_type":"ALIPAY_NATIVE","amount":"10.00"}]}`,
		setting.LianlianMchId,
		tradeNo,
	)
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		CreateTime:      time.Now().Add(-10 * time.Minute).Unix(),
		Status:          common.TopUpStatusPending,
	}).Error)

	summary, err := ReconcileLianlianTopUpsOnce(context.Background())
	require.Error(t, err)
	assert.Equal(t, 1, summary.Failed)
	assert.Equal(t, common.TopUpStatusPending, model.GetTopUpByTradeNo(tradeNo).Status)
}

func TestReconcileLianlianTopUpsQueriesOrdersOlderThanLookback(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	tradeNo := "USR77NOOLDORDER"
	env.queryResponse = fmt.Sprintf(
		`{"ret_code":"0000","ret_msg":"成功","mch_id":%q,"pay_type":"ALIPAY_NATIVE","txn_seqno":%q,"platform_txno":"LLOLD","order_amount":"10.00","txn_status":"SUCCESS","pay_method_infos":[{"pay_type":"ALIPAY_NATIVE","amount":"10.00"}]}`,
		setting.LianlianMchId,
		tradeNo,
	)
	require.NoError(t, env.db.Create(&model.TopUp{
		UserId:          lianlianTestUserID,
		Amount:          1000,
		AmountUnit:      model.AmountUnitCNYCent,
		Money:           1000,
		TradeNo:         tradeNo,
		PaymentMethod:   lianlianpay.PayMethodAlipay,
		PaymentProvider: model.PaymentProviderLianlian,
		CreateTime:      time.Now().Add(-48 * time.Hour).Unix(),
		Status:          common.TopUpStatusPending,
	}).Error)

	summary, err := ReconcileLianlianTopUpsOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Scanned)
	assert.Equal(t, 1, summary.Settled)
	order := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusSuccess, order.Status)
}

func TestReconcileLianlianTopUpsDoesNotStarveAfterFirstBatch(t *testing.T) {
	env := newLianlianTestEnv(t, lianlianTestCreatePayResponse)
	tradeNos := make([]string, 0, lianlianReconcileBatchSize+1)
	for i := 0; i <= lianlianReconcileBatchSize; i++ {
		tradeNo := fmt.Sprintf("USR77NOSTARVE%02d", i)
		tradeNos = append(tradeNos, tradeNo)
		require.NoError(t, env.db.Create(&model.TopUp{
			UserId:          lianlianTestUserID,
			Amount:          1000,
			AmountUnit:      model.AmountUnitCNYCent,
			Money:           1000,
			TradeNo:         tradeNo,
			PaymentMethod:   lianlianpay.PayMethodAlipay,
			PaymentProvider: model.PaymentProviderLianlian,
			CreateTime:      time.Now().Add(-10 * time.Minute).Unix(),
			Status:          common.TopUpStatusPending,
		}).Error)
		env.queryResponses[tradeNo] = fmt.Sprintf(
			`{"ret_code":"0000","ret_msg":"成功","mch_id":%q,"txn_seqno":%q,"txn_status":"PROCESSING"}`,
			setting.LianlianMchId,
			tradeNo,
		)
	}
	paidTradeNo := tradeNos[len(tradeNos)-1]
	env.queryResponses[paidTradeNo] = fmt.Sprintf(
		`{"ret_code":"0000","ret_msg":"成功","mch_id":%q,"pay_type":"ALIPAY_NATIVE","txn_seqno":%q,"platform_txno":"LLLAST","order_amount":"10.00","txn_status":"SUCCESS","pay_method_infos":[{"pay_type":"ALIPAY_NATIVE","amount":"10.00"}]}`,
		setting.LianlianMchId,
		paidTradeNo,
	)

	summary, err := ReconcileLianlianTopUpsOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, lianlianReconcileBatchSize+1, summary.Scanned)
	assert.Equal(t, 1, summary.Settled)
	assert.Equal(t, lianlianReconcileBatchSize, summary.Skipped)

	order := model.GetTopUpByTradeNo(paidTradeNo)
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusSuccess, order.Status)
}
