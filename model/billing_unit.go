package model

import (
	"errors"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"strings"
)

type BillingUnit struct {
	Id          int     `json:"id" gorm:"primaryKey"`
	Name        string  `json:"name" gorm:"type:varchar(128)"`
	OwnerUserId int     `json:"owner_user_id" gorm:"index;uniqueIndex:idx_team_creation,priority:1"`
	CreationKey *string `json:"-" gorm:"type:varchar(80);uniqueIndex:idx_team_creation,priority:2"`
	PayerUserId int     `json:"payer_user_id" gorm:"uniqueIndex"`
}
type BillingUnitMember struct {
	UserId        int `json:"user_id" gorm:"primaryKey;autoIncrement:false"`
	BillingUnitId int `json:"billing_unit_id" gorm:"index"`
}

// ResolveBillingUnit reads membership once. Callers retain the result for the
// complete request, including retries, settlement and refunds.
func ResolveBillingUnit(userId int) (*BillingUnit, error) {
	var payerCount int64
	if err := DB.Model(&BillingUnit{}).Where("payer_user_id = ?", userId).Count(&payerCount).Error; err != nil {
		return nil, err
	}
	if payerCount > 0 {
		return nil, errors.New("billing account cannot submit inference")
	}
	var member BillingUnitMember
	err := DB.First(&member, "user_id = ?", userId).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var unit BillingUnit
	if err = DB.First(&unit, member.BillingUnitId).Error; err != nil {
		return nil, err
	}
	return &unit, nil
}

// A dedicated native User provides the existing wallet/subscription machinery.
// Only a platform administrator can attach one; never attach a personal account.
func CreateBillingUnit(name string, ownerId, payerId int) (*BillingUnit, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 || ownerId <= 0 || payerId <= 0 || ownerId == payerId {
		return nil, errors.New("invalid billing unit")
	}
	unit := &BillingUnit{Name: name, OwnerUserId: ownerId, PayerUserId: payerId}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var payer, owner User
		if err := lockForUpdate(tx).First(&payer, payerId).Error; err != nil {
			return err
		}
		if err := tx.First(&owner, ownerId).Error; err != nil {
			return err
		}
		if payer.Role != common.RoleCommonUser || payer.Status != common.UserStatusEnabled || owner.Status != common.UserStatusEnabled {
			return errors.New("invalid billing account or owner")
		}
		var count int64
		if err := tx.Model(&Token{}).Where("user_id = ?", payerId).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errors.New("billing account already has tokens")
		}
		if err := tx.Model(&BillingUnitMember{}).Where("user_id = ?", payerId).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errors.New("billing account is a member")
		}
		if err := tx.Model(&UserOAuthBinding{}).Where("user_id = ?", payerId).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errors.New("billing account has external identity")
		}
		if payer.UsedQuota != 0 || payer.RequestCount != 0 {
			return errors.New("billing account already consumed")
		}
		return tx.Create(unit).Error
	})
	return unit, err
}

func SetBillingUnitMember(unitId, userId int, remove bool) error {
	if userId <= 0 {
		return errors.New("invalid member")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, userId).Error; err != nil {
			return err
		}
		var unit BillingUnit
		if err := tx.First(&unit, unitId).Error; err != nil {
			return err
		}
		if remove {
			return tx.Where("user_id = ? AND billing_unit_id = ?", userId, unitId).Delete(&BillingUnitMember{}).Error
		}
		if user.Status != common.UserStatusEnabled {
			return errors.New("member unavailable")
		}
		var count int64
		if err := tx.Model(&BillingUnit{}).Where("payer_user_id = ?", userId).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errors.New("billing account cannot be a member")
		}
		var member BillingUnitMember
		err := tx.First(&member, "user_id = ?", userId).Error
		if err == nil {
			if member.BillingUnitId == unitId {
				return nil
			}
			return errors.New("member already belongs to another unit")
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(&BillingUnitMember{UserId: userId, BillingUnitId: unitId}).Error
	})
}
