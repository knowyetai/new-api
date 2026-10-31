package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/lianlianpay"
	"github.com/QuantumNous/new-api/setting"
)

const (
	lianlianReconcileMinAge    = 2 * time.Minute
	lianlianReconcileBatchSize = 50
)

// LianlianReconcileSummary describes one compensation pass.
type LianlianReconcileSummary struct {
	Scanned int `json:"scanned"`
	Settled int `json:"settled"`
	Closed  int `json:"closed"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
}

// ReconcileLianlianTopUpsOnce queries Lianlian for pending orders that are old
// enough that a callback should already have arrived. Query results go through
// the same amount, payment-method and idempotent settlement path as callbacks.
func ReconcileLianlianTopUpsOnce(ctx context.Context) (LianlianReconcileSummary, error) {
	summary := LianlianReconcileSummary{}
	if !isLianlianQueryEnabled() {
		return summary, nil
	}

	now := time.Now()
	lastID := 0
	for {
		topUps, err := model.FindPendingTopUpsByProvider(
			model.PaymentProviderLianlian,
			now.Add(-lianlianReconcileMinAge).Unix(),
			lastID,
			lianlianReconcileBatchSize,
		)
		if err != nil {
			return summary, err
		}
		if len(topUps) == 0 {
			break
		}
		summary.Scanned += len(topUps)

		for i := range topUps {
			if err := ctx.Err(); err != nil {
				return summary, err
			}
			topUp := &topUps[i]
			lastID = topUp.Id
			switch reconcileLianlianTopUp(ctx, topUp, now) {
			case lianlianReconcileSettled:
				summary.Settled++
			case lianlianReconcileClosed:
				summary.Closed++
			case lianlianReconcileSkipped:
				summary.Skipped++
			case lianlianReconcileFailed:
				summary.Failed++
			}
		}
		if len(topUps) < lianlianReconcileBatchSize {
			break
		}
	}

	if summary.Failed > 0 {
		return summary, fmt.Errorf("lianlian reconciliation failed for %d order(s)", summary.Failed)
	}
	return summary, nil
}

type lianlianReconcileOutcome int

const (
	lianlianReconcileSkipped lianlianReconcileOutcome = iota
	lianlianReconcileSettled
	lianlianReconcileClosed
	lianlianReconcileFailed
)

func reconcileLianlianTopUp(ctx context.Context, topUp *model.TopUp, now time.Time) lianlianReconcileOutcome {
	result, queryErr := queryLianlianOrder(ctx, topUp.TradeNo)
	if queryErr != nil {
		if result != nil {
			targetStatus := ""
			switch result.RetCode {
			case lianlianpay.RetCodeOrderExpired:
				targetStatus = common.TopUpStatusExpired
			case lianlianpay.RetCodeOrderNotFound:
				if topUp.CreateTime <= now.Add(-time.Duration(lianlianOrderTTLSeconds)*time.Second).Unix() {
					targetStatus = common.TopUpStatusExpired
				}
			}
			if targetStatus != "" {
				closed, closeErr := closeLianlianTopUp(topUp.TradeNo, targetStatus)
				if closeErr != nil {
					logger.LogError(ctx, fmt.Sprintf("连连 查询终态落库失败 trade_no=%s status=%s error=%q", topUp.TradeNo, targetStatus, closeErr.Error()))
					return lianlianReconcileFailed
				}
				if closed {
					logger.LogInfo(ctx, fmt.Sprintf("连连 查询确认订单关闭 trade_no=%s status=%s ret_code=%s", topUp.TradeNo, targetStatus, result.RetCode))
					return lianlianReconcileClosed
				}
				return lianlianReconcileSkipped
			}
		}
		if _, expired := expireLianlianTopUpIfNeeded(ctx, topUp, now.Unix()); expired {
			logger.LogInfo(ctx, fmt.Sprintf("连连 查询失败且订单已过期 trade_no=%s error=%q", topUp.TradeNo, queryErr.Error()))
			return lianlianReconcileClosed
		}
		logger.LogWarn(ctx, fmt.Sprintf("连连 订单查询失败 trade_no=%s error=%q", topUp.TradeNo, queryErr.Error()))
		return lianlianReconcileFailed
	}
	if strings.TrimSpace(result.MchID) != strings.TrimSpace(setting.LianlianMchId) {
		logger.LogWarn(ctx, fmt.Sprintf("连连 查询商户号不匹配 trade_no=%s mch_id=%s", topUp.TradeNo, result.MchID))
		return lianlianReconcileFailed
	}
	if strings.TrimSpace(result.TxnSeqno) != topUp.TradeNo {
		logger.LogWarn(ctx, fmt.Sprintf("连连 查询订单号不匹配 trade_no=%s query_txn_seqno=%s", topUp.TradeNo, result.TxnSeqno))
		return lianlianReconcileFailed
	}
	status := strings.ToUpper(strings.TrimSpace(result.TxnStatus))
	if status == "" {
		if _, expired := expireLianlianTopUpIfNeeded(ctx, topUp, now.Unix()); expired {
			logger.LogInfo(ctx, fmt.Sprintf("连连 查询无终态且订单已过期 trade_no=%s", topUp.TradeNo))
			return lianlianReconcileClosed
		}
		return lianlianReconcileSkipped
	}
	if status == "FAILURE" || status == "CLOSED" || status == "CANCEL" || status == "EXPIRED" {
		targetStatus := common.TopUpStatusFailed
		if status != "FAILURE" {
			targetStatus = common.TopUpStatusExpired
		}
		closed, closeErr := closeLianlianTopUp(topUp.TradeNo, targetStatus)
		if closeErr != nil {
			logger.LogError(ctx, fmt.Sprintf("连连 查询失败态落库失败 trade_no=%s status=%s error=%q", topUp.TradeNo, targetStatus, closeErr.Error()))
			return lianlianReconcileFailed
		}
		if closed {
			logger.LogInfo(ctx, fmt.Sprintf("连连 查询确认支付失败 trade_no=%s status=%s", topUp.TradeNo, status))
			return lianlianReconcileClosed
		}
		return lianlianReconcileSkipped
	}
	if !lianlianpay.IsPaidStatus(status) {
		if _, expired := expireLianlianTopUpIfNeeded(ctx, topUp, now.Unix()); expired {
			logger.LogInfo(ctx, fmt.Sprintf("连连 查询未支付且订单已过期 trade_no=%s status=%s", topUp.TradeNo, status))
			return lianlianReconcileClosed
		}
		return lianlianReconcileSkipped
	}
	orderAmount := result.OrderAmount.String()
	if strings.TrimSpace(orderAmount) == "" {
		logger.LogWarn(ctx, fmt.Sprintf("连连 查询缺少订单金额 trade_no=%s", topUp.TradeNo))
		return lianlianReconcileFailed
	}
	paymentMethod := lianlianpay.PayMethodForPayType(result.PayTypeValue())
	if paymentMethod == "" {
		logger.LogWarn(ctx, fmt.Sprintf("连连 查询支付方式无效 trade_no=%s pay_type=%s", topUp.TradeNo, result.PayTypeValue()))
		return lianlianReconcileFailed
	}

	alreadyDone, settleErr := model.RechargeLianlian(
		topUp.TradeNo,
		paymentMethod,
		"lianlian-reconcile",
		orderAmount,
		result.PlatformTxNo,
	)
	if settleErr != nil {
		logger.LogError(ctx, fmt.Sprintf("连连 查询补偿入账失败 trade_no=%s error=%q", topUp.TradeNo, settleErr.Error()))
		return lianlianReconcileFailed
	}
	if alreadyDone {
		return lianlianReconcileSkipped
	}
	logger.LogInfo(ctx, fmt.Sprintf("连连 查询补偿入账成功 trade_no=%s", topUp.TradeNo))
	return lianlianReconcileSettled
}

func closeLianlianTopUp(tradeNo string, targetStatus string) (bool, error) {
	updated, err := model.TransitionLianlianTopUpStatus(tradeNo, targetStatus)
	if err != nil {
		return false, err
	}
	return updated, nil
}

func queryLianlianOrder(ctx context.Context, tradeNo string) (*lianlianpay.OrderQueryResult, error) {
	signed, err := lianlianpay.BuildOrderQueryRequest(lianlianpay.OrderQueryRequest{
		Env:        setting.LianlianEnv,
		Gateway:    setting.LianlianGateway,
		PrivateKey: setting.LianlianPrivateKey,
		MerchantID: setting.LianlianMchId,
		TradeNo:    tradeNo,
	})
	if err != nil {
		return nil, err
	}
	body, err := postSignedLianlianRequest(ctx, signed, setting.GetLianlianPlatformPublicKey())
	if err != nil {
		return nil, err
	}
	return lianlianpay.ParseOrderQueryResponse(body)
}
