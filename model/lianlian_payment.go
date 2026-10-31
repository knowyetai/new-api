package model

import (
	"errors"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service/lianlianpay"
	"github.com/QuantumNous/new-api/setting"

	"gorm.io/gorm"
)

const lianlianPaymentConfigLockOptionKey = "LianlianMchId"

const lianlianCallbackConfigLockSeconds = int64(
	(lianlianpay.PayExpireMinutes + lianlianpay.CallbackRetryMinutes) * 60,
)

var (
	ErrLianlianPaymentDisabled         = errors.New("lianlian payment disabled")
	ErrLianlianPaymentConfigIncomplete = errors.New("lianlian payment config incomplete")
	ErrLianlianConfigOpenOrders        = errors.New("lianlian open orders block config change")
	ErrLianlianCreateResultInvalid     = errors.New("lianlian create result invalid")
)

// LianlianPaymentConfig is the persisted configuration snapshot used for one
// create-pay request. Reading it under the same lock as configuration writes
// prevents an order from being created with a partially switched config.
type LianlianPaymentConfig struct {
	Enabled           bool
	Env               string
	Gateway           string
	MerchantID        string
	PrivateKey        string
	PlatformPublicKey string
	BusiType          string
	RiskCategory      string
}

// CreateLianlianTopUpWithConfigLock persists the local order while holding the
// shared Lianlian configuration lock and returns the exact config snapshot
// that must be used for the upstream request.
func CreateLianlianTopUpWithConfigLock(
	topUp *TopUp,
	validate func(LianlianPaymentConfig) error,
) (LianlianPaymentConfig, error) {
	var config LianlianPaymentConfig
	if topUp == nil {
		return config, errors.New("lianlian top-up is required")
	}

	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockLianlianPaymentConfig(tx); err != nil {
			return err
		}
		var err error
		config, err = loadLianlianPaymentConfig(tx)
		if err != nil {
			return err
		}
		if !config.Enabled {
			return ErrLianlianPaymentDisabled
		}
		if strings.TrimSpace(config.MerchantID) == "" ||
			strings.TrimSpace(config.PrivateKey) == "" {
			return ErrLianlianPaymentConfigIncomplete
		}
		if validate != nil {
			if err := validate(config); err != nil {
				return err
			}
		}
		return tx.Create(topUp).Error
	})
	return config, err
}

// CompleteLianlianOrderCreation makes an accepted order payable only after its
// QR payload has been validated. A callback may win the race and settle the
// order first; in that case the stored success state is preserved.
func CompleteLianlianOrderCreation(tradeNo string, providerTradeNo string, qrCode string) (*TopUp, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	providerTradeNo = strings.TrimSpace(providerTradeNo)
	qrCode = strings.TrimSpace(qrCode)
	if tradeNo == "" || qrCode == "" {
		return nil, ErrLianlianCreateResultInvalid
	}
	if providerTradeNo == "" || len(providerTradeNo) > 128 {
		return nil, ErrLianlianProviderTradeNoInvalid
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	var topUp TopUp
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(&topUp).Error; err != nil {
			return ErrTopUpNotFound
		}
		if topUp.PaymentProvider != PaymentProviderLianlian {
			return ErrPaymentMethodMismatch
		}
		if topUp.ProviderTradeNo != "" && topUp.ProviderTradeNo != providerTradeNo {
			return ErrLianlianProviderTradeNoMismatch
		}
		topUp.ProviderTradeNo = providerTradeNo
		topUp.LianlianQRCode = qrCode
		switch topUp.Status {
		case common.TopUpStatusCreateUnknown, common.TopUpStatusPending:
			topUp.Status = common.TopUpStatusPending
		case common.TopUpStatusSuccess, common.TopUpStatusFailed, common.TopUpStatusExpired, common.TopUpStatusCancelled:
		default:
			return ErrTopUpStatusInvalid
		}
		return tx.Save(&topUp).Error
	})
	return &topUp, err
}

// UpdateLianlianOptionsAtomic serializes configuration changes with order
// creation. Changes to the effective gateway identity or signing key are
// rejected while any recoverable Lianlian order is still open.
func UpdateLianlianOptionsAtomic(values map[string]string) error {
	if len(values) == 0 {
		return nil
	}

	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockLianlianPaymentConfig(tx); err != nil {
			return err
		}
		current, err := loadLianlianPaymentConfig(tx)
		if err != nil {
			return err
		}
		next := overlayLianlianPaymentConfig(current, values)
		if lianlianConfigIdentityChanged(current, next) {
			hasOrdersLockingConfig, err := hasLianlianOrdersLockingConfig(tx)
			if err != nil {
				return err
			}
			if hasOrdersLockingConfig {
				return ErrLianlianConfigOpenOrders
			}
		}
		for key, value := range values {
			if err := upsertOption(tx, key, value); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return applyOptionUpdates(values)
}

func lockLianlianPaymentConfig(tx *gorm.DB) error {
	var option Option
	err := tx.Where(map[string]any{"key": lianlianPaymentConfigLockOptionKey}).First(&option).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		option = Option{
			Key:   lianlianPaymentConfigLockOptionKey,
			Value: setting.LianlianMchId,
		}
		if err := tx.Create(&option).Error; err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return lockForUpdate(tx).
		Where(map[string]any{"key": lianlianPaymentConfigLockOptionKey}).
		First(&Option{}).Error
}

func loadLianlianPaymentConfig(tx *gorm.DB) (LianlianPaymentConfig, error) {
	keys := []string{
		"LianlianEnabled",
		"LianlianEnv",
		"LianlianGateway",
		"LianlianMchId",
		"LianlianPrivateKey",
		"LianlianPlatformPublicCert",
		"LianlianBusiType",
		"LianlianRiskCategory",
	}
	var options []Option
	if err := tx.Where(map[string]any{"key": keys}).Find(&options).Error; err != nil {
		return LianlianPaymentConfig{}, err
	}
	stored := make(map[string]string, len(options))
	for i := range options {
		stored[options[i].Key] = options[i].Value
	}
	return lianlianPaymentConfigFromValues(stored), nil
}

func lianlianPaymentConfigFromValues(values map[string]string) LianlianPaymentConfig {
	enabled, _ := strconv.ParseBool(optionValueOr(values, "LianlianEnabled", strconv.FormatBool(setting.LianlianEnabled)))
	return LianlianPaymentConfig{
		Enabled:           enabled,
		Env:               optionValueOr(values, "LianlianEnv", setting.LianlianEnv),
		Gateway:           optionValueOr(values, "LianlianGateway", setting.LianlianGateway),
		MerchantID:        optionValueOr(values, "LianlianMchId", setting.LianlianMchId),
		PrivateKey:        optionValueOr(values, "LianlianPrivateKey", setting.LianlianPrivateKey),
		PlatformPublicKey: optionValueOr(values, "LianlianPlatformPublicCert", setting.LianlianPlatformPublicCert),
		BusiType:          optionValueOr(values, "LianlianBusiType", setting.LianlianBusiType),
		RiskCategory:      optionValueOr(values, "LianlianRiskCategory", setting.LianlianRiskCategory),
	}
}

func overlayLianlianPaymentConfig(current LianlianPaymentConfig, updates map[string]string) LianlianPaymentConfig {
	values := map[string]string{
		"LianlianEnabled":            strconv.FormatBool(current.Enabled),
		"LianlianEnv":                current.Env,
		"LianlianGateway":            current.Gateway,
		"LianlianMchId":              current.MerchantID,
		"LianlianPrivateKey":         current.PrivateKey,
		"LianlianPlatformPublicCert": current.PlatformPublicKey,
		"LianlianBusiType":           current.BusiType,
		"LianlianRiskCategory":       current.RiskCategory,
	}
	for key, value := range updates {
		values[key] = value
	}
	return lianlianPaymentConfigFromValues(values)
}

func lianlianConfigIdentityChanged(current LianlianPaymentConfig, next LianlianPaymentConfig) bool {
	return current.Env != next.Env ||
		current.Gateway != next.Gateway ||
		current.MerchantID != next.MerchantID ||
		current.PrivateKey != next.PrivateKey ||
		current.PlatformPublicKey != next.PlatformPublicKey
}

// Recently expired orders keep the configuration locked because Lianlian may
// still deliver a successful callback signed by that merchant and public key.
func hasLianlianOrdersLockingConfig(tx *gorm.DB) (bool, error) {
	var topUp TopUp
	callbackRetentionStart := common.GetTimestamp() - lianlianCallbackConfigLockSeconds
	err := tx.Select("id").
		Where(
			"payment_provider = ? AND (status IN ? OR (status = ? AND create_time > ?))",
			PaymentProviderLianlian,
			[]string{
				common.TopUpStatusCreateUnknown,
				common.TopUpStatusPending,
				common.TopUpStatusCancelled,
			},
			common.TopUpStatusExpired,
			callbackRetentionStart,
		).
		Limit(1).
		Find(&topUp).Error
	return topUp.Id > 0, err
}

func upsertOption(tx *gorm.DB, key string, value string) error {
	var option Option
	err := tx.Where(map[string]any{"key": key}).First(&option).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&Option{Key: key, Value: value}).Error
	}
	if err != nil {
		return err
	}
	return tx.Model(&option).Update("value", value).Error
}

func optionValueOr(values map[string]string, key string, fallback string) string {
	if value, ok := values[key]; ok {
		return value
	}
	return fallback
}
