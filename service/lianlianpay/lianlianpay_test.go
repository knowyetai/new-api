package lianlianpay

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 文档 https://api-doc.lianlianpay.com/openplatform/doc-338855 公布的官方向量。
const (
	docSampleBody    = `{"card_no":"9558834301000461394","mch_id":"2020042200284052"}`
	docSampleMd5     = "51a592df873c64dd2c2c88ce8c365ec7"
	docSampleSign    = "q2G8ulbvKZEOkBCeyK8s++rATQfMjvn0JhQGELe7wn8Qih0qmuM3pgjJ9BVDza3qcGcLbKXfUCusBW17fAjyyWz33ucgxyS9VVUrgFsO0acVlrQK7xJpIy0X6Z2jjNvSZhpPxLxktDZyRHA7WGQZFo1SL5HsUMlBIxj69OjAG2c="
	docSamplePrivKey = `-----BEGIN RSA PRIVATE KEY-----
MIICXAIBAAKBgQDw/pmpStlt1CV31+azu/YEVGy3vpMArJMO1cxE1wOEH/+VVpT8Tm9ekpX5ZBOM8iJ4RsHoY1E71z73mzbWSSaSkvkP+6FsOoTENXCAvfQ1plejq47c+tU5ouQP+EJ2KlJthkUnH658iC0MPf6opJL8o6NKT7CDE9q4Zrt9S0skjQIDAQABAoGBAKJGrm3iqtpbugci6IQqkxSDTNnFh/MyQawBpxTLu+BiM5X5wUTMBA4oorhV/Rwxh6h57ORwzCiBXjMY3yoYA9Hsad0tOsaUm5pCG71izPsQZe9P/kixu8M7WeCrXSruz0dusSYbeszPyg66L7kcxWDQemW8jtzMvQiV2mUuhlP5AkEA/v8LH9DOwcqfrD88t0hJ/JGrRaKVTs/KBEgQbkjTV5da4Mln4FVwVCkQwLrPKGvzYcds1jaqbnIax3qz6eTHBwJBAPHxconVuJzJC98eFC0Jm7GQarDRF6Cr93ww7x1kZY19ABJXpfl2lbsa7aWIg8cKhUjNLPdpwgY9mV7eDvndnssCQEuZhczK9WGTFz/UB8Y3EmhlL6gXibix9izKyMRJpw7i+riGPdvOuNzax/K778sGJkQpAeWIk4LnJXVatZh0kWUCQGJ8vPlq7Km2FYUPw3rJ7H0NosCzfQDEwAvoB45ZbhV4iuzsjP3NzgnKXm8B8VK276bBDsrwXDEaJjK+Bi6kjp0CQHAyqF6ZGBRmpHo5+i0I+exAbTIH1TWrU4qQZ0MT2hzBhjvVfUs3nW5IDYNDQUs7DbGripxTsvshcBan+qBh5tA=
-----END RSA PRIVATE KEY-----`
	docSamplePubKey = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDw/pmpStlt1CV31+azu/YEVGy3vpMArJMO1cxE1wOEH/+VVpT8Tm9ekpX5ZBOM8iJ4RsHoY1E71z73mzbWSSaSkvkP+6FsOoTENXCAvfQ1plejq47c+tU5ouQP+EJ2KlJthkUnH658iC0MPf6opJL8o6NKT7CDE9q4Zrt9S0skjQIDAQAB
-----END PUBLIC KEY-----`

	platformNotifyBody = `{"accountInfo":{"accountLevel":"V3","accountNeedLevel":"V3","accountNeedType":"PERSONAL_PAYMENT_ACCOUNT","accountType":"PERSONAL_PAYMENT_ACCOUNT"},"accp_txno":"20230829000012374866","oid_partner":"2020042200284052","oid_userno":"2023082813762354","txn_seqno":"1696339459111317504","user_id":"741852963","user_status":"NORMAL"}`
	platformNotifySign = "Gkb/kkuByKHzsPPmAn89hjvxsyJZN02dUCaa5m0nf9fxdaWDCvo5mZdhYz6XBrDY7dg0kgM/yMyEpalCA+24mIW80OjL+yLTUkJMcGcCaEBDjMyT30KwU2jkbbGUmLMSfTdBUoPFTqeEQRNXibP1c0uszWwuiQToOkiSg0UpvXM="
)

func TestMd5HexMatchesOfficialVector(t *testing.T) {
	assert.Equal(t, docSampleMd5, Md5Hex([]byte(docSampleBody)))
	assert.Equal(t, "d41d8cd98f00b204e9800998ecf8427e", Md5Hex(nil))
	assert.Equal(t, "900150983cd24fb0d6963f7d28e17f72", Md5Hex([]byte("abc")))
}

func TestSignMatchesOfficialVector(t *testing.T) {
	signature, err := Sign(docSamplePrivKey, []byte(docSampleBody))
	require.NoError(t, err)
	assert.Equal(t, docSampleSign, signature, "PKCS#1 v1.5 signing is deterministic")
}

func TestVerifyAcceptsOfficialPlatformNotify(t *testing.T) {
	ok, err := Verify(PlatformPublicKey, []byte(platformNotifyBody), platformNotifySign)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "dc21d276613d34970c10356adb9a8176", Md5Hex([]byte(platformNotifyBody)))

	ok, err = Verify(PlatformPublicKey, []byte(strings.Replace(platformNotifyBody, "V3", "V2", 1)), platformNotifySign)
	require.NoError(t, err)
	assert.False(t, ok)

	ok, err = Verify(PlatformPublicKey, []byte(platformNotifyBody), "AAAA")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	body := []byte(`{"mch_id":"2020042200284052","txn_seqno":"USR1NO123"}`)
	signature, err := Sign(docSamplePrivKey, body)
	require.NoError(t, err)
	ok, err := Verify(docSamplePubKey, body, signature)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestSignRejectsMissingOrInvalidPrivateKey(t *testing.T) {
	_, err := Sign("", []byte(`{}`))
	assert.ErrorIs(t, err, ErrEmptyPrivateKey)

	_, err = Sign("not-a-pem", []byte(`{}`))
	assert.ErrorContains(t, err, "PEM")
}

func TestVerifyRejectsMissingOrInvalidPublicKey(t *testing.T) {
	_, err := Verify("", []byte(`{}`), "sig")
	assert.ErrorIs(t, err, ErrEmptyPublicKey)

	ok, err := Verify("not-a-pem", []byte(`{}`), "sig")
	assert.ErrorContains(t, err, "PEM")
	assert.False(t, ok)
}

func TestResolvePlatformKeyFallsBackToBuiltin(t *testing.T) {
	assert.Equal(t, PlatformPublicKey, ResolvePlatformKey(""))
	assert.Equal(t, PlatformPublicKey, ResolvePlatformKey("   "))
	assert.Equal(t, "custom", ResolvePlatformKey("custom"))
}

func TestFormatTimestampUsesBeijingTime(t *testing.T) {
	// 文档用例：2026-09-24 04:04:15 UTC == 2026-09-24 12:04:15 CST
	moment := time.Date(2026, 9, 24, 4, 4, 15, 0, time.UTC)
	assert.Equal(t, "20260924120415", FormatTimestamp(moment))
	assert.Len(t, NowTimestamp(), len(TimestampLayout))
	assert.Regexp(t, `^\d{14}$`, NowTimestamp())
}

func TestGatewayURLSelection(t *testing.T) {
	testCases := []struct {
		name     string
		env      string
		override string
		want     string
		wantErr  bool
	}{
		{name: "test default", env: EnvTest, want: GatewayTest + CreatePayPath},
		{name: "prod default", env: EnvProd, want: GatewayProd + CreatePayPath},
		{name: "unknown env uses test gateway", env: "unknown", want: GatewayTest + CreatePayPath},
		{name: "configured base is used verbatim", env: EnvProd, override: "https://sandbox.example.com/ll/mch", want: "https://sandbox.example.com/ll/mch" + CreatePayPath},
		{name: "trailing slash is trimmed", env: EnvProd, override: "https://sandbox.example.com/ll/mch/", want: "https://sandbox.example.com/ll/mch" + CreatePayPath},
		{name: "surrounding spaces are trimmed", env: EnvTest, override: "  https://sandbox.example.com/ll/mch  ", want: "https://sandbox.example.com/ll/mch" + CreatePayPath},
		{name: "origin without mch is rejected", env: EnvProd, override: "https://sandbox.example.com/ll", wantErr: true},
		{name: "full create pay url is rejected", env: EnvTest, override: GatewayTest + CreatePayPath, wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GatewayURL(tc.env, tc.override)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrGatewayMissingMchSuffix)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPayTypeMap(t *testing.T) {
	assert.Equal(t, PayTypeAlipayNative, PayTypeOf("alipay"))
	assert.Equal(t, PayTypeWechatNative, PayTypeOf("wxpay"))
	assert.Equal(t, "", PayTypeOf("card"))
	assert.True(t, SupportsPayMethod("alipay"))
	assert.False(t, SupportsPayMethod("stripe"))
	assert.Equal(t, PayMethodAlipay, PayMethodForPayType(PayTypeAlipayNative))
	assert.Equal(t, PayMethodWxpay, PayMethodForPayType(PayTypeWechatNative))
	assert.Equal(t, "", PayMethodForPayType("ALIPAY_MICROPAY"))
}

func TestBuildCreatePayBody(t *testing.T) {
	body, err := BuildCreatePayBody(CreatePayRequest{
		MerchantID: "2020042200284052",
		UserID:     "42",
		TradeNo:    "USR42NOabcdef1234567890",
		Money:      "1.00",
		GoodsName:  "TUC10",
		NotifyURL:  "https://api.example.com/api/user/lianlian/notify",
		PayMethod:  "alipay",
		ClientIP:   "1.2.3.4",
		Timestamp:  "20260101120000",
	})
	require.NoError(t, err)

	assert.Equal(t, "2020042200284052", body["mch_id"])
	assert.Equal(t, DefaultBusiType, body["busi_type"])
	assert.Equal(t, "20260101120000", body["txn_time"])
	assert.Equal(t, "USR42NOabcdef1234567890", body["txn_seqno"])
	assert.Equal(t, "1.00", body["order_amount"])
	assert.Equal(t, PayExpireMinutes, body["pay_expire"])
	assert.Equal(t, "TUC10", body["order_info"])
	assert.Equal(t, "https://api.example.com/api/user/lianlian/notify", body["notify_url"])
	assert.Equal(t, []map[string]any{{"pay_type": PayTypeAlipayNative, "amount": "1.00"}}, body["pay_method_infos"])
	assert.Equal(t, []map[string]any{{
		"goods_id":       "API_CREDIT",
		"goods_name":     "TUC10",
		"goods_quantity": "1",
		"goods_price":    "1.00",
	}}, body["goods_info"])
	assert.Equal(t, map[string]any{"device_id": DefaultDeviceID, "device_type": "PC", "device_ip": "1.2.3.4"}, body["device_info"])
	assert.NotContains(t, body, "payee_infos")
	assert.Contains(t, body["risk_item"], "frms_ware_category")
	assert.Contains(t, body["risk_item"], `"user_info_mercht_userno":"42"`)
}

func TestBuildCreatePayBodyMapsWxpayAndIPV6Loopback(t *testing.T) {
	body, err := BuildCreatePayBody(CreatePayRequest{
		MerchantID: "m",
		TradeNo:    "o1",
		Money:      "2.00",
		PayMethod:  "wxpay",
		ClientIP:   "::ffff:127.0.0.1",
		NotifyURL:  "https://api.example.com/api/user/lianlian/notify",
		Timestamp:  "20260101120000",
	})
	require.NoError(t, err)
	assert.Equal(t, PayTypeWechatNative, body["pay_method_infos"].([]map[string]any)[0]["pay_type"])
	assert.Equal(t, "127.0.0.1", body["device_info"].(map[string]any)["device_ip"])
}

func TestBuildCreatePayBodyRejectsInvalidInput(t *testing.T) {
	_, err := BuildCreatePayBody(CreatePayRequest{TradeNo: "t", Money: "1.00", PayMethod: "alipay"})
	assert.ErrorContains(t, err, "商户号未配置")

	_, err = BuildCreatePayBody(CreatePayRequest{MerchantID: "m", Money: "1.00", PayMethod: "alipay"})
	assert.ErrorContains(t, err, "订单号不能为空")

	_, err = BuildCreatePayBody(CreatePayRequest{MerchantID: "m", TradeNo: strings.Repeat("a", 33), Money: "1.00", PayMethod: "alipay"})
	assert.ErrorContains(t, err, "不能超过 32")

	_, err = BuildCreatePayBody(CreatePayRequest{MerchantID: "m", TradeNo: "t", PayMethod: "alipay"})
	assert.ErrorContains(t, err, "金额不能为空")

	_, err = BuildCreatePayBody(CreatePayRequest{MerchantID: "m", TradeNo: "t", Money: "1.00", NotifyURL: "https://example.com/notify", PayMethod: "stripe"})
	assert.ErrorContains(t, err, "不支持的支付方式")

	_, err = BuildCreatePayBody(CreatePayRequest{MerchantID: "m", TradeNo: "t", Money: "0.001", NotifyURL: "https://example.com/notify", PayMethod: "alipay"})
	assert.ErrorContains(t, err, "支付金额必须在")

	_, err = BuildCreatePayBody(CreatePayRequest{MerchantID: "m", TradeNo: "t", Money: "1000000.00", NotifyURL: "https://example.com/notify", PayMethod: "alipay"})
	assert.ErrorContains(t, err, "支付金额必须在")

	_, err = BuildCreatePayBody(CreatePayRequest{MerchantID: "m", TradeNo: "t", Money: "1.00", PayMethod: "alipay"})
	assert.ErrorContains(t, err, "支付回调地址未配置")
}

func TestBuildCreatePayRequestSignsExactBody(t *testing.T) {
	signed, err := BuildCreatePayRequest(CreatePayRequest{
		Gateway:    GatewayTest,
		PrivateKey: docSamplePrivKey,
		MerchantID: "2020042200284052",
		TradeNo:    "USR1NOabc",
		Money:      "0.01",
		PayMethod:  "alipay",
		NotifyURL:  "https://api.example.com/api/user/lianlian/notify",
		Timestamp:  "20260101120000",
	})
	require.NoError(t, err)

	assert.Equal(t, GatewayTest+CreatePayPath, signed.URL)
	assert.Equal(t, "20260101120000", signed.Timestamp)
	assert.Equal(t, "2020042200284052", signed.MerchantID)

	ok, err := Verify(docSamplePubKey, signed.Body, signed.Signature)
	require.NoError(t, err)
	assert.True(t, ok, "signature must cover the exact bytes that will be sent")

	_, err = BuildCreatePayRequest(CreatePayRequest{Gateway: GatewayTest, MerchantID: "m", TradeNo: "t", Money: "1.00", PayMethod: "alipay", NotifyURL: "https://example.com/notify"})
	assert.ErrorIs(t, err, ErrEmptyPrivateKey)
}

func TestParseCreatePayResponse(t *testing.T) {
	body := []byte(`{"ret_code":"0000","ret_msg":"交易成功","payload":"https://qr.alipay.com/bax1","mch_id":"2020042200284052","platform_txno":"2026010122001","txn_seqno":"USR1NOabc","order_amount":"0.01"}`)
	result, err := ParseCreatePayResponse(body)
	require.NoError(t, err)
	assert.Equal(t, "https://qr.alipay.com/bax1", result.QRCode)
	assert.Equal(t, "2020042200284052", result.MchID)
	assert.Equal(t, "2026010122001", result.TradeNo("fallback"))
	assert.Equal(t, "USR1NOabc", result.TxnSeqno)
	assert.Equal(t, "0.01", result.OrderAmount)

	nestedBody := []byte(`{"ret_code":"0000","payload":"{\"qr_code\":\"weixin://wxpay/1\",\"no_order\":\"n\"}"}`)
	result, err = ParseCreatePayResponse(nestedBody)
	require.NoError(t, err)
	assert.Equal(t, "weixin://wxpay/1", result.QRCode)
	assert.Equal(t, "n", result.TradeNo("n"))

	failed, err := ParseCreatePayResponse([]byte(`{"ret_code":"1001","ret_msg":"商户号不存在"}`))
	require.Error(t, err)
	require.NotNil(t, failed)
	assert.Equal(t, "1001", failed.RetCode)
	assert.ErrorContains(t, err, "ret_code=1001")

	_, err = ParseCreatePayResponse([]byte(`{"ret_code":"0000"}`))
	assert.ErrorContains(t, err, "payload 为空")

	_, err = ParseCreatePayResponse([]byte(`not-json`))
	assert.ErrorContains(t, err, "解析失败")
}

func TestParseNotifyPayloadAndStatus(t *testing.T) {
	payload, err := ParseNotifyPayload([]byte(`{"txn_seqno":"USR1NO2","order_amount":"10.00","result_pay":"SUCCESS","platform_txno":"2026010122001"}`))
	require.NoError(t, err)
	assert.Equal(t, "USR1NO2", payload.MerchantTradeNo())
	assert.Equal(t, "10.00", payload.PaidAmount())
	assert.Equal(t, "2026010122001", payload.ProviderTradeNo())
	assert.Equal(t, "SUCCESS", payload.PayStatusValue())
	assert.True(t, IsPaidStatus(payload.PayStatusValue()))

	alias, err := ParseNotifyPayload([]byte(`{"out_trade_no":"n2","money":"1.00","txn_status":"TRADE_SUCCESS","accp_txno":"accp-1"}`))
	require.NoError(t, err)
	assert.Equal(t, "n2", alias.MerchantTradeNo())
	assert.Equal(t, "1.00", alias.PaidAmount())
	assert.Equal(t, "accp-1", alias.ProviderTradeNo())
	assert.True(t, IsPaidStatus(alias.PayStatusValue()))

	_, err = ParseNotifyPayload([]byte(`{"order_amount":"1.00"}`))
	assert.ErrorContains(t, err, "缺少商户订单号")

	_, err = ParseNotifyPayload([]byte(`not-json`))
	assert.ErrorContains(t, err, "解析失败")

	assert.False(t, IsPaidStatus(""))
	assert.True(t, IsPaidStatus("ps"))
	assert.True(t, IsPaidStatus(" paid "))
	assert.False(t, IsPaidStatus("FAILURE"))
	assert.False(t, IsPaidStatus("TRADE_CLOSED"))
}

func TestMarshalBodyIsStableForSigning(t *testing.T) {
	params, err := BuildCreatePayBody(CreatePayRequest{
		MerchantID: "m",
		TradeNo:    "t",
		Money:      "1.00",
		PayMethod:  "alipay",
		NotifyURL:  "https://example.com/notify",
		Timestamp:  "20260101120000",
	})
	require.NoError(t, err)
	first, err := MarshalBody(params)
	require.NoError(t, err)
	second, err := MarshalBody(params)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second))

	var decoded map[string]any
	require.NoError(t, common.Unmarshal(first, &decoded))
	assert.Equal(t, "1.00", decoded["order_amount"])
}

func TestQueryGatewayURLSelection(t *testing.T) {
	testCases := []struct {
		name     string
		env      string
		override string
		want     string
		wantErr  bool
	}{
		{name: "test default", env: EnvTest, want: QueryGatewayTest + OrderQueryPath},
		{name: "prod default", env: EnvProd, want: QueryGatewayProd + OrderQueryPath},
		{name: "official test gateway", env: EnvTest, override: GatewayTest, want: QueryGatewayTest + OrderQueryPath},
		{name: "official prod gateway", env: EnvProd, override: GatewayProd, want: QueryGatewayProd + OrderQueryPath},
		{name: "derive from mch gateway", env: EnvProd, override: "https://sandbox.example.com/ll/mch", want: "https://sandbox.example.com/ll/query" + OrderQueryPath},
		{name: "accept query gateway", env: EnvProd, override: "https://sandbox.example.com/ll/query", want: "https://sandbox.example.com/ll/query" + OrderQueryPath},
		{name: "accept query api gateway", env: EnvTest, override: "https://sandbox.example.com/ll/query-api", want: "https://sandbox.example.com/ll/query-api" + OrderQueryPath},
		{name: "reject unknown gateway", env: EnvProd, override: "https://sandbox.example.com/ll", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := QueryGatewayURL(tc.env, tc.override)
			if tc.wantErr {
				require.Error(t, err)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBuildOrderQueryRequestSignsExactBody(t *testing.T) {
	signed, err := BuildOrderQueryRequest(OrderQueryRequest{
		Env:        EnvTest,
		PrivateKey: docSamplePrivKey,
		MerchantID: "2020042200284052",
		TradeNo:    "USR42NO123",
		Timestamp:  "20260101120000",
	})
	require.NoError(t, err)
	assert.Equal(t, QueryGatewayTest+OrderQueryPath, signed.URL)

	body := map[string]any{}
	require.NoError(t, common.Unmarshal(signed.Body, &body))
	assert.Equal(t, map[string]any{
		"mch_id":    "2020042200284052",
		"txn_seqno": "USR42NO123",
	}, body)
	ok, verifyErr := Verify(docSamplePubKey, signed.Body, signed.Signature)
	require.NoError(t, verifyErr)
	assert.True(t, ok)
}

func TestParseOrderQueryResponse(t *testing.T) {
	body := []byte(`{"ret_code":"0000","ret_msg":"成功","mch_id":"2020042200284052","pay_type":"WECHAT_NATIVE","txn_seqno":"USR42NO123","platform_txno":"LL123","order_amount":"10.00","txn_status":"SUCCESS","pay_method_infos":[{"pay_type":"WECHAT_NATIVE","amount":"10.00"}]}`)
	result, err := ParseOrderQueryResponse(body)
	require.NoError(t, err)
	assert.Equal(t, "2020042200284052", result.MchID)
	assert.Equal(t, "USR42NO123", result.TxnSeqno)
	assert.Equal(t, "10.00", result.OrderAmount.String())
	assert.Equal(t, "SUCCESS", result.TxnStatus)
	assert.Equal(t, PayTypeWechatNative, result.PayTypeValue())
	assert.Equal(t, PayMethodWxpay, PayMethodForPayType(result.PayTypeValue()))
}

func TestParseNotifyPayloadReadsMerchantMethodAndAmount(t *testing.T) {
	body := []byte(`{"mch_id":"2020042200284052","txn_seqno":"USR42NO123","txn_status":"SUCCESS","pay_method_infos":[{"pay_type":"ALIPAY_NATIVE","amount":"10.00"}]}`)
	payload, err := ParseNotifyPayload(body)
	require.NoError(t, err)
	assert.Equal(t, "2020042200284052", payload.MchID)
	assert.Equal(t, "USR42NO123", payload.MerchantTradeNo())
	assert.Equal(t, PayTypeAlipayNative, payload.PayTypeValue())
	assert.Equal(t, "10.00", payload.PaidAmount())
}

func TestParseAmountsAcceptsStringAndNumberForms(t *testing.T) {
	numeric, err := ParseNotifyPayload([]byte(`{"txn_seqno":"USR42NONUMERIC","txn_status":"SUCCESS","order_amount":10.01,"pay_method_infos":[{"pay_type":"ALIPAY_NATIVE","amount":10.01}]}`))
	require.NoError(t, err)
	assert.Equal(t, "10.01", numeric.PaidAmount())

	spaced, err := ParseNotifyPayload([]byte(`{"txn_seqno":"USR42NOSPACED","txn_status":"SUCCESS","order_amount ":10.02,"pay_method_infos":[{"pay_type":"ALIPAY_NATIVE","amount":"10.02"}]}`))
	require.NoError(t, err)
	assert.Equal(t, "10.02", spaced.PaidAmount())

	query, err := ParseOrderQueryResponse([]byte(`{"ret_code":"0000","mch_id":"2020042200284052","txn_seqno":"USR42NOQUERYNUM","order_amount":10.03,"txn_status":"SUCCESS","pay_method_infos":[{"pay_type":"WECHAT_NATIVE","amount":10.03}]}`))
	require.NoError(t, err)
	assert.Equal(t, "10.03", query.OrderAmount.String())
}
