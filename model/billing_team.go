package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var (
	ErrTeamUnavailable       = errors.New("TEAM_UNAVAILABLE")
	ErrTeamInvalid           = errors.New("TEAM_INVALID_REQUEST")
	ErrTeamConflict          = errors.New("TEAM_MEMBERSHIP_CHANGED")
	ErrTeamInvitationExpired = errors.New("TEAM_INVITATION_EXPIRED")
	ErrTeamLimit             = errors.New("TEAM_LIMIT_REACHED")
)

// Invitations bind to a native account or verified directory subject, never a display name.
type BillingUnitInvitation struct {
	Id              int    `json:"id" gorm:"primaryKey"`
	BillingUnitId   int    `json:"billing_unit_id" gorm:"uniqueIndex:idx_team_invitee,priority:1;index"`
	RecipientKey    string `json:"-" gorm:"type:varchar(64);uniqueIndex:idx_team_invitee,priority:2"`
	RecipientUserId int    `json:"-" gorm:"index"`
	ProviderId      int    `json:"-" gorm:"index:idx_team_external,priority:1"`
	Subject         string `json:"-" gorm:"type:varchar(256);index:idx_team_external,priority:2"`
	Username        string `json:"username" gorm:"type:varchar(128)"`
	DisplayName     string `json:"display_name" gorm:"type:varchar(255)"`
	InviterUserId   int    `json:"inviter_user_id"`
	Status          string `json:"status" gorm:"type:varchar(16);index"`
	ExpiresAt       int64  `json:"expires_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

type TeamInvitee struct {
	UserId, ProviderId             int
	Subject, Username, DisplayName string
}

func CreateSelfServiceBillingUnit(ownerId int, name, requestKey string) (*BillingUnit, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 128 || len(requestKey) < 8 || len(requestKey) > 80 {
		return nil, ErrTeamInvalid
	}
	var result BillingUnit
	err := DB.Transaction(func(tx *gorm.DB) error {
		var owner User
		if err := lockForUpdate(tx).First(&owner, ownerId).Error; err != nil {
			return err
		}
		if owner.Status != common.UserStatusEnabled {
			return ErrTeamUnavailable
		}
		var count int64
		if err := tx.Model(&BillingUnit{}).Where("payer_user_id = ?", ownerId).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrTeamUnavailable
		}
		err := tx.Where("owner_user_id = ? AND creation_key = ?", ownerId, requestKey).First(&result).Error
		if err == nil {
			if result.Name != name {
				return ErrTeamInvalid
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err = tx.Model(&BillingUnit{}).Where("owner_user_id = ?", ownerId).Count(&count).Error; err != nil {
			return err
		}
		if count >= 20 {
			return ErrTeamLimit
		}
		// No registration rewards, login password, email, external identity, or API credential.
		payer := User{Username: "team_" + common.GetRandomString(15), DisplayName: "Team billing", AffCode: common.GetRandomString(24), Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: owner.Group, Setting: `{"billing_preference":"wallet_only"}`}
		if err = tx.Create(&payer).Error; err != nil {
			return err
		}
		result = BillingUnit{Name: name, OwnerUserId: ownerId, PayerUserId: payer.Id, CreationKey: &requestKey}
		if err = tx.Create(&result).Error; err != nil {
			return err
		}
		if err = tx.Model(&BillingUnitMember{}).Where("user_id = ?", ownerId).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return tx.Create(&BillingUnitMember{UserId: ownerId, BillingUnitId: result.Id}).Error
		}
		return nil
	})
	return &result, err
}

func teamManaged(tx *gorm.DB, unitId, actor int) (*BillingUnit, error) {
	var unit BillingUnit
	if err := lockForUpdate(tx).First(&unit, unitId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTeamUnavailable
		}
		return nil, err
	}
	var user User
	if err := tx.First(&user, actor).Error; err != nil {
		return nil, err
	}
	if user.Status != common.UserStatusEnabled || (unit.OwnerUserId != actor && user.Role < common.RoleAdminUser) {
		return nil, ErrTeamUnavailable
	}
	return &unit, nil
}

func InviteBillingUnitMember(unitId, actor int, recipient TeamInvitee) (*BillingUnitInvitation, error) {
	if (recipient.UserId <= 0 && (recipient.ProviderId <= 0 || recipient.Subject == "")) || recipient.Username == "" {
		return nil, ErrTeamInvalid
	}
	key := BillingInvitationRecipientKey(recipient.UserId, recipient.ProviderId, recipient.Subject)
	var result BillingUnitInvitation
	err := DB.Transaction(func(tx *gorm.DB) error {
		unit, err := teamManaged(tx, unitId, actor)
		if err != nil {
			return err
		}
		var payer User
		if err = tx.First(&payer, unit.PayerUserId).Error; err != nil {
			return err
		}
		if payer.Status != common.UserStatusEnabled {
			return ErrTeamUnavailable
		}
		if recipient.UserId > 0 {
			var user User
			if err = tx.First(&user, recipient.UserId).Error; err != nil {
				return err
			}
			if user.Status != common.UserStatusEnabled {
				return ErrTeamUnavailable
			}
			var count int64
			if err = tx.Model(&BillingUnit{}).Where("payer_user_id = ?", user.Id).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return ErrTeamUnavailable
			}
			if recipient.UserId == actor {
				return ErrTeamInvalid
			}
		}
		err = tx.Where("billing_unit_id = ? AND recipient_key = ?", unitId, key).First(&result).Error
		now := time.Now().Unix()
		if err == nil && result.Status == "pending" && result.ExpiresAt > now {
			return nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if result.Id == 0 {
			var count int64
			if err = tx.Model(&BillingUnitInvitation{}).Where("billing_unit_id = ? AND status = ? AND expires_at > ?", unitId, "pending", now).Count(&count).Error; err != nil {
				return err
			}
			if count >= 500 {
				return ErrTeamLimit
			}
		}
		result = BillingUnitInvitation{Id: result.Id, BillingUnitId: unitId, RecipientKey: key, RecipientUserId: recipient.UserId, ProviderId: recipient.ProviderId, Subject: recipient.Subject, Username: recipient.Username, DisplayName: recipient.DisplayName, InviterUserId: actor, Status: "pending", ExpiresAt: now + 7*86400, UpdatedAt: now}
		if result.Id == 0 {
			return tx.Create(&result).Error
		}
		return tx.Save(&result).Error
	})
	return &result, err
}

func IsBillingInvitationRecipient(tx *gorm.DB, invitation *BillingUnitInvitation, actor int) (bool, error) {
	if invitation.ProviderId == 0 {
		return invitation.RecipientUserId == actor, nil
	}
	var binding UserOAuthBinding
	err := tx.Where("user_id = ? AND provider_id = ?", actor, invitation.ProviderId).First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return binding.ProviderUserId == invitation.Subject, nil
}

// expectedUnit is the membership shown to the user at confirmation; changing it
// prevents concurrent/stale invitations silently replacing a newer decision.
func RespondBillingInvitation(id, actor int, action string, expectedUnit int) error {
	if action != "accept" && action != "reject" && action != "revoke" {
		return ErrTeamInvalid
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, actor).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled {
			return ErrTeamUnavailable
		}
		var seed BillingUnitInvitation
		if err := tx.First(&seed, id).Error; err != nil {
			return ErrTeamUnavailable
		}
		var unit BillingUnit
		if err := lockForUpdate(tx).First(&unit, seed.BillingUnitId).Error; err != nil {
			return ErrTeamUnavailable
		}
		var invitation BillingUnitInvitation
		if err := lockForUpdate(tx).First(&invitation, id).Error; err != nil {
			return err
		}
		if action == "revoke" {
			if unit.OwnerUserId != actor && user.Role < common.RoleAdminUser {
				return ErrTeamUnavailable
			}
		} else {
			allowed, err := IsBillingInvitationRecipient(tx, &invitation, actor)
			if err != nil {
				return err
			}
			if !allowed {
				return ErrTeamUnavailable
			}
		}
		target := map[string]string{"accept": "accepted", "reject": "rejected", "revoke": "revoked"}[action]
		if invitation.Status == target {
			return nil
		}
		if invitation.Status != "pending" {
			return ErrTeamConflict
		}
		if invitation.ExpiresAt <= time.Now().Unix() {
			return ErrTeamInvitationExpired
		}
		if action == "accept" {
			var payer User
			if err := tx.First(&payer, unit.PayerUserId).Error; err != nil {
				return err
			}
			if payer.Status != common.UserStatusEnabled {
				return ErrTeamUnavailable
			}
			var owner User
			if err := tx.First(&owner, unit.OwnerUserId).Error; err != nil {
				return err
			}
			if owner.Status != common.UserStatusEnabled {
				return ErrTeamUnavailable
			}
			var count int64
			if err := tx.Model(&BillingUnit{}).Where("payer_user_id = ?", actor).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return ErrTeamUnavailable
			}
			var member BillingUnitMember
			err := tx.First(&member, "user_id = ?", actor).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if member.BillingUnitId != expectedUnit {
				return ErrTeamConflict
			}
			if member.UserId == 0 {
				if err = tx.Create(&BillingUnitMember{UserId: actor, BillingUnitId: unit.Id}).Error; err != nil {
					return err
				}
			} else if err = tx.Model(&member).Update("billing_unit_id", unit.Id).Error; err != nil {
				return err
			}
		}
		return tx.Model(&invitation).Updates(map[string]any{"status": target, "updated_at": time.Now().Unix()}).Error
	})
}

func LeaveBillingUnit(actor, expectedUnit int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, actor).Error; err != nil {
			return err
		}
		var member BillingUnitMember
		err := tx.First(&member, "user_id = ?", actor).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if member.BillingUnitId != expectedUnit {
			return ErrTeamConflict
		}
		return tx.Delete(&member).Error
	})
}

func SelectOwnedBillingUnit(actor, unitId, expectedUnit int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, actor).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled {
			return ErrTeamUnavailable
		}
		var unit BillingUnit
		if err := lockForUpdate(tx).First(&unit, unitId).Error; err != nil {
			return ErrTeamUnavailable
		}
		if unit.OwnerUserId != actor {
			return ErrTeamUnavailable
		}
		var payer User
		if err := tx.First(&payer, unit.PayerUserId).Error; err != nil {
			return err
		}
		if payer.Status != common.UserStatusEnabled {
			return ErrTeamUnavailable
		}
		var member BillingUnitMember
		err := tx.First(&member, "user_id = ?", actor).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if member.BillingUnitId == unitId {
			return nil
		}
		if member.BillingUnitId != expectedUnit {
			return ErrTeamConflict
		}
		if member.UserId == 0 {
			return tx.Create(&BillingUnitMember{UserId: actor, BillingUnitId: unitId}).Error
		}
		return tx.Model(&member).Update("billing_unit_id", unitId).Error
	})
}

func UpdatePersonalBillingFallback(actor int, enabled bool) error {
	var value string
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, actor).Error; err != nil {
			return err
		}
		settings := user.GetSetting()
		settings.PersonalBillingFallback = enabled
		user.SetSetting(settings)
		value = user.Setting
		return tx.Model(&user).Update("setting", value).Error
	})
	if err != nil {
		return err
	}
	return updateUserSettingCache(actor, value)
}

// BillingInvitationRecipientKey preserves case-sensitive directory identity on all SQL collations.
func BillingInvitationRecipientKey(userId, providerId int, subject string) string {
	key := fmt.Sprintf("user:%d", userId)
	if providerId > 0 {
		key = fmt.Sprintf("provider:%d:%s", providerId, subject)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
}
