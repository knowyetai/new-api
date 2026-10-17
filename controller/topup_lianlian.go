package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/lianlianpay"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// lianlianOrderTTLSeconds 是扫码订单的有效期（秒），超时后前端停止轮询。
const lianlianOrderTTLSeconds int64 = lianlianpay.PayExpireMinutes * 60

// lianlianResponseBodyLimit 限制读取连连应答的大小，避免异常应答拖垮进程。
const lianlianResponseBodyLimit = 64 * 1024

const lianlianMinAmountCents int64 = 1

const lianlianIdempotencyKeyHeader = "Idempotency-Key"

type LianlianPayRequest struct {
	// Amount is the legacy CNY-cent field. New clients should use AmountCents.
	Amount *int64 `json:"amount"`
	// AmountCents is the canonical recharge amount in CNY cents.
	AmountCents   *int64 `json:"amount_cents"`
	PaymentMethod string `json:"payment_method"`
	// RequestID is accepted for clients that cannot set the idempotency header.
	RequestID string `json:"request_id"`
}

// lianlianHTTPClient 是调用连连下单接口的客户端，超时时间与连连建议一致。
var lianlianHTTPClient = &http.Client{Timeout: lianlianpay.TimeoutSeconds * time.Second}

func rejectInvalidLianlianTopUpQuota(c *gin.Context, userId int, amountCents int64) bool {
	topUp := &model.TopUp{
		Amount:          amountCents,
		AmountUnit:      model.AmountUnitCNYCent,
		PaymentProvider: model.PaymentProviderLianlian,
	}
	quota, err := topUp.QuotaAmount()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return true
	}
	return rejectInvalidCreditedQuota(c, userId, quota)
}

func calculateLianlianPayMoneyCents(amountCents int64, group string) (int64, error) {
	amountCNY := decimal.NewFromInt(amountCents).Shift(-2)
	topupGroupRatio := common.GetTopupGroupRatio(group)
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}
	discount := 1.0
	if amountCNY.IsInteger() {
		if configured, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(amountCNY.IntPart())]; ok && configured > 0 {
			discount = configured
		}
	}
	payMoneyCNY := amountCNY.
		Mul(decimal.NewFromFloat(topupGroupRatio)).
		Mul(decimal.NewFromFloat(discount)).
		Round(2)
	if payMoneyCNY.LessThan(decimal.NewFromFloat(0.01)) {
		return 0, errors.New("支付金额过低")
	}
	payMoneyCents := payMoneyCNY.Shift(2)
	if !payMoneyCents.IsInteger() ||
		payMoneyCents.GreaterThan(decimal.NewFromInt(lianlianpay.MaxOrderAmountCents)) {
		return 0, errors.New("支付金额超出范围")
	}
	return payMoneyCents.IntPart(), nil
}

func isValidLianlianRequestID(requestID string) bool {
	if requestID == "" || len(requestID) > 64 {
		return false
	}
	for _, char := range requestID {
		if char <= ' ' || char >= 0x7f {
			return false
		}
	}
	return true
}

func resolveLianlianAmountCents(req LianlianPayRequest) (int64, error) {
	if req.Amount == nil {
		if req.AmountCents == nil {
			return 0, errors.New("缺少充值金额")
		}
		return *req.AmountCents, nil
	}
	if req.AmountCents != nil && *req.AmountCents != *req.Amount {
		return 0, errors.New("amount 与 amount_cents 不一致")
	}
	return *req.Amount, nil
}

// RequestLianlianAmount 返回本次充值需要支付的金额，单位为 CNY_CENT。
func RequestLianlianAmount(c *gin.Context) {
	if !isLianlianTopUpEnabled() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值服务未启用"})
		return
	}
	var req LianlianPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	req.PaymentMethod = strings.TrimSpace(req.PaymentMethod)
	if !lianlianpay.SupportsPayMethod(req.PaymentMethod) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付方式不存在"})
		return
	}
	amountCents, err := resolveLianlianAmountCents(req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return
	}
	if amountCents < lianlianMinAmountCents {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额不能小于 0.01 元"})
		return
	}

	id := c.GetInt("id")
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	if rejectInvalidLianlianTopUpQuota(c, id, amountCents) {
		return
	}
	moneyCents, err := calculateLianlianPayMoneyCents(amountCents, group)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"amount_cents": moneyCents,
			"amount_unit":  model.AmountUnitCNYCent,
		},
	})
}

// RequestLianlianPay 创建本地订单并向连连下单，返回扫码串供前端渲染二维码。
func RequestLianlianPay(c *gin.Context) {
	if !isLianlianTopUpEnabled() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值服务未启用"})
		return
	}
	var req LianlianPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	req.PaymentMethod = strings.TrimSpace(req.PaymentMethod)
	if !lianlianpay.SupportsPayMethod(req.PaymentMethod) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付方式不存在"})
		return
	}
	amountCents, err := resolveLianlianAmountCents(req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return
	}
	headerRequestID := strings.TrimSpace(c.GetHeader(lianlianIdempotencyKeyHeader))
	bodyRequestID := strings.TrimSpace(req.RequestID)
	if headerRequestID != "" && bodyRequestID != "" && headerRequestID != bodyRequestID {
		c.JSON(http.StatusOK, gin.H{
			"message": "error",
			"code":    "LIANLIAN_IDEMPOTENCY_KEY_MISMATCH",
			"data":    "幂等键请求头与 request_id 不一致",
		})
		return
	}
	req.RequestID = headerRequestID
	if req.RequestID == "" {
		req.RequestID = bodyRequestID
	}
	if !isValidLianlianRequestID(req.RequestID) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "code": "LIANLIAN_REQUEST_ID_INVALID", "data": "Idempotency-Key 或 request_id 必须是 1 到 64 个可见 ASCII 字符"})
		return
	}
	if amountCents < lianlianMinAmountCents {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额不能小于 0.01 元"})
		return
	}
	// 网关地址是管理员配置项：先校验再建单，避免留下注定失败的订单，
	// 具体原因只写入服务端日志，前端统一返回中性提示。
	if _, err := lianlianpay.GatewayURL(setting.LianlianEnv, setting.LianlianGateway); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("连连 网关地址配置错误 user_id=%d gateway=%q error=%q", c.GetInt("id"), setting.LianlianGateway, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付服务暂时不可用"})
		return
	}

	id := c.GetInt("id")
	existing, err := model.GetLianlianTopUpByRequestID(id, req.RequestID)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("连连 查询幂等订单失败 user_id=%d request_id=%s error=%q", id, req.RequestID, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	if replayLianlianOrder(c, existing, amountCents, req.PaymentMethod) {
		return
	}

	user, err := model.GetUserById(id, false)
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "用户不存在"})
		return
	}
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}

	if rejectInvalidLianlianTopUpQuota(c, id, amountCents) {
		return
	}
	moneyCents, err := calculateLianlianPayMoneyCents(amountCents, group)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return
	}
	moneyCNY := decimal.NewFromInt(moneyCents).Shift(-2)
	callbackBase := strings.TrimRight(strings.TrimSpace(service.GetCallbackAddress()), "/")
	notifyURL := callbackBase + "/api/user/lianlian/notify"
	parsedNotifyURL, err := url.Parse(notifyURL)
	if err != nil || parsedNotifyURL.Scheme != "https" || parsedNotifyURL.Host == "" {
		logger.LogError(c.Request.Context(), fmt.Sprintf("连连 回调地址配置错误 user_id=%d notify_url=%q", id, notifyURL))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付服务暂时不可用"})
		return
	}

	tradeNo := fmt.Sprintf("USR%dNO%s%d", id, common.GetRandomString(6), time.Now().Unix())
	if len(tradeNo) > lianlianpay.CreatePayTradeNoLimit {
		tradeNo = tradeNo[:lianlianpay.CreatePayTradeNoLimit]
	}

	topUp := &model.TopUp{
		UserId:            id,
		Amount:            amountCents,
		AmountUnit:        model.AmountUnitCNYCent,
		Money:             float64(moneyCents),
		LianlianRequestID: &req.RequestID,
		TradeNo:           tradeNo,
		PaymentMethod:     req.PaymentMethod,
		PaymentProvider:   model.PaymentProviderLianlian,
		CreateTime:        time.Now().Unix(),
		Status:            common.TopUpStatusPending,
	}
	if err := topUp.Insert(); err != nil {
		existing, lookupErr := model.GetLianlianTopUpByRequestID(id, req.RequestID)
		if lookupErr == nil && replayLianlianOrder(c, existing, amountCents, req.PaymentMethod) {
			return
		}
		logger.LogError(c.Request.Context(), fmt.Sprintf("连连 创建充值订单失败 user_id=%d trade_no=%s amount_cents=%d error=%q", id, tradeNo, amountCents, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}

	signed, err := lianlianpay.BuildCreatePayRequest(lianlianpay.CreatePayRequest{
		Env:        setting.LianlianEnv,
		Gateway:    setting.LianlianGateway,
		PrivateKey: setting.LianlianPrivateKey,
		MerchantID: setting.LianlianMchId,
		BusiType:   setting.LianlianBusiType,
		UserID:     strconv.Itoa(id),
		TradeNo:    tradeNo,
		Money:      moneyCNY.StringFixed(2),
		GoodsName:  fmt.Sprintf("NewAPI 充值 %s", moneyCNY.StringFixed(2)),
		NotifyURL:  notifyURL,
		ClientIP:   c.ClientIP(),
		PayMethod:  req.PaymentMethod,
		Timestamp:  lianlianpay.NowTimestamp(),
	})
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("连连 构造下单报文失败 user_id=%d trade_no=%s error=%q", id, tradeNo, err.Error()))
		markTopUpFailed(topUp)
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}

	result, err := createLianlianPayment(c, signed, tradeNo, moneyCents)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("连连 拉起支付失败 user_id=%d trade_no=%s payment_method=%s amount_cents=%d error=%q", id, tradeNo, req.PaymentMethod, amountCents, err.Error()))
		markTopUpFailed(topUp)
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}

	providerTradeNo := strings.TrimSpace(result.PlatformTxNo)
	if len(providerTradeNo) > 128 {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("连连 上游订单号超长已忽略 trade_no=%s provider_trade_no=%q", tradeNo, providerTradeNo))
		providerTradeNo = ""
	}
	result.QRCode = strings.TrimSpace(result.QRCode)
	if err := model.UpdateLianlianPaymentResult(tradeNo, providerTradeNo, result.QRCode); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("连连 保存可重试支付信息失败 trade_no=%s provider_trade_no=%s error=%q", tradeNo, providerTradeNo, err.Error()))
	}
	topUp.ProviderTradeNo = providerTradeNo
	topUp.LianlianQRCode = result.QRCode

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("连连 充值订单创建成功 user_id=%d trade_no=%s request_id=%s provider_trade_no=%s payment_method=%s amount_cents=%d payable_amount_cents=%d", id, tradeNo, req.RequestID, providerTradeNo, req.PaymentMethod, topUp.Amount, moneyCents))
	writeLianlianOrderResponse(c, topUp)
}

// replayLianlianOrder returns the original order for an idempotency key. A
// reused key with different order parameters is rejected instead of silently
// returning an order for another amount or payment method.
func replayLianlianOrder(c *gin.Context, topUp *model.TopUp, amountCents int64, paymentMethod string) bool {
	if topUp == nil {
		return false
	}
	if topUp.PaymentProvider != model.PaymentProviderLianlian ||
		topUp.Amount != amountCents ||
		topUp.EffectiveAmountUnit() != model.AmountUnitCNYCent ||
		topUp.PaymentMethod != paymentMethod {
		c.JSON(http.StatusOK, gin.H{
			"message": "error",
			"code":    "LIANLIAN_IDEMPOTENCY_CONFLICT",
			"data":    "request_id 已用于不同的充值订单",
		})
		return true
	}
	topUp, _ = expireLianlianTopUpIfNeeded(c.Request.Context(), topUp, common.GetTimestamp())
	if topUp.Status == common.TopUpStatusPending && topUp.LianlianQRCode == "" {
		c.JSON(http.StatusOK, gin.H{
			"message": "error",
			"code":    "LIANLIAN_ORDER_CREATING",
			"data":    "订单正在创建中，请稍后重试",
		})
		return true
	}
	writeLianlianOrderResponse(c, topUp)
	return true
}

func writeLianlianOrderResponse(c *gin.Context, topUp *model.TopUp) {
	now := common.GetTimestamp()
	canPay := topUp.Status == common.TopUpStatusPending &&
		topUp.CreateTime+lianlianOrderTTLSeconds > now
	qrCode := ""
	if canPay {
		qrCode = topUp.LianlianQRCode
	}
	requestID := ""
	if topUp.LianlianRequestID != nil {
		requestID = *topUp.LianlianRequestID
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"trade_no":               topUp.TradeNo,
			"request_id":             requestID,
			"provider_trade_no":      topUp.ProviderTradeNo,
			"status":                 topUp.Status,
			"can_pay":                canPay,
			"qr_code":                qrCode,
			"expires_at":             topUp.CreateTime + lianlianOrderTTLSeconds,
			"requested_amount_cents": topUp.Amount,
			"requested_amount_unit":  topUp.EffectiveAmountUnit(),
			"payable_amount_cents":   int64(topUp.Money),
			"payable_amount_unit":    model.AmountUnitCNYCent,
			// Retained for clients developed against the first revision.
			"money":       topUp.Money,
			"amount":      topUp.Amount,
			"amount_unit": topUp.EffectiveAmountUnit(),
			"create_time": topUp.CreateTime,
		},
	})
}

// createLianlianPayment 发送下单请求并解析应答。
func createLianlianPayment(c *gin.Context, signed *lianlianpay.SignedRequest, expectedTradeNo string, expectedMoneyCents int64) (*lianlianpay.CreatePayResult, error) {
	body, err := postSignedLianlianRequest(c.Request.Context(), signed, setting.GetLianlianPlatformPublicKey())
	if err != nil {
		return nil, err
	}
	result, err := lianlianpay.ParseCreatePayResponse(body)
	if err != nil {
		return result, err
	}
	responseAmount, amountErr := decimal.NewFromString(strings.TrimSpace(result.OrderAmount))
	if strings.TrimSpace(result.MchID) != strings.TrimSpace(setting.LianlianMchId) ||
		strings.TrimSpace(result.TxnSeqno) != expectedTradeNo ||
		strings.TrimSpace(result.PlatformTxNo) == "" ||
		amountErr != nil ||
		!responseAmount.Shift(2).Equal(decimal.NewFromInt(expectedMoneyCents)) {
		return result, errors.New("连连接口应答与本地订单不一致")
	}
	return result, nil
}

func postSignedLianlianRequest(ctx context.Context, signed *lianlianpay.SignedRequest, publicKey string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, signed.URL, bytes.NewReader(signed.Body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", lianlianpay.HeaderContentTypeValue)
	request.Header.Set(lianlianpay.HeaderMchID, signed.MerchantID)
	request.Header.Set(lianlianpay.HeaderSignatureType, lianlianpay.SignTypeRSA)
	request.Header.Set(lianlianpay.HeaderSignatureData, signed.Signature)
	request.Header.Set(lianlianpay.HeaderTimestamp, signed.Timestamp)

	response, err := lianlianHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = response.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(response.Body, lianlianResponseBodyLimit))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("连连接口返回 HTTP %d: %s", response.StatusCode, common.LocalLogPreview(string(body)))
	}
	signature := response.Header.Get(lianlianpay.HeaderSignatureData)
	verified, err := lianlianpay.Verify(publicKey, body, signature)
	if err != nil {
		return nil, fmt.Errorf("连连响应验签失败: %w", err)
	}
	if !verified {
		return nil, errors.New("连连响应签名无效")
	}
	return body, nil
}

// markTopUpFailed 把下单失败的订单标记为失败，避免留下永远 pending 的订单。
func markTopUpFailed(topUp *model.TopUp) {
	if topUp == nil {
		return
	}
	err := model.UpdatePendingTopUpStatus(
		topUp.TradeNo,
		model.PaymentProviderLianlian,
		common.TopUpStatusFailed,
	)
	if errors.Is(err, model.ErrTopUpStatusInvalid) {
		return
	}
	if err != nil {
		common.SysError("failed to mark lianlian topup as failed: " + err.Error())
	}
}

func getOwnedLianlianTopUp(c *gin.Context) (*model.TopUp, bool) {
	tradeNo := c.Param("tradeNo")
	if tradeNo == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return nil, false
	}
	topUp := model.GetTopUpByTradeNo(tradeNo)
	if topUp == nil || topUp.PaymentProvider != model.PaymentProviderLianlian {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "订单不存在"})
		return nil, false
	}
	userId := c.GetInt("id")
	if topUp.UserId != userId {
		// 不暴露他人订单的存在性细节，统一按订单不存在处理。
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("连连 订单查询越权拒绝 user_id=%d trade_no=%s client_ip=%s", userId, tradeNo, c.ClientIP()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "订单不存在"})
		return nil, false
	}
	return topUp, true
}

func expireLianlianTopUpIfNeeded(ctx context.Context, topUp *model.TopUp, now int64) (*model.TopUp, bool) {
	if topUp == nil ||
		(topUp.Status != common.TopUpStatusPending && topUp.Status != common.TopUpStatusCancelled) ||
		topUp.CreateTime <= 0 ||
		topUp.CreateTime+lianlianOrderTTLSeconds > now {
		return topUp, false
	}
	updated, err := model.TransitionLianlianTopUpStatus(topUp.TradeNo, common.TopUpStatusExpired)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("连连 订单过期落库失败 trade_no=%s error=%q", topUp.TradeNo, err.Error()))
		return topUp, false
	}
	if !updated {
		current := model.GetTopUpByTradeNo(topUp.TradeNo)
		if current != nil {
			return current, current.Status == common.TopUpStatusExpired
		}
		return topUp, false
	}
	topUp.Status = common.TopUpStatusExpired
	return topUp, true
}

// GetLianlianOrder 返回当前用户的订单状态，供前端在展示或刷新二维码前校验。
func GetLianlianOrder(c *gin.Context) {
	topUp, ok := getOwnedLianlianTopUp(c)
	if !ok {
		return
	}
	now := common.GetTimestamp()
	topUp, _ = expireLianlianTopUpIfNeeded(c.Request.Context(), topUp, now)
	canPay := topUp.Status == common.TopUpStatusPending &&
		topUp.CreateTime+lianlianOrderTTLSeconds > now
	paymentHint := ""
	switch {
	case canPay:
	case topUp.Status == common.TopUpStatusCancelled:
		paymentHint = "订单已取消，请重新创建订单"
	case topUp.Status == common.TopUpStatusFailed:
		paymentHint = "订单创建失败，请重新创建订单"
	case topUp.Status == common.TopUpStatusExpired:
		paymentHint = "订单已过期，请重新创建订单"
	case topUp.Status == common.TopUpStatusSuccess:
		paymentHint = "订单已支付"
	default:
		paymentHint = "订单已过期，请重新创建订单"
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"trade_no":               topUp.TradeNo,
			"request_id":             topUp.LianlianRequestID,
			"provider_trade_no":      topUp.ProviderTradeNo,
			"status":                 topUp.Status,
			"can_pay":                canPay,
			"payment_hint":           paymentHint,
			"requested_amount_cents": topUp.Amount,
			"requested_amount_unit":  topUp.EffectiveAmountUnit(),
			"payable_amount_cents":   int64(topUp.Money),
			"payable_amount_unit":    model.AmountUnitCNYCent,
			"qr_code": func() string {
				if canPay {
					return topUp.LianlianQRCode
				}
				return ""
			}(),
			"money":       topUp.Money,
			"amount":      topUp.Amount,
			"amount_unit": topUp.EffectiveAmountUnit(),
			"create_time": topUp.CreateTime,
		},
	})
}

// CancelLianlianOrder abandons a pending order locally. A provider-confirmed
// payment can still settle the order after cancellation.
func CancelLianlianOrder(c *gin.Context) {
	topUp, ok := getOwnedLianlianTopUp(c)
	if !ok {
		return
	}
	topUp, expired := expireLianlianTopUpIfNeeded(c.Request.Context(), topUp, common.GetTimestamp())
	if expired || topUp.Status == common.TopUpStatusExpired {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "订单已过期，请重新创建订单"})
		return
	}
	if topUp.Status == common.TopUpStatusCancelled {
		c.JSON(http.StatusOK, gin.H{
			"message": "success",
			"data": gin.H{
				"trade_no": topUp.TradeNo,
				"status":   topUp.Status,
			},
		})
		return
	}
	if topUp.Status != common.TopUpStatusPending {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "当前订单状态不可取消"})
		return
	}
	err := model.UpdatePendingTopUpStatus(
		topUp.TradeNo,
		model.PaymentProviderLianlian,
		common.TopUpStatusCancelled,
	)
	if err != nil {
		current := model.GetTopUpByTradeNo(topUp.TradeNo)
		if current != nil && current.Status == common.TopUpStatusCancelled {
			c.JSON(http.StatusOK, gin.H{
				"message": "success",
				"data": gin.H{
					"trade_no": current.TradeNo,
					"status":   current.Status,
				},
			})
			return
		}
		if errors.Is(err, model.ErrTopUpStatusInvalid) {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "当前订单状态不可取消"})
			return
		}
		logger.LogError(c.Request.Context(), fmt.Sprintf("连连 取消订单失败 trade_no=%s error=%q", topUp.TradeNo, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "取消订单失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"trade_no": topUp.TradeNo,
			"status":   common.TopUpStatusCancelled,
		},
	})
}

// LianlianNotify 处理连连异步支付通知：验签 → 校验订单与金额 → 入账 → 返回 Success。
func LianlianNotify(c *gin.Context) {
	ctx := c.Request.Context()
	if !isLianlianWebhookEnabled() {
		logger.LogWarn(ctx, fmt.Sprintf("连连 webhook 被拒绝 reason=webhook_disabled path=%q client_ip=%s", c.Request.URL.Path, c.ClientIP()))
		writeLianlianNotifyResponse(c, lianlianpay.FailResponse)
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("连连 webhook 读取请求体失败 path=%q client_ip=%s error=%q", c.Request.URL.Path, c.ClientIP(), err.Error()))
		writeLianlianNotifyResponse(c, lianlianpay.FailResponse)
		return
	}
	if len(body) == 0 {
		logger.LogWarn(ctx, fmt.Sprintf("连连 webhook 请求体为空 path=%q client_ip=%s", c.Request.URL.Path, c.ClientIP()))
		writeLianlianNotifyResponse(c, lianlianpay.FailResponse)
		return
	}

	signature := c.GetHeader(lianlianpay.HeaderSignatureData)
	verified, verifyErr := lianlianpay.Verify(setting.GetLianlianPlatformPublicKey(), body, signature)
	if verifyErr != nil {
		logger.LogError(ctx, fmt.Sprintf("连连 webhook 验签异常 path=%q client_ip=%s error=%q", c.Request.URL.Path, c.ClientIP(), verifyErr.Error()))
		writeLianlianNotifyResponse(c, lianlianpay.FailResponse)
		return
	}
	if !verified {
		logger.LogWarn(ctx, fmt.Sprintf("连连 webhook 验签失败 path=%q client_ip=%s", c.Request.URL.Path, c.ClientIP()))
		writeLianlianNotifyResponse(c, lianlianpay.FailResponse)
		return
	}

	notify, err := lianlianpay.ParseNotifyPayload(body)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("连连 webhook 报文解析失败 path=%q client_ip=%s error=%q", c.Request.URL.Path, c.ClientIP(), err.Error()))
		writeLianlianNotifyResponse(c, lianlianpay.FailResponse)
		return
	}
	if strings.TrimSpace(notify.MchID) != strings.TrimSpace(setting.LianlianMchId) {
		logger.LogWarn(ctx, fmt.Sprintf("连连 webhook 商户号不匹配 mch_id=%s client_ip=%s", notify.MchID, c.ClientIP()))
		writeLianlianNotifyResponse(c, lianlianpay.FailResponse)
		return
	}
	actualPaymentMethod := lianlianpay.PayMethodForPayType(notify.PayTypeValue())
	if actualPaymentMethod == "" {
		logger.LogWarn(ctx, fmt.Sprintf("连连 webhook 支付方式无效 pay_type=%s trade_no=%s client_ip=%s", notify.PayTypeValue(), notify.MerchantTradeNo(), c.ClientIP()))
		writeLianlianNotifyResponse(c, lianlianpay.FailResponse)
		return
	}
	tradeNo := notify.MerchantTradeNo()
	logger.LogInfo(ctx, fmt.Sprintf("连连 webhook 验签成功 trade_no=%s provider_trade_no=%s result=%s client_ip=%s", tradeNo, notify.ProviderTradeNo(), notify.PayStatusValue(), c.ClientIP()))

	if !lianlianpay.IsPaidStatus(notify.PayStatusValue()) {
		logger.LogWarn(ctx, fmt.Sprintf("连连 webhook 拒绝非成功状态 trade_no=%s result=%s client_ip=%s", tradeNo, notify.PayStatusValue(), c.ClientIP()))
		writeLianlianNotifyResponse(c, lianlianpay.FailResponse)
		return
	}

	// 进程内锁只是优化；重复/并发回调的正确性由 RechargeLianlian 的
	// 数据库行锁 + 事务内状态校验保证（多实例部署下同样安全）。
	LockOrder(tradeNo)
	defer UnlockOrder(tradeNo)
	alreadyDone, err := model.RechargeLianlian(
		tradeNo,
		actualPaymentMethod,
		c.ClientIP(),
		notify.PaidAmount(),
		notify.ProviderTradeNo(),
	)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrTopUpNotFound):
			logger.LogWarn(ctx, fmt.Sprintf("连连 回调订单不存在 trade_no=%s client_ip=%s", tradeNo, c.ClientIP()))
		case errors.Is(err, model.ErrPaymentMethodMismatch):
			logger.LogWarn(ctx, fmt.Sprintf("连连 订单支付方式不匹配 trade_no=%s client_ip=%s", tradeNo, c.ClientIP()))
		case errors.Is(err, model.ErrTopUpStatusInvalid):
			logger.LogWarn(ctx, fmt.Sprintf("连连 订单状态非法 trade_no=%s client_ip=%s", tradeNo, c.ClientIP()))
		case errors.Is(err, model.ErrLianlianAmountMismatch):
			logger.LogError(ctx, fmt.Sprintf("连连 回调金额与订单不一致 trade_no=%s paid=%s client_ip=%s", tradeNo, notify.PaidAmount(), c.ClientIP()))
		case errors.Is(err, model.ErrLianlianProviderTradeNoMismatch):
			logger.LogWarn(ctx, fmt.Sprintf("连连 上游订单号不一致 trade_no=%s provider_trade_no=%s client_ip=%s", tradeNo, notify.ProviderTradeNo(), c.ClientIP()))
		default:
			logger.LogError(ctx, fmt.Sprintf("连连 充值处理失败 trade_no=%s client_ip=%s error=%q", tradeNo, c.ClientIP(), err.Error()))
		}
		writeLianlianNotifyResponse(c, lianlianpay.FailResponse)
		return
	}
	if alreadyDone {
		logger.LogInfo(ctx, fmt.Sprintf("连连 重复回调幂等忽略 trade_no=%s client_ip=%s", tradeNo, c.ClientIP()))
	} else {
		logger.LogInfo(ctx, fmt.Sprintf("连连 充值成功 trade_no=%s client_ip=%s", tradeNo, c.ClientIP()))
	}
	writeLianlianNotifyResponse(c, lianlianpay.SuccessResponse)
}

// writeLianlianNotifyResponse 按连连要求返回纯文本结果。
func writeLianlianNotifyResponse(c *gin.Context, text string) {
	c.String(http.StatusOK, text)
}

// lianlianPayMethods 返回钱包页展示的连连支付方式（支付宝/微信扫码）。
func lianlianPayMethods() []map[string]string {
	return []map[string]string{
		{"name": "Alipay", "type": lianlianpay.PayMethodAlipay, "color": "#1677FF", "min_topup": strconv.FormatInt(lianlianMinAmountCents, 10)},
		{"name": "WeChat Pay", "type": lianlianpay.PayMethodWxpay, "color": "#07C160", "min_topup": strconv.FormatInt(lianlianMinAmountCents, 10)},
	}
}
