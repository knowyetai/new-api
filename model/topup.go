package model

import (
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type TopUp struct {
	BillingUnitId     int        `json:"billing_unit_id" gorm:"index;default:0"`
	OperatorUserId    int        `json:"operator_user_id" gorm:"index;default:0"`
	Id                int        `json:"id"`
	UserId            int        `json:"user_id" gorm:"index;uniqueIndex:idx_top_ups_user_lianlian_request_id,priority:1"`
	Amount            int64      `json:"amount"`
	AmountUnit        AmountUnit `json:"amount_unit" gorm:"type:varchar(16);default:''"`
	Money             float64    `json:"money"` // Lianlian payment money is stored in AmountUnit, currently CNY cents.
	ProviderTradeNo   string     `json:"provider_trade_no" gorm:"type:varchar(128);default:''"`
	LianlianRequestID *string    `json:"-" gorm:"type:varchar(64);uniqueIndex:idx_top_ups_user_lianlian_request_id,priority:2"`
	LianlianQRCode    string     `json:"-" gorm:"type:text"`
	TradeNo           string     `json:"trade_no" gorm:"unique;type:varchar(255);index"`
	PaymentMethod     string     `json:"payment_method" gorm:"type:varchar(50)"`
	PaymentProvider   string     `json:"payment_provider" gorm:"type:varchar(50);default:'';index:idx_top_ups_provider_status_create_time,priority:1"`
	CreateTime        int64      `json:"create_time" gorm:"index:idx_top_ups_provider_status_create_time,priority:3"`
	CompleteTime      int64      `json:"complete_time"`
	Status            string     `json:"status" gorm:"index:idx_top_ups_provider_status_create_time,priority:2"`
}

const (
	TopUpProviderStatusCreateTimeIndex = "idx_top_ups_provider_status_create_time"
	topUpUserLianlianRequestIDIndex    = "idx_top_ups_user_lianlian_request_id"
)

const (
	PaymentMethodStripe       = "stripe"
	PaymentMethodCreem        = "creem"
	PaymentMethodWaffo        = "waffo"
	PaymentMethodWaffoPancake = "waffo_pancake"
	PaymentMethodBalance      = "balance"
)

const (
	PaymentProviderEpay         = "epay"
	PaymentProviderStripe       = "stripe"
	PaymentProviderCreem        = "creem"
	PaymentProviderWaffo        = "waffo"
	PaymentProviderWaffoPancake = "waffo_pancake"
	PaymentProviderLianlian     = "lianlian"
	PaymentProviderBalance      = "balance"
)

type AmountUnit string

const (
	AmountUnitUSDCent AmountUnit = "USD_CENT"
	AmountUnitCNYCent AmountUnit = "CNY_CENT"
	AmountUnitUSD     AmountUnit = "USD"
	AmountUnitQuota   AmountUnit = "QUOTA"

	amountMinorUnitScale int64 = 100
)

var (
	ErrPaymentMethodMismatch     = errors.New("payment method mismatch")
	ErrTopUpNotFound             = errors.New("topup not found")
	ErrTopUpStatusInvalid        = errors.New("topup status invalid")
	ErrInvalidTopUpQuota         = errors.New("invalid top-up quota")
	ErrTopUpQuotaLimitExceeded   = errors.New("top-up quota limit exceeded")
	ErrWalletQuotaLimitExceeded  = errors.New("wallet quota limit exceeded")
	ErrInvalidAmountUnit         = errors.New("invalid top-up amount unit")
	ErrInvalidAmountExchangeRate = errors.New("invalid top-up amount exchange rate")
)

func (topUp *TopUp) Insert() error {
	var err error
	err = DB.Create(topUp).Error
	return err
}

func topUpQuotaMaxCurrent(creditedQuota int) (int, error) {
	if creditedQuota <= 0 || creditedQuota > common.MaxWalletQuota {
		return 0, ErrInvalidTopUpQuota
	}
	return common.MaxWalletQuota - creditedQuota, nil
}

// ValidateTopUpQuotaCapacity performs the user-facing pre-payment check. The
// settlement path repeats the same invariant with an atomic conditional
// update, because the wallet balance can change after checkout creation.
func ValidateTopUpQuotaCapacity(userId int, creditedQuota int) error {
	maxCurrentQuota, err := topUpQuotaMaxCurrent(creditedQuota)
	if err != nil {
		return err
	}

	var user User
	if err := DB.Select("quota").Where("id = ?", userId).First(&user).Error; err != nil {
		return err
	}
	if user.Quota > maxCurrentQuota {
		return ErrTopUpQuotaLimitExceeded
	}
	return nil
}

// creditTopUpQuota atomically enforces the wallet ceiling while adding quota.
// Keeping the predicate and increment in one UPDATE prevents two
// concurrent callbacks from both passing a separate read/check.
func creditTopUpQuota(tx *gorm.DB, userId int, creditedQuota int, updates map[string]any) error {
	maxCurrentQuota, err := topUpQuotaMaxCurrent(creditedQuota)
	if err != nil {
		return err
	}

	updateFields := make(map[string]any, len(updates)+1)
	maps.Copy(updateFields, updates)
	updateFields["quota"] = gorm.Expr("quota + ?", creditedQuota)

	result := tx.Model(&User{}).
		Where("id = ? AND quota <= ?", userId, maxCurrentQuota).
		Updates(updateFields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}

	var count int64
	if err := tx.Model(&User{}).Where("id = ?", userId).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return ErrTopUpQuotaLimitExceeded
}

func (topUp *TopUp) Update() error {
	var err error
	err = DB.Save(topUp).Error
	return err
}

func GetTopUpById(id int) *TopUp {
	var topUp *TopUp
	var err error
	err = DB.Where("id = ?", id).First(&topUp).Error
	if err != nil {
		return nil
	}
	return topUp
}

func GetTopUpByTradeNo(tradeNo string) *TopUp {
	var topUp *TopUp
	var err error
	err = DB.Where("trade_no = ?", tradeNo).First(&topUp).Error
	if err != nil {
		return nil
	}
	return topUp
}

// GetLianlianTopUpByRequestID returns the order previously created for a
// user's idempotency key.
func GetLianlianTopUpByRequestID(userID int, requestID string) (*TopUp, error) {
	var topUp TopUp
	err := DB.Where("user_id = ? AND lianlian_request_id = ?", userID, requestID).
		First(&topUp).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &topUp, nil
}

// EffectiveAmountUnit preserves legacy Amount semantics for rows created before
// AmountUnit was introduced.
func (topUp *TopUp) EffectiveAmountUnit() AmountUnit {
	if topUp == nil {
		return ""
	}
	if topUp.AmountUnit != "" {
		return topUp.AmountUnit
	}
	switch topUp.PaymentProvider {
	case PaymentProviderCreem:
		return AmountUnitQuota
	case PaymentProviderLianlian:
		// Lianlian orders must state whether Amount is CNY or USD cents. Guessing
		// can over-credit a CNY wallet by the exchange-rate multiplier.
		return ""
	default:
		return AmountUnitUSD
	}
}

// QuotaAmount converts the stored top-up amount into the quota that should be
// credited. CNY amounts use the current configured exchange rate at settlement.
func (topUp *TopUp) QuotaAmount() (decimal.Decimal, error) {
	if topUp == nil {
		return decimal.Zero, ErrInvalidAmountUnit
	}
	switch topUp.EffectiveAmountUnit() {
	case AmountUnitQuota:
		return decimal.NewFromInt(topUp.Amount), nil
	case AmountUnitUSD:
		return decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)), nil
	case AmountUnitUSDCent:
		return decimal.NewFromInt(topUp.Amount).
			Div(decimal.NewFromInt(amountMinorUnitScale)).
			Mul(decimal.NewFromFloat(common.QuotaPerUnit)), nil
	case AmountUnitCNYCent:
		exchangeRate := operation_setting.USDExchangeRate
		if exchangeRate <= 0 {
			return decimal.Zero, ErrInvalidAmountExchangeRate
		}
		return decimal.NewFromInt(topUp.Amount).
			Div(decimal.NewFromInt(amountMinorUnitScale)).
			Div(decimal.NewFromFloat(exchangeRate)).
			Mul(decimal.NewFromFloat(common.QuotaPerUnit)), nil
	default:
		return decimal.Zero, ErrInvalidAmountUnit
	}
}

// Quota converts the stored top-up amount into the wallet quota to credit.
func (topUp *TopUp) Quota() (int, error) {
	quota, err := topUp.QuotaAmount()
	if err != nil {
		return 0, err
	}
	return common.WalletQuotaFromDecimalStrict(quota)
}

// UpdateTopUpProviderTradeNo records an upstream transaction number without
// overwriting payment state. A callback may complete the order while the
// create-payment response is still in flight.
func UpdateTopUpProviderTradeNo(tradeNo string, providerTradeNo string) error {
	providerTradeNo = strings.TrimSpace(providerTradeNo)
	if tradeNo == "" || providerTradeNo == "" {
		return nil
	}
	result := DB.Model(&TopUp{}).
		Where("trade_no = ? AND (provider_trade_no = '' OR provider_trade_no = ?)", tradeNo, providerTradeNo).
		Update("provider_trade_no", providerTradeNo)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}

	var topUp TopUp
	if err := DB.Select("provider_trade_no").Where("trade_no = ?", tradeNo).First(&topUp).Error; err != nil {
		return ErrTopUpNotFound
	}
	if topUp.ProviderTradeNo != providerTradeNo {
		return ErrLianlianProviderTradeNoMismatch
	}
	return nil
}

// UpdateLianlianPaymentResult stores the retryable QR payload alongside the
// provider transaction number without changing settlement state.
func UpdateLianlianPaymentResult(tradeNo string, providerTradeNo string, qrCode string) error {
	if err := UpdateTopUpProviderTradeNo(tradeNo, providerTradeNo); err != nil {
		return err
	}
	qrCode = strings.TrimSpace(qrCode)
	if qrCode == "" {
		return nil
	}
	result := DB.Model(&TopUp{}).
		Where("trade_no = ? AND payment_provider = ?", tradeNo, PaymentProviderLianlian).
		Update("lianlian_qr_code", qrCode)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrTopUpNotFound
	}
	return nil
}

// FindPendingTopUpsByProvider returns a keyset page of pending or
// user-cancelled orders old enough for an upstream status check.
func FindPendingTopUpsByProvider(provider string, createdBefore int64, afterID int, limit int) ([]TopUp, error) {
	if limit <= 0 {
		limit = 50
	}
	query := DB.Where("payment_provider = ? AND status IN ? AND create_time <= ?",
		provider,
		[]string{common.TopUpStatusPending, common.TopUpStatusCancelled},
		createdBefore,
	)
	if afterID > 0 {
		query = query.Where("id > ?", afterID)
	}
	var topUps []TopUp
	err := query.Order("id asc").Limit(limit).Find(&topUps).Error
	return topUps, err
}

func HasPendingTopUpsByProvider(provider string, createdBefore int64, createdAfter int64) (bool, error) {
	var topUp TopUp
	query := DB.Select("id").
		Where("payment_provider = ? AND status IN ? AND create_time <= ?",
			provider,
			[]string{common.TopUpStatusPending, common.TopUpStatusCancelled},
			createdBefore,
		)
	if createdAfter > 0 {
		query = query.Where("create_time > ?", createdAfter)
	}
	err := query.Limit(1).Find(&topUp).Error
	return topUp.Id > 0, err
}

func UpdatePendingTopUpStatus(tradeNo string, expectedPaymentProvider string, targetStatus string) error {
	if tradeNo == "" {
		return errors.New("未提供支付单号")
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		topUp := &TopUp{}
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			return ErrTopUpNotFound
		}
		if expectedPaymentProvider != "" && topUp.PaymentProvider != expectedPaymentProvider {
			return ErrPaymentMethodMismatch
		}
		if topUp.Status != common.TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}

		topUp.Status = targetStatus
		return tx.Save(topUp).Error
	})
}

// TransitionLianlianTopUpStatus closes pending or user-cancelled Lianlian
// orders after the provider confirms a terminal state.
func TransitionLianlianTopUpStatus(tradeNo string, targetStatus string) (bool, error) {
	if tradeNo == "" {
		return false, errors.New("未提供支付单号")
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	updated := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		topUp := &TopUp{}
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			return ErrTopUpNotFound
		}
		if topUp.PaymentProvider != PaymentProviderLianlian {
			return ErrPaymentMethodMismatch
		}
		if topUp.Status == targetStatus {
			return nil
		}
		switch topUp.Status {
		case common.TopUpStatusPending, common.TopUpStatusCancelled:
		default:
			return nil
		}
		topUp.Status = targetStatus
		updated = true
		return tx.Save(topUp).Error
	})
	return updated, err
}

// RechargeEpay 原子完成易支付订单：订单行锁、状态校验、成功更新与用户额度增加
// 在同一个事务内完成，因此同一订单的并发/重复回调（包括多实例部署下）最多充值一次。
// alreadyDone=true 表示订单此前已完成，本次为幂等重复回调。
// 进程内的 LockOrder 只是优化，正确性由本函数的数据库行锁保证。
func RechargeEpay(tradeNo string, actualPaymentMethod string, callerIp string, paidAmount ...string) (alreadyDone bool, err error) {
	if tradeNo == "" {
		return false, errors.New("未提供支付单号")
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	var quotaToAdd int
	topUp := &TopUp{}
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			return ErrTopUpNotFound
		}
		if topUp.PaymentProvider != PaymentProviderEpay {
			return ErrPaymentMethodMismatch
		}
		if len(paidAmount) > 0 {
			paid, amountErr := decimal.NewFromString(paidAmount[0])
			if amountErr != nil || !paid.Equal(decimal.NewFromFloat(topUp.Money).Round(2)) || !paid.IsPositive() {
				return errors.New("payment amount mismatch")
			}
		}
		if topUp.Status == common.TopUpStatusSuccess {
			alreadyDone = true
			return nil
		}
		if topUp.Status != common.TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}
		if actualPaymentMethod != "" && topUp.PaymentMethod != actualPaymentMethod {
			topUp.PaymentMethod = actualPaymentMethod
		}
		var quotaErr error
		quotaToAdd, quotaErr = topUp.Quota()
		if quotaErr != nil || quotaToAdd <= 0 {
			return ErrInvalidTopUpQuota
		}
		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}
		return creditTopUpQuota(tx, topUp.UserId, quotaToAdd, nil)
	})
	if err != nil {
		if !errors.Is(err, ErrTopUpNotFound) && !errors.Is(err, ErrPaymentMethodMismatch) && !errors.Is(err, ErrTopUpStatusInvalid) {
			common.SysError("epay topup failed: " + err.Error())
		}
		return false, err
	}
	if alreadyDone {
		return true, nil
	}
	syncCreditUserQuotaCache(topUp.UserId, quotaToAdd, "epay topup")

	common.SysLog(fmt.Sprintf("易支付充值成功 trade_no=%s user_id=%d quota_to_add=%d money=%.2f", topUp.TradeNo, topUp.UserId, quotaToAdd, topUp.Money))
	RecordTopupLog(topUp.UserId, fmt.Sprintf("使用在线充值成功，充值金额: %v，支付金额：%f", logger.LogQuota(quotaToAdd), topUp.Money), callerIp, topUp.PaymentMethod, PaymentProviderEpay)
	return false, nil
}

var (
	// ErrLianlianAmountMismatch 表示连连回调金额与订单金额不一致，拒绝入账。
	ErrLianlianAmountMismatch = errors.New("lianlian payment amount mismatch")
	// ErrLianlianProviderTradeNoMismatch 表示同一商户订单对应了不同的上游流水号。
	ErrLianlianProviderTradeNoMismatch = errors.New("lianlian provider trade number mismatch")
)

// RechargeLianlian atomically completes a Lianlian order. CNY_CENT amounts use
// the current USD exchange rate at settlement time.
func RechargeLianlian(tradeNo string, actualPaymentMethod string, callerIp string, paidAmount string, providerTradeNo string) (alreadyDone bool, err error) {
	if tradeNo == "" {
		return false, errors.New("未提供支付单号")
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	var quotaToAdd int
	topUp := &TopUp{}
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			return ErrTopUpNotFound
		}
		if topUp.PaymentProvider != PaymentProviderLianlian {
			return ErrPaymentMethodMismatch
		}
		expectedMoneyCents := decimal.NewFromFloat(topUp.Money)
		paidCNY, amountErr := decimal.NewFromString(paidAmount)
		paidMoneyCents := paidCNY.Mul(decimal.NewFromInt(amountMinorUnitScale))
		if amountErr != nil ||
			!expectedMoneyCents.IsPositive() ||
			!expectedMoneyCents.IsInteger() ||
			!paidMoneyCents.IsInteger() ||
			!paidMoneyCents.Equal(expectedMoneyCents) {
			return ErrLianlianAmountMismatch
		}

		providerTradeNo = strings.TrimSpace(providerTradeNo)
		if providerTradeNo != "" {
			if len(providerTradeNo) > 128 ||
				(topUp.ProviderTradeNo != "" && topUp.ProviderTradeNo != providerTradeNo) {
				return ErrLianlianProviderTradeNoMismatch
			}
			topUp.ProviderTradeNo = providerTradeNo
		}
		if topUp.Status == common.TopUpStatusSuccess {
			if providerTradeNo != "" {
				if err := tx.Model(topUp).Update("provider_trade_no", providerTradeNo).Error; err != nil {
					return err
				}
			}
			alreadyDone = true
			return nil
		}
		switch topUp.Status {
		case common.TopUpStatusPending, common.TopUpStatusFailed, common.TopUpStatusExpired, common.TopUpStatusCancelled:
		default:
			return ErrTopUpStatusInvalid
		}
		if actualPaymentMethod != "" && topUp.PaymentMethod != actualPaymentMethod {
			return ErrPaymentMethodMismatch
		}
		var quotaErr error
		quotaToAdd, quotaErr = topUp.Quota()
		if quotaErr != nil || quotaToAdd <= 0 {
			return ErrInvalidTopUpQuota
		}
		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}
		return creditTopUpQuota(tx, topUp.UserId, quotaToAdd, nil)
	})
	if err != nil {
		if !errors.Is(err, ErrTopUpNotFound) &&
			!errors.Is(err, ErrPaymentMethodMismatch) &&
			!errors.Is(err, ErrTopUpStatusInvalid) &&
			!errors.Is(err, ErrLianlianAmountMismatch) &&
			!errors.Is(err, ErrLianlianProviderTradeNoMismatch) {
			common.SysError("lianlian topup failed: " + err.Error())
		}
		return false, err
	}
	if alreadyDone {
		return true, nil
	}
	syncCreditUserQuotaCache(topUp.UserId, quotaToAdd, "lianlian topup")

	common.SysLog(fmt.Sprintf("连连充值成功 trade_no=%s user_id=%d quota_to_add=%d money_cents=%.0f", topUp.TradeNo, topUp.UserId, quotaToAdd, topUp.Money))
	RecordTopupLog(topUp.UserId, fmt.Sprintf("使用连连支付充值成功，充值金额: %v，支付金额（分）：%.0f", logger.LogQuota(quotaToAdd), topUp.Money), callerIp, topUp.PaymentMethod, PaymentProviderLianlian)
	return false, nil
}

func Recharge(referenceId string, customerId string, callerIp string) (err error) {
	if referenceId == "" {
		return errors.New("未提供支付单号")
	}

	var quota int
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", referenceId).First(topUp).Error
		if err != nil {
			return errors.New("充值订单不存在")
		}

		if topUp.PaymentProvider != PaymentProviderStripe {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("充值订单状态错误")
		}

		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		err = tx.Save(topUp).Error
		if err != nil {
			return err
		}

		quota, err = common.WalletQuotaFromDecimalStrict(
			decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
		)
		if err != nil || quota <= 0 {
			return ErrInvalidTopUpQuota
		}
		return creditTopUpQuota(tx, topUp.UserId, quota, map[string]any{
			"stripe_customer": customerId,
		})
	})

	if err != nil {
		common.SysError("topup failed: " + err.Error())
		return errors.New("充值失败，请稍后重试")
	}
	syncCreditUserQuotaCache(topUp.UserId, quota, "stripe topup")

	RecordTopupLog(topUp.UserId, fmt.Sprintf("使用在线充值成功，充值金额: %v，支付金额：%d", logger.FormatQuota(quota), topUp.Amount), callerIp, topUp.PaymentMethod, PaymentMethodStripe)

	return nil
}

// topUpQueryWindowSeconds 限制充值记录查询的时间窗口（秒）。
const topUpQueryWindowSeconds int64 = 30 * 24 * 60 * 60

// topUpQueryCutoff 返回允许查询的最早 create_time（秒级 Unix 时间戳）。
func topUpQueryCutoff() int64 {
	return common.GetTimestamp() - topUpQueryWindowSeconds
}

func GetUserTopUps(userId int, pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	// Start transaction
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	cutoff := topUpQueryCutoff()

	// Get total count within transaction
	err = tx.Model(&TopUp{}).Where("user_id = ? AND create_time >= ?", userId, cutoff).Count(&total).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// Get paginated topups within same transaction
	err = tx.Where("user_id = ? AND create_time >= ?", userId, cutoff).Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// Commit transaction
	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	return topups, total, nil
}

// GetAllTopUps 获取全平台的充值记录（管理员使用，不限制时间窗口）
func GetAllTopUps(pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	if err = tx.Model(&TopUp{}).Count(&total).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	return topups, total, nil
}

// searchTopUpCountHardLimit 搜索充值记录时 COUNT 的安全上限，
// 防止对超大表执行无界 COUNT 触发 DoS。
const searchTopUpCountHardLimit = 10000

// SearchUserTopUps 按订单号搜索某用户的充值记录
func SearchUserTopUps(userId int, keyword string, pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	query := tx.Model(&TopUp{}).Where("user_id = ? AND create_time >= ?", userId, topUpQueryCutoff())
	if keyword != "" {
		pattern, perr := sanitizeLikePattern(keyword)
		if perr != nil {
			tx.Rollback()
			return nil, 0, perr
		}
		query = query.Where("trade_no LIKE ? ESCAPE '!'", pattern)
	}

	if err = query.Limit(searchTopUpCountHardLimit).Count(&total).Error; err != nil {
		tx.Rollback()
		common.SysError("failed to count search topups: " + err.Error())
		return nil, 0, errors.New("搜索充值记录失败")
	}

	if err = query.Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error; err != nil {
		tx.Rollback()
		common.SysError("failed to search topups: " + err.Error())
		return nil, 0, errors.New("搜索充值记录失败")
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	return topups, total, nil
}

// SearchAllTopUps 按订单号搜索全平台充值记录（管理员使用，不限制时间窗口）
func SearchAllTopUps(keyword string, pageInfo *common.PageInfo) (topups []*TopUp, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	query := tx.Model(&TopUp{})
	if keyword != "" {
		pattern, perr := sanitizeLikePattern(keyword)
		if perr != nil {
			tx.Rollback()
			return nil, 0, perr
		}
		query = query.Where("trade_no LIKE ? ESCAPE '!'", pattern)
	}

	if err = query.Limit(searchTopUpCountHardLimit).Count(&total).Error; err != nil {
		tx.Rollback()
		common.SysError("failed to count search topups: " + err.Error())
		return nil, 0, errors.New("搜索充值记录失败")
	}

	if err = query.Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&topups).Error; err != nil {
		tx.Rollback()
		common.SysError("failed to search topups: " + err.Error())
		return nil, 0, errors.New("搜索充值记录失败")
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	return topups, total, nil
}

// ManualCompleteTopUp 管理员手动完成订单并给用户充值
func ManualCompleteTopUp(tradeNo string, callerIp string) error {
	if tradeNo == "" {
		return errors.New("未提供订单号")
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	var userId int
	var quotaToAdd int
	var payMoney float64
	var paymentMethod string

	err := DB.Transaction(func(tx *gorm.DB) error {
		topUp := &TopUp{}
		// 行级锁，避免并发补单
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			return errors.New("充值订单不存在")
		}

		// 幂等处理：已成功直接返回
		if topUp.Status == common.TopUpStatusSuccess {
			return nil
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("订单状态不是待支付，无法补单")
		}

		// Stripe stores the credited USD value in Money. Other providers use
		// Amount plus AmountUnit, including legacy unit inference.
		var quotaErr error
		switch topUp.PaymentProvider {
		case PaymentProviderStripe:
			quotaToAdd, quotaErr = common.WalletQuotaFromDecimalStrict(
				decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(common.QuotaPerUnit)),
			)
		default:
			quotaToAdd, quotaErr = topUp.Quota()
		}
		if quotaErr != nil || quotaToAdd <= 0 {
			return ErrInvalidTopUpQuota
		}

		// 标记完成
		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}

		// 增加用户额度（立即写库，保持一致性）
		if err := creditTopUpQuota(tx, topUp.UserId, quotaToAdd, nil); err != nil {
			return err
		}

		userId = topUp.UserId
		payMoney = topUp.Money
		paymentMethod = topUp.PaymentMethod
		return nil
	})

	if err != nil {
		return err
	}

	// 事务外记录日志，避免阻塞
	syncCreditUserQuotaCache(userId, quotaToAdd, "manual topup")
	RecordTopupLog(userId, fmt.Sprintf("管理员补单成功，充值金额: %v，支付金额：%f", logger.FormatQuota(quotaToAdd), payMoney), callerIp, paymentMethod, "admin")
	return nil
}
func RechargeCreem(referenceId string, customerEmail string, customerName string, callerIp string) (err error) {
	if referenceId == "" {
		return errors.New("未提供支付单号")
	}

	var quota int
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", referenceId).First(topUp).Error
		if err != nil {
			return errors.New("充值订单不存在")
		}

		if topUp.PaymentProvider != PaymentProviderCreem {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("充值订单状态错误")
		}

		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		err = tx.Save(topUp).Error
		if err != nil {
			return err
		}

		quota, err = topUp.Quota()
		if err != nil || quota <= 0 {
			return ErrInvalidTopUpQuota
		}

		// 构建更新字段，优先使用邮箱，如果邮箱为空则使用用户名
		updateFields := map[string]any{}

		// 如果有客户邮箱，尝试更新用户邮箱（仅当用户邮箱为空时）
		if customerEmail != "" {
			// 先检查用户当前邮箱是否为空
			var user User
			err = tx.Where("id = ?", topUp.UserId).First(&user).Error
			if err != nil {
				return err
			}

			// 如果用户邮箱为空，则更新为支付时使用的邮箱
			if user.Email == "" {
				updateFields["email"] = customerEmail
			}
		}

		return creditTopUpQuota(tx, topUp.UserId, quota, updateFields)
	})

	if err != nil {
		common.SysError("creem topup failed: " + err.Error())
		return errors.New("充值失败，请稍后重试")
	}
	syncCreditUserQuotaCache(topUp.UserId, quota, "creem topup")

	RecordTopupLog(topUp.UserId, fmt.Sprintf("使用Creem充值成功，充值额度: %v，支付金额：%.2f", quota, topUp.Money), callerIp, topUp.PaymentMethod, PaymentMethodCreem)

	return nil
}

func RechargeWaffo(tradeNo string, callerIp string) (err error) {
	if tradeNo == "" {
		return errors.New("未提供支付单号")
	}

	var quotaToAdd int
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error
		if err != nil {
			return errors.New("充值订单不存在")
		}

		if topUp.PaymentProvider != PaymentProviderWaffo {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status == common.TopUpStatusSuccess {
			return nil // 幂等：已成功直接返回
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("充值订单状态错误")
		}

		quotaToAdd, err = topUp.Quota()
		if err != nil || quotaToAdd <= 0 {
			return ErrInvalidTopUpQuota
		}

		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}

		return creditTopUpQuota(tx, topUp.UserId, quotaToAdd, nil)
	})

	if err != nil {
		common.SysError("waffo topup failed: " + err.Error())
		return errors.New("充值失败，请稍后重试")
	}
	syncCreditUserQuotaCache(topUp.UserId, quotaToAdd, "waffo topup")

	if quotaToAdd > 0 {
		RecordTopupLog(topUp.UserId, fmt.Sprintf("Waffo充值成功，充值额度: %v，支付金额: %.2f", logger.FormatQuota(quotaToAdd), topUp.Money), callerIp, topUp.PaymentMethod, PaymentMethodWaffo)
	}

	return nil
}

func RechargeWaffoPancake(tradeNo string) (err error) {
	if tradeNo == "" {
		return errors.New("未提供支付单号")
	}

	var quotaToAdd int
	topUp := &TopUp{}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	err = DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error
		if err != nil {
			return errors.New("充值订单不存在")
		}

		if topUp.PaymentProvider != PaymentProviderWaffoPancake {
			return ErrPaymentMethodMismatch
		}

		if topUp.Status == common.TopUpStatusSuccess {
			return nil
		}

		if topUp.Status != common.TopUpStatusPending {
			return errors.New("充值订单状态错误")
		}

		quotaToAdd, err = topUp.Quota()
		if err != nil || quotaToAdd <= 0 {
			return ErrInvalidTopUpQuota
		}

		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}

		return creditTopUpQuota(tx, topUp.UserId, quotaToAdd, nil)
	})

	if err != nil {
		common.SysError("waffo pancake topup failed: " + err.Error())
		return errors.New("充值失败，请稍后重试")
	}
	syncCreditUserQuotaCache(topUp.UserId, quotaToAdd, "waffo pancake topup")

	if quotaToAdd > 0 {
		RecordLog(topUp.UserId, LogTypeTopup, fmt.Sprintf("Waffo Pancake充值成功，充值额度: %v，支付金额: %.2f", logger.FormatQuota(quotaToAdd), topUp.Money))
	}

	return nil
}
