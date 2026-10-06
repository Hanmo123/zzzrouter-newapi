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

func TestProviderDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "providers.db"))
			case "mysql":
				if os.Getenv("TEST_MYSQL_DSN") == "" {
					t.Skip("TEST_MYSQL_DSN not configured; real MySQL verification required")
				}
				driver = mysql.Open(os.Getenv("TEST_MYSQL_DSN"))
			case "postgres":
				if os.Getenv("TEST_POSTGRES_DSN") == "" {
					t.Skip("TEST_POSTGRES_DSN not configured; real PostgreSQL verification required")
				}
				driver = postgres.New(postgres.Config{DSN: os.Getenv("TEST_POSTGRES_DSN"), PreferSimpleProtocol: true})
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "provider_test_"}})
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

			for _, upgrade := range []bool{false, true} {
				name := "fresh"
				if upgrade {
					name = "upgrade"
				}
				t.Run(name, func(t *testing.T) {
					t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&Ability{}, &Channel{}, &Provider{})) })
					legacy := Channel{Name: "legacy multi-key", Key: "key-a\nkey-b", Models: "model-a,model-b", Group: "default", ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyMode: constant.MultiKeyModeRandom}}
					if upgrade {
						// Reconstruct the pre-feature Channel schema, preserving all
						// other columns and indexes from the repository's base revision.
						require.NoError(t, db.AutoMigrate(&Channel{}))
						require.NoError(t, db.Migrator().DropColumn(&Channel{}, "ProviderID"))
						require.NoError(t, db.Migrator().DropColumn(&Channel{}, "UpstreamRatio"))
						require.NoError(t, db.Omit("ProviderID", "UpstreamRatio").Create(&legacy).Error)
					}
					for range 2 {
						require.NoError(t, db.AutoMigrate(&Provider{}, &Channel{}, &Ability{}))
						require.NoError(t, migrateChannelProviders(db))
					}
					providers, total, err := ListProviders("", 0, 20)
					require.NoError(t, err)
					require.EqualValues(t, 1, total)
					defaultID := providers[0].ID
					assert.Error(t, DeleteProvider(defaultID))
					assert.True(t, db.Migrator().HasIndex(&Channel{}, "ProviderID"))
					if upgrade {
						loaded, err := GetChannelById(legacy.Id, true)
						require.NoError(t, err)
						assert.Equal(t, defaultID, loaded.ProviderID)
						assert.Equal(t, 1.0, loaded.ModelConfig("model-a").Discount())
						assert.Equal(t, legacy.Key, loaded.Key)
						assert.Equal(t, legacy.Models, loaded.Models)
						assert.Equal(t, legacy.ChannelInfo, loaded.ChannelInfo)
					}

					provider := Provider{Name: "Reseller A", Description: "first"}
					require.NoError(t, provider.Save())
					other := Provider{Name: "Reseller B"}
					require.NoError(t, other.Save())
					ratio := 0.8
					channel := legacy
					channel.Id, channel.ProviderID = 0, provider.ID
					channel.ModelConfigs = ChannelModelConfigs{"model-a": {UpstreamDiscount: &ratio}}
					require.NoError(t, channel.Insert())
					assert.Error(t, DeleteProvider(provider.ID), "referenced provider must not be deleted")
					patch := Channel{Id: channel.Id, Name: "renamed"}
					require.NoError(t, patch.Update())
					assert.Equal(t, provider.ID, patch.ProviderID)
					assert.Equal(t, ratio, patch.ModelConfig("model-a").Discount())
					assert.Equal(t, channel.Key, patch.Key)
					// Reparenting does not change credentials or multi-key state.
					patch.ProviderID = other.ID
					require.NoError(t, patch.Update())
					assert.Equal(t, channel.GetKeys(), patch.GetKeys())
					assert.True(t, patch.ChannelInfo.IsMultiKey)
					require.NoError(t, DeleteProvider(provider.ID))
					patch.ProviderID = provider.ID
					assert.Error(t, patch.Update(), "deleted provider cannot be assigned")
					patch.ProviderID = -1
					assert.Error(t, patch.Update())
					patch.ProviderID = other.ID
					for _, invalid := range []float64{1.1, -1, math.NaN(), math.Inf(1)} {
						patch.ModelConfigs = ChannelModelConfigs{"model-a": {UpstreamDiscount: &invalid}}
						assert.Error(t, patch.Update())
					}
					loaded, err := GetChannelById(channel.Id, true)
					require.NoError(t, err)
					assert.Equal(t, other.ID, loaded.ProviderID)
					assert.Equal(t, ratio, loaded.ModelConfig("model-a").Discount())

					batch := []Channel{{Name: "single", Key: "key-c", Models: "model-a", Group: "default"}, {Name: "invalid", Key: "key-d", ProviderID: provider.ID}}
					assert.Error(t, BatchInsertChannels(batch))
					var count int64
					require.NoError(t, db.Model(&Channel{}).Where("name = ?", "single").Count(&count).Error)
					assert.Zero(t, count, "invalid batch must not partially insert channels")
					require.NoError(t, BatchInsertChannels(batch[:1]))
					assert.Equal(t, defaultID, batch[0].ProviderID)
					assert.Equal(t, 1.0, batch[0].ModelConfig("model-a").Discount())
					saved := Channel{Name: "internal-save", Key: "key-e"}
					require.NoError(t, saved.Save())
					assert.Equal(t, defaultID, saved.ProviderID)
					assert.Equal(t, 1.0, saved.ModelConfig("model-a").Discount())
					saved.ProviderID = provider.ID
					assert.Error(t, saved.Save(), "full-record saves also reject deleted providers")

					other.Name, other.Description = "Updated reseller", ""
					require.NoError(t, other.Save())
					providers, total, err = ListProviders("Updated", 0, 1)
					require.NoError(t, err)
					require.EqualValues(t, 1, total)
					assert.EqualValues(t, 1, providers[0].ChannelCount)
					assert.Empty(t, providers[0].Description)
					var historical Provider
					require.NoError(t, db.Unscoped().First(&historical, provider.ID).Error)
					assert.True(t, historical.DeletedAt.Valid, "historical identity is retained")
				})
			}
		})
	}
}
