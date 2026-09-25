package model

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestChannelMetadataDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "metadata.db"))
			case "mysql":
				if os.Getenv("TEST_MYSQL_DSN") == "" {
					t.Skip("TEST_MYSQL_DSN required for real MySQL verification")
				}
				driver = mysql.Open(os.Getenv("TEST_MYSQL_DSN"))
			case "postgres":
				if os.Getenv("TEST_POSTGRES_DSN") == "" {
					t.Skip("TEST_POSTGRES_DSN required for real PostgreSQL verification")
				}
				driver = postgres.New(postgres.Config{DSN: os.Getenv("TEST_POSTGRES_DSN"), PreferSimpleProtocol: true})
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "metadata_test_"}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			previousDB, previousType := DB, common.MainDatabaseType()
			DB = db
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			initCol()
			t.Cleanup(func() {
				DB = previousDB
				common.SetMainDatabaseType(previousType)
				initCol()
				require.NoError(t, sqlDB.Close())
			})
			var version string
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)
			for _, mode := range []string{"fresh", "upgrade"} {
				t.Run(mode, func(t *testing.T) {
					t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&Ability{}, &Channel{}, &Provider{})) })
					legacy := Channel{Name: "legacy", Type: 1, Key: "key-a\nkey-b", Models: "model-a", Group: "default", Status: common.ChannelStatusManuallyDisabled, ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyMode: constant.MultiKeyModePolling}}
					if mode == "upgrade" {
						// Reconstruct the preceding Provider-enabled schema, not a schema
						// missing Provider itself. All existing columns/indexes remain.
						require.NoError(t, db.AutoMigrate(&Provider{}, &Channel{}, &Ability{}))
						provider, err := defaultProvider(db)
						require.NoError(t, err)
						legacy.ProviderID = provider.ID
						fields := []string{"PublicRatio", "IsDegraded", "IsOnline", "TTFTMs", "LastCheckedAt"}
						for _, field := range fields {
							require.NoError(t, db.Migrator().DropColumn(&Channel{}, field))
						}
						require.NoError(t, db.Omit(fields...).Create(&legacy).Error)
					}
					for range 2 {
						require.NoError(t, db.AutoMigrate(&Provider{}, &Channel{}, &Ability{}))
						require.NoError(t, migrateChannelProviders(db))
					}
					if mode == "fresh" {
						require.NoError(t, legacy.Insert())
					}
					loaded, err := GetChannelById(legacy.Id, true)
					require.NoError(t, err)
					require.NotNil(t, loaded.PublicRatio)
					assert.Equal(t, 1.0, *loaded.PublicRatio)
					assert.Nil(t, loaded.IsDegraded)
					assert.Nil(t, loaded.IsOnline)
					assert.Nil(t, loaded.TTFTMs)
					assert.Nil(t, loaded.LastCheckedAt)
					assert.Equal(t, legacy.Key, loaded.Key)
					assert.Equal(t, legacy.ChannelInfo, loaded.ChannelInfo)
					assert.True(t, db.Migrator().HasIndex(&Channel{}, "ProviderID"))

					ratio, degraded, online, ttft, checked := 0.0125, true, false, int64(0), int64(1700000123)
					patch := Channel{Id: legacy.Id, PublicRatio: &ratio, IsDegraded: &degraded, IsOnline: &online, TTFTMs: &ttft, LastCheckedAt: &checked}
					require.NoError(t, patch.Update())
					rename := Channel{Id: legacy.Id, Name: "renamed"}
					require.NoError(t, rename.Update())
					assert.Equal(t, &ratio, rename.PublicRatio)
					assert.Equal(t, &degraded, rename.IsDegraded)
					assert.Equal(t, &online, rename.IsOnline)
					assert.Equal(t, &ttft, rename.TTFTMs)
					assert.Equal(t, &checked, rename.LastCheckedAt)
					assert.Equal(t, common.ChannelStatusManuallyDisabled, rename.Status)
					assert.Equal(t, legacy.Key, rename.Key)
					assert.Equal(t, 1.0, *rename.UpstreamRatio)
					// False and zero must persist, independently of operational status.
					degraded, online = false, true
					falsePatch := Channel{Id: legacy.Id, IsDegraded: &degraded, IsOnline: &online}
					require.NoError(t, falsePatch.Update())
					assert.Equal(t, &degraded, falsePatch.IsDegraded)
					assert.Equal(t, &online, falsePatch.IsOnline)
					assert.Equal(t, common.ChannelStatusManuallyDisabled, falsePatch.Status)
					falsePatch.UpdateResponseTime(900)
					status := Channel{Id: legacy.Id, Status: common.ChannelStatusEnabled}
					require.NoError(t, status.saveStatusState())
					loaded, err = GetChannelById(legacy.Id, true)
					require.NoError(t, err)
					assert.Equal(t, &ttft, loaded.TTFTMs)
					assert.Equal(t, &checked, loaded.LastCheckedAt)

					clear := Channel{Id: legacy.Id}
					require.NoError(t, clear.Update("is_degraded", "is_online", "ttft_ms", "last_checked_at"))
					assert.Nil(t, clear.IsDegraded)
					assert.Nil(t, clear.IsOnline)
					assert.Nil(t, clear.TTFTMs)
					assert.Nil(t, clear.LastCheckedAt)
					assert.Equal(t, &ratio, clear.PublicRatio)
					assert.Error(t, clear.Update("key"), "field mask cannot write credentials")
					for _, invalid := range []float64{0, -1, math.NaN(), math.Inf(1)} {
						bad := Channel{Id: legacy.Id, PublicRatio: &invalid}
						assert.Error(t, bad.Update())
						assert.Error(t, bad.Save())
						bad.Id = 0
						assert.Error(t, bad.Insert())
					}
					negative := int64(-1)
					invalidTTFT := Channel{Id: legacy.Id, TTFTMs: &negative}
					assert.Error(t, invalidTTFT.Update())
					for _, invalid := range []int64{-1, 253402300800} {
						bad := Channel{Id: legacy.Id, LastCheckedAt: &invalid}
						assert.Error(t, bad.Update())
					}
					batch := []Channel{{Name: "batch-valid", Key: "key-c"}, {Name: "batch-invalid", Key: "key-d", TTFTMs: &negative}}
					assert.Error(t, BatchInsertChannels(batch))
					var count int64
					require.NoError(t, db.Model(&Channel{}).Where("name = ?", "batch-valid").Count(&count).Error)
					assert.Zero(t, count)
					require.NoError(t, BatchInsertChannels(batch[:1]))
					assert.Equal(t, 1.0, *batch[0].PublicRatio)
					saved := Channel{Name: "saved", Key: "key-e", IsOnline: &online, TTFTMs: &ttft}
					require.NoError(t, saved.Save())
					assert.Equal(t, 1.0, *saved.PublicRatio)
					// Repeat migration with non-default data; observations must survive.
					require.NoError(t, db.AutoMigrate(&Provider{}, &Channel{}, &Ability{}))
					loaded, err = GetChannelById(saved.Id, true)
					require.NoError(t, err)
					assert.Equal(t, &online, loaded.IsOnline)
					assert.Equal(t, &ttft, loaded.TTFTMs)
				})
			}
		})
	}
}
