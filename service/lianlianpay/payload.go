package lianlianpay

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

// CreatePayRequest 是构造收单创单报文所需的钱包侧数据。
type CreatePayRequest struct {
	// Env 决定默认网关（test/prod），Gateway 非空时优先使用 Gateway。
	Env          string
	Gateway      string
	PrivateKey   string
	MerchantID   string
	BusiType     string
	UserID       string
	Username     string
	UserEmail    string
	Registered   string
	RiskCategory string
	TradeNo      string
	Money        string
	GoodsName    string
	NotifyURL    string
	ClientIP     string
	DeviceID     string
	PayMethod    string
	Timestamp    string
}

// SignedRequest 是可以直接发送的创单请求：报文与签名一一对应，调用方不得
// 再重新序列化 Body。
type SignedRequest struct {
	URL        string
	Body       []byte
	Signature  string
	Timestamp  string
	MerchantID string
}

// CreatePayTradeNoLimit 是连连 txn_seqno 的最大长度。
const CreatePayTradeNoLimit = 32

// MaxOrderAmountCents is the largest amount accepted by the current Lianlian
// acquiring API (999999.99 CNY).
const MaxOrderAmountCents int64 = 99_999_999

// maxUserIDLength 是连连 user_id 的最大长度。
const maxUserIDLength = 64

// BuildCreatePayRequest 构造创单请求：报文、签名与时间戳。
func BuildCreatePayRequest(req CreatePayRequest) (*SignedRequest, error) {
	params, err := BuildCreatePayBody(req)
	if err != nil {
		return nil, err
	}
	body, err := MarshalBody(params)
	if err != nil {
		return nil, err
	}
	signature, err := Sign(req.PrivateKey, body)
	if err != nil {
		return nil, err
	}
	url, err := GatewayURL(req.Env, req.Gateway)
	if err != nil {
		return nil, err
	}
	return &SignedRequest{
		URL:        url,
		Body:       body,
		Signature:  signature,
		Timestamp:  stringValue(params["txn_time"]),
		MerchantID: strings.TrimSpace(req.MerchantID),
	}, nil
}

// OrderQueryRequest 是收款结果查询所需的钱包侧数据。
type OrderQueryRequest struct {
	Env        string
	Gateway    string
	PrivateKey string
	MerchantID string
	TradeNo    string
	Timestamp  string
}

// BuildOrderQueryRequest 构造收款查询请求：报文、签名与时间戳。
func BuildOrderQueryRequest(req OrderQueryRequest) (*SignedRequest, error) {
	merchantID := strings.TrimSpace(req.MerchantID)
	if merchantID == "" {
		return nil, errors.New("lianlian 商户号未配置")
	}
	tradeNo := strings.TrimSpace(req.TradeNo)
	if tradeNo == "" {
		return nil, errors.New("lianlian 商户订单号不能为空")
	}
	timestamp := strings.TrimSpace(req.Timestamp)
	if timestamp == "" {
		timestamp = NowTimestamp()
	}
	body, err := MarshalBody(map[string]any{
		"mch_id":    merchantID,
		"txn_seqno": tradeNo,
	})
	if err != nil {
		return nil, err
	}
	signature, err := Sign(req.PrivateKey, body)
	if err != nil {
		return nil, err
	}
	url, err := QueryGatewayURL(req.Env, req.Gateway)
	if err != nil {
		return nil, err
	}
	return &SignedRequest{
		URL:        url,
		Body:       body,
		Signature:  signature,
		Timestamp:  timestamp,
		MerchantID: merchantID,
	}, nil
}

// BuildCreatePayBody 构造收单创单报文（签名不在报文中，而在请求头）。
func BuildCreatePayBody(req CreatePayRequest) (map[string]any, error) {
	merchantID := strings.TrimSpace(req.MerchantID)
	if merchantID == "" {
		return nil, errors.New("lianlian 商户号未配置")
	}
	tradeNo := strings.TrimSpace(req.TradeNo)
	if tradeNo == "" {
		return nil, errors.New("lianlian 商户订单号不能为空")
	}
	if len(tradeNo) > CreatePayTradeNoLimit {
		return nil, fmt.Errorf("lianlian 商户订单号不能超过 %d 个字符", CreatePayTradeNoLimit)
	}
	money := strings.TrimSpace(req.Money)
	if money == "" {
		return nil, errors.New("lianlian 支付金额不能为空")
	}
	payMoney, err := decimal.NewFromString(money)
	if err != nil {
		return nil, errors.New("lianlian 支付金额格式错误")
	}
	payMoneyCents := payMoney.Shift(2)
	if !payMoneyCents.IsInteger() ||
		!payMoneyCents.IsPositive() ||
		payMoneyCents.GreaterThan(decimal.NewFromInt(MaxOrderAmountCents)) {
		return nil, errors.New("lianlian 支付金额必须在 0.01 至 999999.99 元之间")
	}
	money = payMoney.StringFixed(2)
	notifyURL := strings.TrimSpace(req.NotifyURL)
	if notifyURL == "" {
		return nil, errors.New("lianlian 支付回调地址未配置")
	}
	if len(notifyURL) > 255 {
		return nil, errors.New("lianlian 支付回调地址不能超过 255 个字符")
	}
	payType := PayTypeOf(req.PayMethod)
	if payType == "" {
		return nil, fmt.Errorf("lianlian 不支持的支付方式: %s", req.PayMethod)
	}
	timestamp := strings.TrimSpace(req.Timestamp)
	if timestamp == "" {
		timestamp = NowTimestamp()
	}
	goodsName := truncate(strings.TrimSpace(req.GoodsName), 255)
	if goodsName == "" {
		goodsName = "NewAPI Top-up"
	}
	busiType := strings.TrimSpace(req.BusiType)
	if busiType == "" {
		busiType = DefaultBusiType
	}
	userID := truncate(strings.TrimSpace(req.UserID), maxUserIDLength)
	if userID == "" {
		userID = tradeNo
	}
	deviceID := strings.TrimSpace(req.DeviceID)
	if deviceID == "" {
		deviceID = DefaultDeviceIDPrefix + userID
	}
	deviceID = truncate(deviceID, 64)
	deviceIP := strings.TrimPrefix(strings.TrimSpace(req.ClientIP), "::ffff:")
	if deviceIP == "" {
		deviceIP = "127.0.0.1"
	}

	riskCategory := strings.TrimSpace(req.RiskCategory)
	if riskCategory == "" {
		riskCategory = DefaultRiskCategory
	}

	return map[string]any{
		"mch_id":       merchantID,
		"user_id":      userID,
		"busi_type":    busiType,
		"txn_seqno":    tradeNo,
		"txn_time":     timestamp,
		"order_amount": money,
		"order_info":   truncate(goodsName, 255),
		"pay_expire":   PayExpireMinutes,
		"notify_url":   notifyURL,
		"risk_item": RiskItem(RiskItemParams{
			Category:     riskCategory,
			UserID:       userID,
			Username:     req.Username,
			Email:        req.UserEmail,
			ClientIP:     req.ClientIP,
			RegisteredAt: req.Registered,
		}),
		"pay_method_infos": []map[string]any{
			{"pay_type": payType, "amount": money},
		},
		"goods_info": []map[string]any{
			{
				"goods_id":       "API_CREDIT",
				"goods_name":     truncate(goodsName, 40),
				"goods_quantity": "1",
				"goods_price":    money,
			},
		},
		"device_info": map[string]any{
			"device_id":   deviceID,
			"device_type": "PC",
			"device_ip":   truncate(deviceIP, 32),
		},
	}, nil
}

// RiskItemParams contains the user attributes accepted by Lianlian risk control.
type RiskItemParams struct {
	Category     string
	UserID       string
	Username     string
	Email        string
	ClientIP     string
	RegisteredAt string
}

// RiskItem 生成连连风控参数（JSON 字符串）。字段与官方示例一致，多余字段会
// 触发渠道侧异常。
func RiskItem(params RiskItemParams) string {
	clientIP := strings.TrimPrefix(strings.TrimSpace(params.ClientIP), "::ffff:")
	category := strings.TrimSpace(params.Category)
	if category == "" {
		category = DefaultRiskCategory
	}
	risk := map[string]any{
		"frms_ware_category":         category,
		"user_info_mercht_userno":    strings.TrimSpace(params.UserID),
		"user_info_mercht_userlogin": strings.TrimSpace(params.Username),
		"user_info_mail":             strings.TrimSpace(params.Email),
		"user_info_bind_phone":       "",
		"user_info_mercht_usertype":  "",
		"user_info_dt_registe":       strings.TrimSpace(params.RegisteredAt),
		"user_info_register_ip":      clientIP,
		"user_info_full_name":        "",
		"user_info_id_type":          "0",
		"user_info_id_no":            "",
		"user_info_identify_state":   "0",
		"user_info_identify_type":    "4",
	}
	encoded, err := common.Marshal(risk)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

// CreatePayResult 是创单应答解析结果。
type CreatePayResult struct {
	RetCode      string
	RetMsg       string
	QRCode       string
	MchID        string
	PlatformTxNo string
	TxnSeqno     string
	OrderAmount  string
}

// TradeNo 返回可用于对账的连连交易号，缺省回落到商户订单号。
func (result *CreatePayResult) TradeNo(fallback string) string {
	for _, candidate := range []string{result.PlatformTxNo, result.TxnSeqno} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return fallback
}

type createPayEnvelope struct {
	RetCode      string          `json:"ret_code"`
	RetMsg       string          `json:"ret_msg"`
	Payload      json.RawMessage `json:"payload"`
	MchID        string          `json:"mch_id"`
	PlatformTxNo string          `json:"platform_txno"`
	TxnSeqno     string          `json:"txn_seqno"`
	OrderAmount  FlexibleAmount  `json:"order_amount"`
}

// ParseCreatePayResponse 解析创单应答。ret_code 非 0000 时返回错误（错误信息
// 保留连连的 ret_code/ret_msg 便于排障）。
func ParseCreatePayResponse(body []byte) (*CreatePayResult, error) {
	envelope := &createPayEnvelope{}
	if err := common.Unmarshal(body, envelope); err != nil {
		return nil, fmt.Errorf("lianlian 应答解析失败: %w", err)
	}
	result := &CreatePayResult{
		RetCode:      envelope.RetCode,
		RetMsg:       envelope.RetMsg,
		MchID:        envelope.MchID,
		PlatformTxNo: envelope.PlatformTxNo,
		TxnSeqno:     envelope.TxnSeqno,
		OrderAmount:  envelope.OrderAmount.String(),
	}
	if envelope.RetCode != RetCodeSuccess {
		return result, fmt.Errorf("lianlian 创单失败 ret_code=%s ret_msg=%s", envelope.RetCode, envelope.RetMsg)
	}
	qrcode, err := extractQRCode(envelope.Payload)
	if err != nil {
		return result, err
	}
	result.QRCode = qrcode
	return result, nil
}

// OrderQueryResult 是收款查询应答的业务字段。
type OrderQueryResult struct {
	RetCode        string          `json:"ret_code"`
	RetMsg         string          `json:"ret_msg"`
	MchID          string          `json:"mch_id"`
	PayType        string          `json:"pay_type"`
	TxnSeqno       string          `json:"txn_seqno"`
	PlatformTxNo   string          `json:"platform_txno"`
	OrderAmount    FlexibleAmount  `json:"order_amount"`
	TxnStatus      string          `json:"txn_status"`
	PayMethodInfos []PayMethodInfo `json:"pay_method_infos"`
}

// PayTypeValue 返回查询结果中的实际支付方式类型。
func (result *OrderQueryResult) PayTypeValue() string {
	for _, method := range result.PayMethodInfos {
		if trimmed := strings.TrimSpace(method.PayType); trimmed != "" {
			return trimmed
		}
	}
	return strings.TrimSpace(result.PayType)
}

// ParseOrderQueryResponse 解析收款查询应答。ret_code 非 0000 时返回错误，
// 但保留应答内容供调用方按业务状态处理。
func ParseOrderQueryResponse(body []byte) (*OrderQueryResult, error) {
	result := &OrderQueryResult{}
	if err := common.Unmarshal(body, result); err != nil {
		return nil, fmt.Errorf("lianlian 查询应答解析失败: %w", err)
	}
	if result.RetCode != RetCodeSuccess {
		return result, fmt.Errorf("lianlian 查询失败 ret_code=%s ret_msg=%s", result.RetCode, result.RetMsg)
	}
	return result, nil
}

// extractQRCode 取出扫码支付串：payload 为字符串时直接使用；个别版本把业务
// 报文作为 JSON 字符串返回，此时按 qr_code/code_url/pay_url 依次取用。
func extractQRCode(payload json.RawMessage) (string, error) {
	raw := strings.TrimSpace(string(payload))
	if raw == "" || raw == "null" {
		return "", errors.New("lianlian 创单成功但 payload 为空")
	}
	if !strings.HasPrefix(raw, "\"") {
		return "", errors.New("lianlian 创单应答 payload 不是字符串")
	}
	var content string
	if err := common.Unmarshal(payload, &content); err != nil {
		return "", fmt.Errorf("lianlian payload 解析失败: %w", err)
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "", errors.New("lianlian 创单成功但 payload 为空")
	}
	if !strings.HasPrefix(content, "{") {
		return content, nil
	}
	nested := map[string]any{}
	if err := common.Unmarshal([]byte(content), &nested); err != nil {
		return content, nil
	}
	for _, key := range []string{"qr_code", "code_url", "pay_url", "payload"} {
		if candidate, ok := nested[key].(string); ok && strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate), nil
		}
	}
	return content, nil
}

// FlexibleAmount accepts the string amounts used by most Lianlian examples and
// the numeric amounts declared by some versions of the callback schema.
// Conversion to decimal remains at the settlement boundary.
type FlexibleAmount string

func (amount *FlexibleAmount) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*amount = ""
		return nil
	}
	if raw[0] == '"' {
		var value string
		if err := common.Unmarshal(data, &value); err != nil {
			return err
		}
		*amount = FlexibleAmount(strings.TrimSpace(value))
		return nil
	}

	var value json.Number
	if err := common.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("invalid amount: %w", err)
	}
	*amount = FlexibleAmount(value.String())
	return nil
}

func (amount FlexibleAmount) String() string {
	return string(amount)
}

// PayMethodInfo 是连连支付方式与金额。
type PayMethodInfo struct {
	PayType string         `json:"pay_type"`
	Amount  FlexibleAmount `json:"amount"`
}

// NotifyPayload 是连连异步通知的业务字段。字段名按连连收单通知定义，兼容
// 文档中并列的多个别名字段。
type NotifyPayload struct {
	MchID            string          `json:"mch_id"`
	TxnSeqno         string          `json:"txn_seqno"`
	OutTradeNo       string          `json:"out_trade_no"`
	OrderAmount      FlexibleAmount  `json:"order_amount"`
	OrderAmountSpace FlexibleAmount  `json:"order_amount "`
	Money            FlexibleAmount  `json:"money"`
	ResultPay        string          `json:"result_pay"`
	TxnStatus        string          `json:"txn_status"`
	PayStatus        string          `json:"pay_status"`
	PayType          string          `json:"pay_type"`
	PayMethod        string          `json:"pay_method"`
	PayMethodInfos   []PayMethodInfo `json:"pay_method_infos"`
	PlatformTxNo     string          `json:"platform_txno"`
	AccpTxno         string          `json:"accp_txno"`
}

// MerchantTradeNo 返回商户订单号。
func (payload *NotifyPayload) MerchantTradeNo() string {
	for _, candidate := range []string{payload.TxnSeqno, payload.OutTradeNo} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// PaidAmount 返回通知中的实付金额。
func (payload *NotifyPayload) PaidAmount() string {
	for _, candidate := range []FlexibleAmount{
		payload.OrderAmount,
		payload.OrderAmountSpace,
		payload.Money,
	} {
		if trimmed := strings.TrimSpace(candidate.String()); trimmed != "" {
			return trimmed
		}
	}
	for _, method := range payload.PayMethodInfos {
		if trimmed := strings.TrimSpace(method.Amount.String()); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// PayTypeValue 返回通知中的实际支付方式类型。
func (payload *NotifyPayload) PayTypeValue() string {
	for _, method := range payload.PayMethodInfos {
		if trimmed := strings.TrimSpace(method.PayType); trimmed != "" {
			return trimmed
		}
	}
	for _, candidate := range []string{payload.PayType, payload.PayMethod} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// ProviderTradeNo 返回渠道交易号（支付宝/微信侧），缺省回落到连连流水号。
func (payload *NotifyPayload) ProviderTradeNo() string {
	for _, candidate := range []string{payload.PlatformTxNo, payload.AccpTxno} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// PayStatus 返回通知中的支付状态字段。
func (payload *NotifyPayload) PayStatusValue() string {
	for _, candidate := range []string{payload.ResultPay, payload.TxnStatus, payload.PayStatus} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// IsPaidStatus 判断通知状态是否为支付成功。空状态按成功处理，与连连文档中
// 「未返回状态即已支付」的通知语义一致。
func IsPaidStatus(status string) bool {
	trimmed := strings.ToUpper(strings.TrimSpace(status))
	if trimmed == "" {
		return false
	}
	switch trimmed {
	case "SUCCESS", "TRADE_SUCCESS", "PS", "PAID":
		return true
	default:
		return false
	}
}

// ParseNotifyPayload 解析异步通知原文。
func ParseNotifyPayload(body []byte) (*NotifyPayload, error) {
	payload := &NotifyPayload{}
	if err := common.Unmarshal(body, payload); err != nil {
		return nil, fmt.Errorf("lianlian 通知报文解析失败: %w", err)
	}
	if payload.MerchantTradeNo() == "" {
		return nil, errors.New("lianlian 通知缺少商户订单号")
	}
	return payload, nil
}
