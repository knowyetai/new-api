package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type MigrationIdentityFields struct {
	ID        int    `gorm:"primaryKey"`
	Name      string `gorm:"size:64;unique"`
	Reference string `gorm:"size:64;uniqueIndex"`
	Provider  string `gorm:"size:32;uniqueIndex:,composite:provider_subject"`
	Subject   string `gorm:"size:64;uniqueIndex:,composite:provider_subject"`
}

type migrationIdentityV1 struct {
	MigrationIdentityFields
	Digest string `gorm:"type:char(32)"`
}

type migrationIdentityV2 struct {
	MigrationIdentityFields
	Digest string `gorm:"type:char(64)"`
	Note   string `gorm:"size:128"`
}

type migrationConstraintV1 struct {
	ID   int    `gorm:"primaryKey"`
	Name string `gorm:"size:64"`
}

type migrationConstraintV2 struct {
	ID   int    `gorm:"primaryKey"`
	Name string `gorm:"size:64;unique"`
}

type migrationDecimalV1 struct {
	ID    int     `gorm:"primaryKey"`
	Price float64 `gorm:"type:decimal(10,6);default:0"`
}

type migrationDecimalV2 struct {
	ID    int     `gorm:"primaryKey"`
	Price float64 `gorm:"type:decimal(12,6);not null;default:0"`
}

type migrationDecimalV3 struct {
	ID    int     `gorm:"primaryKey"`
	Price float64 `gorm:"type:decimal(12,6);not null;default:1.25"`
}

type migrationTopUpV1 struct {
	Id              int `gorm:"primaryKey"`
	UserId          int `gorm:"index"`
	Amount          int64
	Money           float64
	TradeNo         string `gorm:"unique;type:varchar(255);index"`
	PaymentMethod   string `gorm:"type:varchar(50)"`
	PaymentProvider string `gorm:"type:varchar(50);default:''"`
	CreateTime      int64
	CompleteTime    int64
	Status          string
	BillingUnitId   int `gorm:"index;default:0"`
	OperatorUserId  int `gorm:"index;default:0"`
}

func TestMigrationSchemaStability(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var dsn string
			switch dialect {
			case "sqlite":
				dsn = "local"
				previousPath := common.SQLitePath
				common.SQLitePath = filepath.Join(t.TempDir(), "migration.db")
				t.Cleanup(func() { common.SQLitePath = previousPath })
			case "mysql":
				dsn = os.Getenv("TEST_MYSQL_DSN")
			case "postgres":
				dsn = os.Getenv("TEST_POSTGRES_DSN")
			}
			if dsn == "" {
				t.Skip("test database DSN is not configured")
			}
			t.Setenv("MIGRATION_TEST_DSN", dsn)
			db, _, err := chooseDB("MIGRATION_TEST_DSN", false)
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			recorder := &migrationSQLRecorder{}
			db = db.Session(&gorm.Session{Logger: recorder})

			t.Run("identity_and_indexes", func(t *testing.T) {
				const table = "migration_identity_test"
				t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
				require.NoError(t, db.Table(table).AutoMigrate(&migrationIdentityV1{}))
				row := migrationIdentityV1{
					MigrationIdentityFields: MigrationIdentityFields{ID: 1, Name: "root", Reference: "token-reference", Provider: "oidc", Subject: "subject"},
					Digest:                  "old-digest",
				}
				require.NoError(t, db.Table(table).Create(&row).Error)
				recorder.reset()
				require.NoError(t, db.Table(table).AutoMigrate(&migrationIdentityV1{}))
				assert.Empty(t, recorder.schemaMutations())

				require.NoError(t, db.Table(table).AutoMigrate(&migrationIdentityV2{}))
				columns, err := db.Table(table).Migrator().ColumnTypes(&migrationIdentityV2{})
				require.NoError(t, err)
				for _, column := range columns {
					if column.Name() == "digest" {
						length, ok := column.Length()
						require.True(t, ok)
						assert.EqualValues(t, 64, length)
					}
				}
				assert.True(t, db.Table(table).Migrator().HasColumn(&migrationIdentityV2{}, "note"))
				recorder.reset()
				require.NoError(t, db.Table(table).AutoMigrate(&migrationIdentityV2{}))
				assert.Empty(t, recorder.schemaMutations())
				var saved migrationIdentityV2
				require.NoError(t, db.Table(table).First(&saved, 1).Error)
				assert.Equal(t, row.MigrationIdentityFields, saved.MigrationIdentityFields)
				expectedDigest := row.Digest
				if dialect == "postgres" {
					expectedDigest += strings.Repeat(" ", 64-len(row.Digest))
				}
				assert.Equal(t, expectedDigest, saved.Digest)
				for _, duplicate := range []migrationIdentityV2{
					{MigrationIdentityFields: MigrationIdentityFields{Name: "root", Reference: "other-1", Provider: "other", Subject: "1"}},
					{MigrationIdentityFields: MigrationIdentityFields{Name: "other-2", Reference: "token-reference", Provider: "other", Subject: "2"}},
					{MigrationIdentityFields: MigrationIdentityFields{Name: "other-3", Reference: "other-3", Provider: "oidc", Subject: "subject"}},
				} {
					assert.Error(t, db.Table(table).Create(&duplicate).Error)
				}
			})

			t.Run("unique_constraint_changes", func(t *testing.T) {
				const table = "migration_constraint_test"
				t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
				require.NoError(t, db.Table(table).AutoMigrate(&migrationConstraintV1{}))
				require.NoError(t, db.Table(table).Create(&migrationConstraintV1{Name: "existing"}).Error)
				require.NoError(t, db.Table(table).AutoMigrate(&migrationConstraintV2{}))
				assert.Error(t, db.Table(table).Create(&migrationConstraintV2{Name: "existing"}).Error)
				recorder.reset()
				require.NoError(t, db.Table(table).AutoMigrate(&migrationConstraintV2{}))
				assert.Empty(t, recorder.schemaMutations())
				require.NoError(t, db.Table(table).AutoMigrate(&migrationConstraintV1{}))
				require.NoError(t, db.Table(table).Create(&migrationConstraintV1{Name: "existing"}).Error)
			})

			t.Run("topup_payment_tracking_columns", func(t *testing.T) {
				const table = "migration_topup_test"
				t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
				require.NoError(t, db.Table(table).AutoMigrate(&migrationTopUpV1{}))
				require.NoError(t, db.Table(table).Create(&migrationTopUpV1{
					Id:              1,
					UserId:          7,
					Amount:          10,
					Money:           73.05,
					TradeNo:         "legacy-topup",
					PaymentMethod:   "alipay",
					PaymentProvider: "epay",
					Status:          common.TopUpStatusSuccess,
				}).Error)

				require.NoError(t, db.Table(table).AutoMigrate(&TopUp{}))
				recorder.reset()
				require.NoError(t, db.Table(table).AutoMigrate(&TopUp{}))
				assert.Empty(t, recorder.schemaMutations())

				assert.False(t, db.Table(table).Migrator().HasColumn(&TopUp{}, "money_cents"))
				assert.True(t, db.Table(table).Migrator().HasColumn(&TopUp{}, "amount_unit"))
				assert.True(t, db.Table(table).Migrator().HasColumn(&TopUp{}, "provider_trade_no"))
				assert.True(t, db.Table(table).Migrator().HasColumn(&TopUp{}, "lianlian_request_id"))
				assert.True(t, db.Table(table).Migrator().HasIndex(&TopUp{}, TopUpProviderStatusCreateTimeIndex))
				assert.True(t, db.Table(table).Migrator().HasIndex(&TopUp{}, topUpUserLianlianRequestIDIndex))

				var legacy TopUp
				require.NoError(t, db.Table(table).First(&legacy, 1).Error)
				assert.Empty(t, legacy.AmountUnit)
				assert.Empty(t, legacy.ProviderTradeNo)
				assert.InDelta(t, 73.05, legacy.Money, 1e-9)

				requestID := "request-1"
				tracked := TopUp{
					Id:                2,
					UserId:            7,
					Amount:            10,
					AmountUnit:        AmountUnitUSD,
					Money:             73.05,
					ProviderTradeNo:   "LL20261021001",
					LianlianRequestID: &requestID,
					TradeNo:           "tracked-topup",
					PaymentMethod:     "alipay",
					PaymentProvider:   "lianlian",
					Status:            common.TopUpStatusPending,
				}
				require.NoError(t, db.Table(table).Create(&tracked).Error)
				var saved TopUp
				require.NoError(t, db.Table(table).Where("trade_no = ?", tracked.TradeNo).First(&saved).Error)
				assert.Equal(t, AmountUnitUSD, saved.AmountUnit)
				assert.Equal(t, tracked.ProviderTradeNo, saved.ProviderTradeNo)
				require.NotNil(t, saved.LianlianRequestID)
				assert.Equal(t, requestID, *saved.LianlianRequestID)

				const freshTable = "migration_topup_fresh_test"
				t.Cleanup(func() { _ = db.Migrator().DropTable(freshTable) })
				require.NoError(t, db.Migrator().DropTable(table))
				require.NoError(t, db.Table(freshTable).AutoMigrate(&TopUp{}))
				recorder.reset()
				require.NoError(t, db.Table(freshTable).AutoMigrate(&TopUp{}))
				assert.Empty(t, recorder.schemaMutations())
				assert.False(t, db.Table(freshTable).Migrator().HasColumn(&TopUp{}, "money_cents"))
				assert.True(t, db.Table(freshTable).Migrator().HasIndex(&TopUp{}, TopUpProviderStatusCreateTimeIndex))
				assert.True(t, db.Table(freshTable).Migrator().HasIndex(&TopUp{}, topUpUserLianlianRequestIDIndex))
				require.NoError(t, db.Table(freshTable).Create(&tracked).Error)
				var freshSaved TopUp
				require.NoError(t, db.Table(freshTable).Where("trade_no = ?", tracked.TradeNo).First(&freshSaved).Error)
				assert.Equal(t, tracked.Money, freshSaved.Money)
				assert.Equal(t, tracked.AmountUnit, freshSaved.AmountUnit)
				assert.Equal(t, tracked.ProviderTradeNo, freshSaved.ProviderTradeNo)
				require.NotNil(t, freshSaved.LianlianRequestID)
				assert.Equal(t, requestID, *freshSaved.LianlianRequestID)

				const idempotencyTable = "migration_topup_idempotency_test"
				t.Cleanup(func() { _ = db.Migrator().DropTable(idempotencyTable) })
				require.NoError(t, db.Migrator().DropTable(freshTable))
				require.NoError(t, db.Table(idempotencyTable).AutoMigrate(&TopUp{}))
				idempotencyKey := "same-request"
				require.NoError(t, db.Table(idempotencyTable).Create(&TopUp{
					UserId:            7,
					Amount:            10,
					LianlianRequestID: &idempotencyKey,
					TradeNo:           "idempotency-user-7",
					PaymentProvider:   PaymentProviderLianlian,
				}).Error)
				assert.Error(t, db.Table(idempotencyTable).Create(&TopUp{
					UserId:            7,
					Amount:            20,
					LianlianRequestID: &idempotencyKey,
					TradeNo:           "idempotency-user-7-duplicate",
					PaymentProvider:   PaymentProviderLianlian,
				}).Error)
				assert.NoError(t, db.Table(idempotencyTable).Create(&TopUp{
					UserId:            8,
					Amount:            10,
					LianlianRequestID: &idempotencyKey,
					TradeNo:           "idempotency-user-8",
					PaymentProvider:   PaymentProviderLianlian,
				}).Error)
			})

			t.Run("topup_reconciliation_statuses", func(t *testing.T) {
				const table = "top_ups"
				t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
				require.NoError(t, db.Table(table).AutoMigrate(&TopUp{}))
				require.NoError(t, db.Table(table).Create([]TopUp{
					{
						Id:              1,
						UserId:          7,
						Amount:          100,
						AmountUnit:      AmountUnitCNYCent,
						Money:           100,
						TradeNo:         "reconcile-pending",
						PaymentProvider: PaymentProviderLianlian,
						CreateTime:      100,
						Status:          common.TopUpStatusPending,
					},
					{
						Id:              2,
						UserId:          7,
						Amount:          100,
						AmountUnit:      AmountUnitCNYCent,
						Money:           100,
						TradeNo:         "reconcile-cancelled",
						PaymentProvider: PaymentProviderLianlian,
						CreateTime:      200,
						Status:          common.TopUpStatusCancelled,
					},
					{
						Id:              3,
						UserId:          7,
						Amount:          100,
						AmountUnit:      AmountUnitCNYCent,
						Money:           100,
						TradeNo:         "reconcile-expired",
						PaymentProvider: PaymentProviderLianlian,
						CreateTime:      300,
						Status:          common.TopUpStatusExpired,
					},
					{
						Id:              4,
						UserId:          7,
						Amount:          100,
						AmountUnit:      AmountUnitCNYCent,
						Money:           100,
						TradeNo:         "reconcile-create-unknown",
						PaymentProvider: PaymentProviderLianlian,
						CreateTime:      250,
						Status:          common.TopUpStatusCreateUnknown,
					},
				}).Error)

				oldDB := DB
				oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
				DB = db
				databaseType := map[string]common.DatabaseType{
					"sqlite":   common.DatabaseTypeSQLite,
					"mysql":    common.DatabaseTypeMySQL,
					"postgres": common.DatabaseTypePostgreSQL,
				}[dialect]
				common.SetDatabaseTypes(databaseType, oldLogType)
				t.Cleanup(func() {
					DB = oldDB
					common.SetDatabaseTypes(oldMainType, oldLogType)
				})

				topUps, err := FindPendingTopUpsByProvider(PaymentProviderLianlian, 250, 0, 10)
				require.NoError(t, err)
				require.Len(t, topUps, 3)
				assert.Equal(t, []string{"reconcile-pending", "reconcile-cancelled", "reconcile-create-unknown"}, []string{
					topUps[0].TradeNo,
					topUps[1].TradeNo,
					topUps[2].TradeNo,
				})

				hasOpen, err := HasPendingTopUpsByProvider(PaymentProviderLianlian, 250, 0)
				require.NoError(t, err)
				assert.True(t, hasOpen)

				require.NoError(t, UpdatePendingTopUpStatus(
					"reconcile-pending",
					PaymentProviderLianlian,
					common.TopUpStatusCancelled,
				))
				updated, err := TransitionLianlianTopUpStatus(
					"reconcile-pending",
					common.TopUpStatusExpired,
				)
				require.NoError(t, err)
				assert.True(t, updated)
				var transitioned TopUp
				require.NoError(t, db.Where("trade_no = ?", "reconcile-pending").First(&transitioned).Error)
				assert.Equal(t, common.TopUpStatusExpired, transitioned.Status)
			})

			t.Run("lianlian_config_callback_window", func(t *testing.T) {
				const table = "top_ups"
				t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
				require.NoError(t, db.Table(table).AutoMigrate(&TopUp{}))

				now := common.GetTimestamp()
				require.NoError(t, db.Table(table).Create([]TopUp{
					{
						Id:              1,
						UserId:          7,
						Amount:          100,
						AmountUnit:      AmountUnitCNYCent,
						Money:           100,
						TradeNo:         "config-lock-recent-expired",
						PaymentProvider: PaymentProviderLianlian,
						CreateTime:      now - lianlianCallbackConfigLockSeconds + 60,
						Status:          common.TopUpStatusExpired,
					},
					{
						Id:              2,
						UserId:          7,
						Amount:          100,
						AmountUnit:      AmountUnitCNYCent,
						Money:           100,
						TradeNo:         "config-lock-old-expired",
						PaymentProvider: PaymentProviderLianlian,
						CreateTime:      now - lianlianCallbackConfigLockSeconds - 60,
						Status:          common.TopUpStatusExpired,
					},
				}).Error)

				locked, err := hasLianlianOrdersLockingConfig(db)
				require.NoError(t, err)
				assert.True(t, locked)

				require.NoError(t, db.Table(table).Where("id = ?", 1).Delete(&TopUp{}).Error)
				locked, err = hasLianlianOrdersLockingConfig(db)
				require.NoError(t, err)
				assert.False(t, locked)
			})

			if dialect == "mysql" {
				t.Run("decimal_default_and_real_changes", func(t *testing.T) {
					const table = "migration_decimal_test"
					t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
					require.NoError(t, db.Table(table).AutoMigrate(&migrationDecimalV1{}))
					require.NoError(t, db.Table(table).Create(&migrationDecimalV1{ID: 1, Price: 12.345678}).Error)
					for _, target := range []any{&migrationDecimalV1{}, &migrationDecimalV2{}, &migrationDecimalV3{}} {
						require.NoError(t, db.Table(table).AutoMigrate(target))
						recorder.reset()
						require.NoError(t, db.Table(table).AutoMigrate(target))
						assert.Empty(t, recorder.schemaMutations())
					}
					columns, err := db.Table(table).Migrator().ColumnTypes(&migrationDecimalV3{})
					require.NoError(t, err)
					for _, column := range columns {
						if column.Name() == "price" {
							precision, scale, ok := column.DecimalSize()
							require.True(t, ok)
							assert.EqualValues(t, 12, precision)
							assert.EqualValues(t, 6, scale)
							nullable, ok := column.Nullable()
							require.True(t, ok)
							assert.False(t, nullable)
						}
					}
					require.NoError(t, db.Table(table).Create(&map[string]any{"id": 2}).Error)
					var prices []float64
					require.NoError(t, db.Table(table).Order("id").Pluck("price", &prices).Error)
					assert.Equal(t, []float64{12.345678, 1.25}, prices)
				})
			}
		})
	}
}
