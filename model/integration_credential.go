package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
)

// ResolveIntegrationCredential is for a trusted platform which has already
// authenticated the external subject. It never accepts a payer or a native user ID.
// The OAuth binding and Token stay authoritative in this database.
func ResolveIntegrationCredential(providerID int, issuer, subject string, displayNames ...string) (*Token, error) {
	if providerID <= 0 || subject == "" || len(subject) > 256 || issuer == "" {
		return nil, errors.New("invalid identity")
	}
	displayName := ""
	if len(displayNames) > 0 {
		displayName = integrationDisplayName(displayNames[0])
	}
	var result Token
	err := DB.Transaction(func(tx *gorm.DB) error {
		var provider CustomOAuthProvider
		if err := tx.First(&provider, providerID).Error; err != nil {
			return err
		}
		if provider.AccessPolicy != "" || !provider.Enabled || provider.WellKnown != strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration" || provider.UserIdField != "sub" {
			return errors.New("provider mismatch")
		}
		var binding UserOAuthBinding
		query := func(db *gorm.DB) *gorm.DB {
			return db.Where("provider_id = ? AND provider_user_id = ?", providerID, subject)
		}
		err := query(tx).First(&binding).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Only first provision serializes on the provider; existing identities lock
			// their own user, allowing unrelated model tasks to resolve concurrently.
			if err = lockForUpdate(tx).First(&provider, providerID).Error; err != nil {
				return err
			}
			if provider.AccessPolicy != "" || !provider.Enabled || provider.WellKnown != strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration" || provider.UserIdField != "sub" {
				return errors.New("provider unavailable")
			}
			err = query(lockForUpdate(tx)).First(&binding).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", providerID, subject)))
				user := User{Username: fmt.Sprintf("sso_%x", digest[:8]), DisplayName: "SSO user", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
				if displayName != "" {
					user.DisplayName = displayName
				}
				if err = user.InsertWithTx(tx, 0); err != nil {
					return err
				}
				binding = UserOAuthBinding{UserId: user.Id, ProviderId: providerID, ProviderUserId: subject}
				if err = tx.Create(&binding).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		// Fail closed under case-insensitive database collations; never alias subjects.
		if binding.ProviderUserId != subject {
			return errors.New("identity mismatch")
		}
		var user User
		if err = lockForUpdate(tx).First(&user, binding.UserId).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled {
			return errors.New("user disabled")
		}
		// Refresh under the user lock to see a concurrent first Token creation.
		if err = lockForUpdate(tx).First(&binding, binding.Id).Error; err != nil {
			return err
		}
		if binding.ProviderUserId != subject {
			return errors.New("identity changed")
		}
		if displayName != "" && displayName != user.DisplayName {
			if err = tx.Model(&user).Update("display_name", displayName).Error; err != nil {
				return err
			}
		}
		if binding.ModelTokenId == 0 {
			var count int64
			if err = tx.Model(&Token{}).Where("user_id = ?", user.Id).Count(&count).Error; err != nil {
				return err
			}
			if count >= int64(operation_setting.GetMaxUserTokens()) {
				return errors.New("token limit")
			}
			key, err := common.GenerateKey()
			if err != nil {
				return err
			}
			result = Token{UserId: user.Id, Key: key, Name: "known-engine", Status: common.TokenStatusEnabled, CreatedTime: common.GetTimestamp(), AccessedTime: common.GetTimestamp(), ExpiredTime: -1, UnlimitedQuota: true}
			if err = tx.Create(&result).Error; err != nil {
				return err
			}
			return tx.Model(&binding).Update("model_token_id", result.Id).Error
		}
		if err = lockForUpdate(tx).First(&result, binding.ModelTokenId).Error; err != nil {
			return err
		}
		if result.UserId != user.Id || result.Status != common.TokenStatusEnabled || (result.ExpiredTime != -1 && result.ExpiredTime <= common.GetTimestamp()) {
			return errors.New("token unavailable")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// integrationDisplayName follows the existing native display-name limit.
// It is presentation metadata, never an account lookup or authorization field.
func integrationDisplayName(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > 20 {
		runes = runes[:20]
	}
	return string(runes)
}

// SyncIntegrationDisplayName updates only accounts provisioned for model use.
// Ordinary OAuth accounts keep their existing native profile behavior.
func SyncIntegrationDisplayName(providerID int, subject, displayName string) error {
	displayName = integrationDisplayName(displayName)
	if displayName == "" {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var binding UserOAuthBinding
		err := tx.Where("provider_id = ? AND provider_user_id = ?", providerID, subject).First(&binding).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if binding.ProviderUserId != subject {
			return errors.New("identity mismatch")
		}
		if binding.ModelTokenId == 0 {
			return nil
		}
		var user User
		if err = lockForUpdate(tx).First(&user, binding.UserId).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled {
			return errors.New("user disabled")
		}
		if user.DisplayName == displayName {
			return nil
		}
		return tx.Model(&user).Update("display_name", displayName).Error
	})
}
