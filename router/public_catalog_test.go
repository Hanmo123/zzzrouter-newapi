package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type publicCatalogReply struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    struct {
		Page     int              `json:"page"`
		PageSize int              `json:"page_size"`
		Total    int              `json:"total"`
		Items    []map[string]any `json:"items"`
	} `json:"data"`
}

func requestPublicCatalog(t *testing.T, engine *gin.Engine, path string) (*httptest.ResponseRecorder, publicCatalogReply) {
	t.Helper()
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	var reply publicCatalogReply
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &reply), recorder.Body.String())
	// Canary values come from private fields on the actual database records.
	for _, secret := range []string{"upstream_discount", "billing_expression", "92837.625", "private-key-canary", "private-provider-description", "private-code", "private.example", "private-setting-canary", "private-remark-canary", "private-group"} {
		assert.NotContains(t, recorder.Body.String(), secret)
	}
	return recorder, reply
}

func TestHomepageBoardConfigValidation(t *testing.T) {
	for _, raw := range []string{
		`{"models":["model-a"],"default_model":"model-b"}`,
		`{"models":["model-a","model-a"],"default_model":"model-a"}`,
		`{"models":["model-a,model-b"],"default_model":"model-a,model-b"}`,
		`{"models":[],"default_model":""}`,
		`not json`,
	} {
		_, err := setting.ParseHomepageBoardConfig(raw)
		require.Error(t, err, raw)
	}
	config, err := setting.ParseHomepageBoardConfig(`{"models":[],"default_model":"gpt-6-astra"}`)
	require.NoError(t, err)
	assert.Empty(t, config.Models)
}

func TestPublicCatalogDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "catalog.db"))
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
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "catalog_test_"}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			previousDB, previousType := model.DB, common.MainDatabaseType()
			previousLogDB, previousLogType := model.LOG_DB, common.LogDatabaseType()
			model.DB, model.LOG_DB = db, db
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			common.SetLogDatabaseType(common.DatabaseType(dialect))
			t.Cleanup(func() {
				require.NoError(t, db.Migrator().DropTable(&model.Log{}, &model.PublicDailyUsage{}, &model.AuditLog{}, &model.User{}, &model.ChannelDegradationSample{}, &model.Channel{}, &model.Provider{}))
				model.DB, model.LOG_DB = previousDB, previousLogDB
				common.SetMainDatabaseType(previousType)
				common.SetLogDatabaseType(previousLogType)
				require.NoError(t, sqlDB.Close())
			})
			var version string
			versionQuery := "SELECT version()"
			if dialect == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("database version: %s", version)
			require.NoError(t, db.AutoMigrate(&model.Provider{}, &model.Channel{}, &model.ChannelDegradationSample{}, &model.PublicDailyUsage{}, &model.Log{}, &model.User{}, &model.AuditLog{}))
			providers := []model.Provider{
				{ID: 101, Code: "private-code-a", Name: "Provider A", Description: "private-provider-description"},
				{ID: 102, Code: "private-code-b", Name: "Provider B", Description: "private-provider-description"},
				{ID: 103, Code: "private-code-empty", Name: "Empty provider"},
				{ID: 104, Code: "private-code-deleted", Name: "Deleted provider"},
			}
			require.NoError(t, db.Create(&providers).Error)
			yes, no := true, false
			zero, fast, slow, checked := int64(0), int64(100), int64(500), int64(1700000123)
			privateRatio := 92837.625
			baseURL, channelSetting, remark := "https://private.example", `{"internal":"private-setting-canary"}`, "private-remark-canary"
			for _, row := range []struct {
				id, provider, status int
				ratio                float64
				online, degraded     *bool
				ttft                 *int64
			}{
				{1, 101, 1, 0.5, &yes, &no, &fast},
				{2, 101, 1, 1, &yes, &no, &zero},
				{3, 102, 1, 1, &yes, &no, &fast},
				{4, 101, 1, 2, &yes, &no, nil},
				{5, 101, 1, 3, &yes, &no, &slow},
				{6, 101, 1, 0.1, &yes, &yes, &fast},
				{7, 101, 1, 0.2, &no, &no, &fast},
				{8, 101, 1, 0.3, nil, &no, nil},
				{9, 101, 1, 0.4, &yes, nil, nil},
				{10, 101, 1, 0.6, nil, nil, nil},
				{11, 101, 2, 0.01, &yes, &no, &fast},
				{12, 101, 3, 0.02, &yes, &no, &fast},
				{13, 104, 1, 0.03, &yes, &no, &fast},
				{14, 999999, 1, 0.04, &yes, &no, &fast},
			} {
				channel := model.Channel{Id: row.id, ProviderID: row.provider, Status: row.status, Type: 1, Name: fmt.Sprintf("Channel %d", row.id), Models: "model-a,model-b", Group: "private-group", Key: "private-key-canary", ModelConfigs: model.ChannelModelConfigs{"model-a": {PublicRatio: &row.ratio, UpstreamDiscount: &privateRatio, IsOnline: row.online, IsDegraded: row.degraded, TTFTMs: row.ttft, LastCheckedAt: &checked}, "model-b": {PublicRatio: &row.ratio, UpstreamDiscount: &privateRatio, IsOnline: row.online, IsDegraded: row.degraded, TTFTMs: row.ttft, LastCheckedAt: &checked}}, BaseURL: &baseURL, Setting: &channelSetting, Remark: &remark}
				// Deliberately include legacy orphan/soft-deleted-provider rows to
				// verify public visibility, bypassing normal association validation.
				require.NoError(t, db.Create(&channel).Error)
			}
			require.NoError(t, db.Delete(&providers[3]).Error)
			gin.SetMode(gin.TestMode)
			engine := gin.New()
			api := engine.Group("/api")
			registerPublicCatalogRoutes(api)
			registerProviderRoutes(api)
			registerChannelRoutes(api)

			t.Run("platform statistics and daily tokens come from current public facts and the separate log store", func(t *testing.T) {
				now := time.Now().UTC()
				stats, err := model.GetPublicPlatformStatistics(context.Background(), now)
				require.NoError(t, err)
				assert.EqualValues(t, 2, stats.Providers)
				assert.EqualValues(t, 10, stats.Channels)
				assert.EqualValues(t, 2, stats.Models)
				assert.Zero(t, stats.Evaluations24h)
				assert.Nil(t, stats.Latest)
				var visible, disabled model.Channel
				require.NoError(t, db.First(&visible, "id = ?", 1).Error)
				require.NoError(t, db.First(&disabled, "id = ?", 11).Error)
				zero := int64(0)
				for i, instance := range []string{*visible.InstanceID, *disabled.InstanceID, "stale-copy-instance", *visible.InstanceID} {
					provider := 101
					started := now.UnixMilli() - 1000
					if i == 3 {
						started = now.Add(24 * time.Hour).UnixMilli()
					}
					require.NoError(t, db.Create(&model.ChannelDegradationSample{ID: fmt.Sprintf("platform-fact-%d", i), ChannelInstanceID: instance, ChannelID: 1, ProviderID: provider, Model: "model-a", State: "normal", StartedAt: started, CompletedAt: started, TTFTMs: &zero}).Error)
				}
				t.Cleanup(func() {
					require.NoError(t, db.Where("id LIKE ?", "platform-fact-%").Delete(&model.ChannelDegradationSample{}).Error)
				})
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/public/platform", nil))
				require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
				var reply struct {
					Data model.PublicPlatformStatistics `json:"data"`
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &reply))
				assert.EqualValues(t, 1, reply.Data.Evaluations24h)
				require.NotNil(t, reply.Data.Latest)
				require.NotNil(t, reply.Data.Latest.TTFTMs)
				assert.Zero(t, *reply.Data.Latest.TTFTMs)
				for _, secret := range []string{"private-key-canary", "private.example", "instance", "provider_name", "error_code", "upstream_ratio"} {
					assert.NotContains(t, recorder.Body.String(), secret)
				}
				logDriver := driver
				if dialect == "sqlite" {
					logDriver = sqlite.Open(filepath.Join(t.TempDir(), "separate-log.db"))
				}
				logs, err := gorm.Open(logDriver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "platform_log_test_"}})
				require.NoError(t, err)
				sqlLogs, err := logs.DB()
				require.NoError(t, err)
				sqlLogs.SetMaxOpenConns(1)
				require.NoError(t, logs.AutoMigrate(&model.Log{}))
				oldLogs := model.LOG_DB
				model.LOG_DB = logs
				t.Cleanup(func() {
					model.LOG_DB = oldLogs
					require.NoError(t, logs.Migrator().DropTable(&model.Log{}))
					require.NoError(t, sqlLogs.Close())
				})
				day := now.Truncate(24*time.Hour).AddDate(0, 0, -1).Unix()
				require.NoError(t, logs.Create(&[]model.Log{
					{Type: model.LogTypeConsume, CreatedAt: day, PromptTokens: 1100000000000, CompletionTokens: 23, Username: "private-log-user", Content: "private-log-content", Other: "private-log-other"},
					{Type: model.LogTypeConsume, CreatedAt: day - 86400, PromptTokens: 0, CompletionTokens: 0},
					{Type: model.LogTypeConsume, CreatedAt: day + 86400, PromptTokens: 999},
					{Type: model.LogTypeConsume, CreatedAt: day + 172800, PromptTokens: 888},
					{Type: model.LogTypeError, CreatedAt: day, PromptTokens: 999999},
					{Type: model.LogTypeRefund, CreatedAt: day, PromptTokens: 999999},
					{Type: model.LogTypeConsume, CreatedAt: day, PromptTokens: -1},
				}).Error)
				usage, err := model.ListPublicConsumptionDailyUsage(context.Background(), 7, now)
				require.NoError(t, err)
				assert.EqualValues(t, 1100000000023, usage.Total)
				assert.Equal(t, "consumption_logs", usage.Source)
				assert.Equal(t, 2, usage.RecordedDays)
				require.Len(t, usage.Items, 7)
				assert.Nil(t, usage.Items[0].TokenCount)
				require.NotNil(t, usage.Items[5].TokenCount)
				assert.Zero(t, *usage.Items[5].TokenCount)
				recorder = httptest.NewRecorder()
				engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/public/usage/daily?days=7", nil))
				require.Equal(t, http.StatusOK, recorder.Code)
				for _, secret := range []string{"username", "private-log-user", "private-log-content", "private-log-other", "quota", "request_id"} {
					assert.NotContains(t, recorder.Body.String(), secret)
				}
				require.NoError(t, logs.Create(&model.Log{Type: model.LogTypeConsume, CreatedAt: day, PromptTokens: 9007199254740992}).Error)
				_, err = model.ListPublicConsumptionDailyUsage(context.Background(), 7, now)
				require.Error(t, err, "unsafe totals must never be silently rounded")
				recorder = httptest.NewRecorder()
				engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/public/usage/daily?days=7", nil))
				assert.Equal(t, http.StatusInternalServerError, recorder.Code)
				assert.NotContains(t, recorder.Body.String(), "9007199254740992")
				assert.NotContains(t, recorder.Body.String(), "safe public integer")
			})

			t.Run("channel sorting filtering and pagination", func(t *testing.T) {
				for _, scenario := range []struct {
					path              string
					ids               []int
					total, page, size int
				}{
					{"/api/public/channels", []int{1, 2, 3, 4, 5}, 5, 1, 20},
					{"/api/public/channels?sort_by=public_ratio&sort_order=desc", []int{5, 4, 2, 3, 1}, 5, 1, 20},
					{"/api/public/channels?sort_by=ttft", []int{2, 1, 3, 5, 4}, 5, 1, 20},
					{"/api/public/channels?sort_by=ttft_ms&sort_order=DESC", []int{5, 1, 3, 2, 4}, 5, 1, 20},
					{"/api/public/channels?p=2&page_size=2", []int{2}, 5, 2, 2},
					{"/api/public/channels?p=10&page_size=2", []int{}, 5, 10, 2},
					{"/api/public/channels?is_online=false", []int{7}, 1, 1, 20},
					{"/api/public/channels?is_degraded=true", []int{6}, 1, 1, 20},
					{"/api/public/channels?is_online=unknown", []int{8}, 1, 1, 20},
					{"/api/public/channels?is_degraded=unknown", []int{9}, 1, 1, 20},
					{"/api/public/channels?is_online=unknown&is_degraded=unknown", []int{10}, 1, 1, 20},
					{"/api/public/channels?is_online=all&is_degraded=all", []int{6, 7, 8, 9, 1, 10, 2, 3, 4, 5}, 10, 1, 20},
					{"/api/public/channels?provider_id=102", []int{3}, 1, 1, 20},
					{"/api/public/providers/101/channels?sort_by=ttft", []int{2, 1, 5, 4}, 4, 1, 20},
					{"/api/public/providers/102/channels?provider_id=102", []int{3}, 1, 1, 20},
					{"/api/public/providers/101/channels?is_online=false", []int{7}, 1, 1, 20},
					{"/api/public/providers/103/channels", []int{}, 0, 1, 20},
					{"/api/public/providers/104/channels?is_online=all&is_degraded=all", []int{}, 0, 1, 20},
					{"/api/public/providers/999999/channels", []int{}, 0, 1, 20},
				} {
					t.Run(scenario.path, func(t *testing.T) {
						recorder, reply := requestPublicCatalog(t, engine, scenario.path)
						require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
						require.True(t, reply.Success, reply.Message)
						assert.Contains(t, recorder.Header().Get("Cache-Control"), "no-store")
						assert.Equal(t, scenario.total*2, reply.Data.Total)
						assert.Equal(t, scenario.page, reply.Data.Page)
						assert.Equal(t, scenario.size, reply.Data.PageSize)
						ids := make([]int, 0, len(reply.Data.Items))
						for _, item := range reply.Data.Items {
							ids = append(ids, int(item["id"].(float64)))
							keys := make([]string, 0, len(item))
							for key := range item {
								keys = append(keys, key)
							}
							assert.ElementsMatch(t, []string{"id", "provider_id", "provider_name", "name", "type", "model", "public_ratio", "is_degraded", "is_online", "ttft_ms", "last_checked_at"}, keys, "the public response is an allowlist, not an admin channel")
							assert.Contains(t, []string{"model-a", "model-b"}, item["model"])
							assert.EqualValues(t, checked, item["last_checked_at"])
							if item["id"] == float64(1) {
								assert.Equal(t, map[string]any{"min": 0.5, "max": 0.5}, item["public_ratio"])
								assert.Equal(t, "Channel 1", item["name"])
							}
							if item["id"] == float64(2) {
								assert.EqualValues(t, 0, item["ttft_ms"])
								assert.Equal(t, true, item["is_online"])
								assert.Equal(t, false, item["is_degraded"])
							}
							if item["id"] == float64(3) {
								assert.EqualValues(t, 102, item["provider_id"])
								assert.Equal(t, "Provider B", item["provider_name"])
							}
							if item["id"] == float64(7) {
								assert.Equal(t, false, item["is_online"])
							}
							if item["id"] == float64(10) {
								assert.Nil(t, item["is_online"])
								assert.Nil(t, item["is_degraded"])
							}
							if item["id"] == float64(4) {
								assert.Nil(t, item["ttft_ms"])
							}
						}
						expected := make([]int, 0, len(scenario.ids)*2)
						for _, id := range scenario.ids {
							expected = append(expected, id, id)
						}
						assert.Equal(t, expected, ids)
						if len(scenario.ids) == 0 {
							assert.Contains(t, recorder.Body.String(), `"items":[]`)
						}
					})
				}
			})

			t.Run("provider counts only include matching public channels", func(t *testing.T) {
				for _, scenario := range []struct {
					path   string
					ids    []int
					counts []int
					total  int
				}{
					{"/api/public/providers", []int{101, 102}, []int{4, 1}, 2},
					{"/api/public/providers?p=2&page_size=1", []int{102}, []int{1}, 2},
					{"/api/public/providers?is_online=false", []int{101}, []int{1}, 1},
					{"/api/public/providers?is_online=all&is_degraded=all", []int{101, 102}, []int{9, 1}, 2},
					{"/api/public/providers?is_online=false&is_degraded=true", []int{}, []int{}, 0},
				} {
					recorder, reply := requestPublicCatalog(t, engine, scenario.path)
					require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
					require.True(t, reply.Success)
					assert.Equal(t, scenario.total, reply.Data.Total)
					ids, counts := make([]int, 0), make([]int, 0)
					for _, item := range reply.Data.Items {
						keys := make([]string, 0, len(item))
						for key := range item {
							keys = append(keys, key)
						}
						assert.ElementsMatch(t, []string{"id", "name", "channel_count"}, keys)
						ids = append(ids, int(item["id"].(float64)))
						counts = append(counts, int(item["channel_count"].(float64)))
					}
					assert.Equal(t, scenario.ids, ids)
					assert.Equal(t, scenario.counts, counts)
				}
			})

			t.Run("exact model filters paginate on the server and list all visible options", func(t *testing.T) {
				changes := map[int]string{1: "model-a, model-b", 2: "model-b", 3: "model-c,modela", 4: "model-ab,model a", 5: "m_%, model-a"}
				for id, names := range changes {
					var source model.Channel
					require.NoError(t, db.First(&source, id).Error)
					for _, name := range strings.Split(names, ",") {
						source.ModelConfigs[strings.TrimSpace(name)] = source.ModelConfig("model-a")
					}
					require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", id).Updates(map[string]any{"models": names, "model_configs": source.ModelConfigs}).Error)
				}
				t.Cleanup(func() {
					for id := range changes {
						require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", id).Update("models", "model-a,model-b").Error)
					}
				})
				for _, scenario := range []struct {
					path  string
					ids   []int
					total int
				}{
					{"/api/public/channels?model=model-a", []int{1, 5}, 2},
					{"/api/public/channels?model=model-b&page_size=1&p=2", []int{2}, 2},
					{"/api/public/channels?model=model-c", []int{3}, 1},
					{"/api/public/channels?model=model-ab", []int{4}, 1},
					{"/api/public/channels?model=model%20a", []int{4}, 1},
					{"/api/public/channels?model=modela", []int{3}, 1},
					{"/api/public/channels?model=Model-a", []int{}, 0},
					{"/api/public/channels?model=m_%25", []int{5}, 1},
					{"/api/public/channels?model=m_%25&provider_id=102", []int{}, 0},
					{"/api/public/providers/101/channels?model=model-b", []int{1, 2}, 2},
					{"/api/public/channels?model=nonexistent", []int{}, 0},
				} {
					recorder, reply := requestPublicCatalog(t, engine, scenario.path)
					require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
					assert.Equal(t, scenario.total, reply.Data.Total)
					ids := make([]int, 0, len(reply.Data.Items))
					for _, entry := range reply.Data.Items {
						ids = append(ids, int(entry["id"].(float64)))
					}
					assert.Equal(t, scenario.ids, ids, scenario.path)
				}
				recorder, providers := requestPublicCatalog(t, engine, "/api/public/providers?model=model-b")
				require.Equal(t, http.StatusOK, recorder.Code)
				require.Len(t, providers.Data.Items, 1)
				assert.EqualValues(t, 101, providers.Data.Items[0]["id"])
				assert.EqualValues(t, 2, providers.Data.Items[0]["channel_count"])
				for _, scenario := range []struct {
					path   string
					models []string
					total  int
				}{
					{"/api/public/models", []string{"m_%", "model a", "model-a", "model-ab", "model-b", "model-c", "modela"}, 7},
					{"/api/public/models?p=2", []string{}, 7},
					{"/api/public/models?page_size=2&p=2", []string{"model-a", "model-ab"}, 7},
					{"/api/public/models?provider_id=102", []string{"model-c", "modela"}, 2},
					{"/api/public/models?provider_id=103", []string{}, 0},
					{"/api/public/models?search=MODEL-B", []string{"model-b"}, 1},
					{"/api/public/models?search=m_%25", []string{"m_%"}, 1},
					{"/api/public/models?is_online=unknown&is_degraded=unknown", []string{"model-a", "model-b"}, 2},
				} {
					recorder := httptest.NewRecorder()
					engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, scenario.path, nil))
					require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
					for _, secret := range []string{"private-key-canary", "upstream_ratio", "private.example"} {
						assert.NotContains(t, recorder.Body.String(), secret)
					}
					var options struct {
						Success bool `json:"success"`
						Data    struct {
							Total          int      `json:"total"`
							Items          []string `json:"items"`
							DefaultModel   string   `json:"default_model"`
							HomepageModels []string `json:"homepage_models"`
						} `json:"data"`
					}
					require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &options))
					assert.True(t, options.Success)
					assert.Equal(t, scenario.total, options.Data.Total)
					assert.Equal(t, scenario.models, options.Data.Items)
					assert.Equal(t, "gpt-6.1-sol", options.Data.DefaultModel)
					assert.Equal(t, []string{}, options.Data.HomepageModels)
				}
			})

			t.Run("homepage settings are public metadata without restricting the directory", func(t *testing.T) {
				common.OptionMapRWMutex.Lock()
				wasNil := common.OptionMap == nil
				if wasNil {
					common.OptionMap = make(map[string]string)
				}
				previous, existed := common.OptionMap[setting.HomepageBoardOptionKey]
				common.OptionMap[setting.HomepageBoardOptionKey] = `{"models":["model-b","model-a"],"default_model":"model-b"}`
				common.OptionMapRWMutex.Unlock()
				t.Cleanup(func() {
					common.OptionMapRWMutex.Lock()
					if existed {
						common.OptionMap[setting.HomepageBoardOptionKey] = previous
					} else {
						delete(common.OptionMap, setting.HomepageBoardOptionKey)
					}
					if wasNil {
						common.OptionMap = nil
					}
					common.OptionMapRWMutex.Unlock()
				})
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/public/models?page_size=2", nil))
				require.Equal(t, http.StatusOK, recorder.Code)
				var reply struct {
					Data struct {
						Items          []string `json:"items"`
						Total          int      `json:"total"`
						HomepageModels []string `json:"homepage_models"`
						DefaultModel   string   `json:"default_model"`
					} `json:"data"`
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &reply))
				assert.Empty(t, reply.Data.HomepageModels)
				assert.Equal(t, "gpt-6.1-sol", reply.Data.DefaultModel)
				assert.Equal(t, []string{"model-a", "model-b"}, reply.Data.Items)
				assert.Equal(t, 2, reply.Data.Total)
			})

			t.Run("append-only evaluation history survives migration and is safely published by provider", func(t *testing.T) {
				now := time.Now().UnixMilli()
				today := now / model.EvaluationDayMs * model.EvaluationDayMs
				zero, hundred, twoHundred := int64(0), int64(100), int64(200)
				for i, fact := range []struct {
					channel     int
					at          int64
					state, name string
					ttft        *int64
				}{
					{1, today - 2*model.EvaluationDayMs + 1000, "normal", "model-a", &zero},
					{1, today - 2*model.EvaluationDayMs + 2000, "normal", "model-a", &hundred},
					{1, today - model.EvaluationDayMs + 1000, "abnormal", "model-a", &twoHundred},
					{1, today - model.EvaluationDayMs + 2000, "error", "model-b", nil},
					{7, today - model.EvaluationDayMs + 3000, "error", "model-a", nil},
					{11, today - model.EvaluationDayMs + 1000, "normal", "private-model-canary", &hundred},
					{3, today - model.EvaluationDayMs + 1000, "normal", "model-a", &hundred},
					{1, today - 10*model.EvaluationDayMs, "normal", "model-a", &hundred},
					{1, today + model.EvaluationDayMs, "normal", "model-a", &hundred},
				} {
					var source model.Channel
					require.NoError(t, db.First(&source, fact.channel).Error)
					sample := model.ChannelDegradationSample{ID: fmt.Sprintf("history-%d", i), State: fact.state, Model: fact.name, UpstreamModel: "private-upstream-model-canary", StartedAt: fact.at, CompletedAt: fact.at + 500, DurationMs: 500, TTFTMs: fact.ttft}
					require.NoError(t, model.RecordChannelDegradation(context.Background(), &source, &sample, "", true))
				}
				var source model.Channel
				require.NoError(t, db.First(&source, 1).Error)
				cancelled := model.ChannelDegradationSample{ID: "cancelled-attempt", State: "error", ErrorCode: "cancelled", Model: "model-b", StartedAt: today - model.EvaluationDayMs + 2500, CompletedAt: today - model.EvaluationDayMs + 2500}
				require.NoError(t, model.AppendChannelDegradationHistory(context.Background(), &source, &cancelled))
				for _, sample := range []model.ChannelDegradationSample{
					{ID: "replaced-instance", ChannelID: 1, ChannelInstanceID: "old-instance", ProviderID: 101, Model: "model-a", State: "normal", StartedAt: today - model.EvaluationDayMs},
					{ID: "reassigned-provider", ChannelID: 1, ChannelInstanceID: *source.InstanceID, ProviderID: 102, Model: "model-a", State: "normal", StartedAt: today - model.EvaluationDayMs},
				} {
					require.NoError(t, db.Create(&sample).Error)
				}
				// Simulate an upgrade from the pre-timeseries-index schema, then
				// run the exact GORM migration twice and verify retained facts.
				for _, index := range []string{"idx_evaluation_provider_time", "idx_evaluation_instance_time"} {
					require.NoError(t, db.Migrator().DropIndex(&model.ChannelDegradationSample{}, index))
				}
				for range 2 {
					require.NoError(t, db.AutoMigrate(&model.ChannelDegradationSample{}))
				}
				for _, index := range []string{"idx_evaluation_provider_time", "idx_evaluation_instance_time"} {
					assert.True(t, db.Migrator().HasIndex(&model.ChannelDegradationSample{}, index))
				}
				var preserved model.ChannelDegradationSample
				require.NoError(t, db.First(&preserved, "id = ?", "history-0").Error)
				require.NotNil(t, preserved.TTFTMs)
				assert.EqualValues(t, 0, *preserved.TTFTMs)
				assert.Error(t, model.RecordChannelDegradation(context.Background(), &source, &preserved, "", true), "duplicate facts cannot overwrite history")

				for _, scenario := range []struct {
					path          string
					total, online int64
				}{
					{"/api/public/providers/101/history?days=7", 6, 3},
					{"/api/public/providers/101/history?days=90", 7, 4},
					{"/api/public/providers/101/history?days=7&model=model-a", 4, 3},
					{"/api/public/providers/101/history?days=7&channel_id=1", 5, 3},
					{"/api/public/providers/101/history?days=7&channel_id=3", 0, 0},
					{"/api/public/providers/102/history?days=7", 1, 1},
					{"/api/public/providers/104/history?days=7", 0, 0},
					{"/api/public/providers/999999/history?days=7", 0, 0},
				} {
					recorder := httptest.NewRecorder()
					engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, scenario.path, nil))
					require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
					for _, secret := range []string{"channel_instance_id", "upstream_model", "private-upstream-model-canary", "private-model-canary", "private-key-canary", "private.example", "task_id"} {
						assert.NotContains(t, recorder.Body.String(), secret)
					}
					var reply struct {
						Success bool                        `json:"success"`
						Data    model.PublicProviderHistory `json:"data"`
					}
					require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &reply))
					assert.True(t, reply.Success)
					assert.Equal(t, scenario.total, reply.Data.Total)
					assert.Equal(t, scenario.online, reply.Data.Online)
					assert.Len(t, reply.Data.Buckets, reply.Data.Days)
					if scenario.total == 0 {
						assert.Nil(t, reply.Data.AverageTTFTMs)
						assert.Empty(t, reply.Data.Recent)
					}
					if scenario.total == 6 {
						require.NotNil(t, reply.Data.AverageTTFTMs)
						assert.Equal(t, float64(100), *reply.Data.AverageTTFTMs)
						assert.EqualValues(t, 2, reply.Data.Errors)
						assert.EqualValues(t, 1, reply.Data.Degraded)
						assert.Len(t, reply.Data.Recent, 6)
						assert.EqualValues(t, 1, reply.Data.Unknown)
						assert.Equal(t, "unknown", reply.Data.Recent[1].State)
						assert.Equal(t, "Channel 7", reply.Data.Recent[0].ChannelName)
						for _, bucket := range reply.Data.Buckets {
							if bucket.Total == 0 {
								assert.Nil(t, bucket.AverageTTFTMs)
							}
						}
					}
				}
				for _, change := range []struct {
					column          string
					value, original any
				}{
					{"instance_id", "replacement-instance", *source.InstanceID},
					{"provider_id", 102, 101},
					{"status", common.ChannelStatusManuallyDisabled, common.ChannelStatusEnabled},
				} {
					require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 1).Update(change.column, change.value).Error)
					history, err := model.ListPublicProviderHistory(context.Background(), model.PublicHistoryQuery{ProviderID: 101, Days: 7}, time.Now(), model.NewRelationalEvaluationHistoryStore(db))
					require.NoError(t, err)
					assert.EqualValues(t, 1, history.Total, "changed/private channel identity must not publish its old history")
					require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 1).Update(change.column, change.original).Error)
				}
				for _, path := range []string{"/api/public/providers/abc/history", "/api/public/providers/101/history?days=1000", "/api/public/providers/101/history?days=7&days=30", "/api/public/providers/101/history?channel_id=-1", "/api/public/providers/101/history?model=", "/api/public/providers/101/history?model=a%2Cb", "/api/public/providers/101/history?is_online=true", "/api/public/providers/101/history?fields=key"} {
					recorder := httptest.NewRecorder()
					engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
					assert.Equal(t, http.StatusBadRequest, recorder.Code, path)
				}
			})

			t.Run("legacy manual storage remains portable but public usage only reads consumption logs", func(t *testing.T) {
				previousLogDB, previousRedis := model.LOG_DB, common.RedisEnabled
				model.LOG_DB, common.RedisEnabled = db, false
				defer func() { model.LOG_DB, common.RedisEnabled = previousLogDB, previousRedis }()
				for i, role := range []int{common.RoleCommonUser, common.RoleAdminUser, common.RoleRootUser} {
					token := fmt.Sprintf("daily-usage-test-%d", i)
					require.NoError(t, db.Create(&model.User{Id: 91 + i, Username: fmt.Sprintf("private-usage-editor-%d", i), AffCode: fmt.Sprintf("usage%d", i), Role: role, Status: common.UserStatusEnabled, AccessToken: &token}).Error)
				}
				registerDailyUsageManagementRoutes(engine.Group("/api/option", middleware.RootAuth()))
				now := time.Now().UTC()
				today, yesterday := now.Format("2006-01-02"), now.AddDate(0, 0, -1).Format("2006-01-02")
				dayBefore := now.AddDate(0, 0, -2).Format("2006-01-02")
				require.NoError(t, db.Create(&[]model.Log{
					{Type: model.LogTypeConsume, CreatedAt: now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1).Unix(), PromptTokens: 100, CompletionTokens: 23, Username: "private-token-user", Other: "private-log-payload"},
					{Type: model.LogTypeConsume, CreatedAt: now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -2).Unix(), PromptTokens: 0, CompletionTokens: 0},
					{Type: model.LogTypeConsume, CreatedAt: now.UTC().Truncate(24 * time.Hour).Unix(), PromptTokens: 999},
					{Type: model.LogTypeError, CreatedAt: now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1).Unix(), PromptTokens: 999999},
				}).Error)
				body := fmt.Sprintf(`{"items":[{"date":%q,"token_count":123},{"date":%q,"token_count":0},{"date":%q,"token_count":999}],"updated_by":91}`, yesterday, dayBefore, today)
				request := func(method, path, body, token string) *httptest.ResponseRecorder {
					recorder := httptest.NewRecorder()
					req := httptest.NewRequest(method, path, strings.NewReader(body))
					if token != "" {
						req.Header.Set("Authorization", "Bearer "+token)
					}
					req.Header.Set("Content-Type", "application/json")
					engine.ServeHTTP(recorder, req)
					return recorder
				}
				for _, operation := range []struct{ method, path string }{
					{http.MethodGet, "/api/option/daily_usage"},
					{http.MethodPut, "/api/option/daily_usage"},
					{http.MethodDelete, "/api/option/daily_usage/" + yesterday},
				} {
					for _, token := range []string{"", "daily-usage-test-0", "daily-usage-test-1"} {
						want := http.StatusForbidden
						if token == "" {
							want = http.StatusUnauthorized
						}
						assert.Equal(t, want, request(operation.method, operation.path, body, token).Code)
					}
				}
				// A pre-token-version manually entered number uses the same physical
				// column; upgrades must preserve it, not multiply or erase it.
				legacyDay := now.AddDate(0, 0, -10).Format("2006-01-02")
				require.NoError(t, db.Table(db.NamingStrategy.TableName("PublicDailyUsage")).Create(map[string]any{
					"day": legacyDay, "request_count": 127, "updated_at": now.Unix(), "updated_by": 93,
				}).Error)
				root := "daily-usage-test-2"
				require.Equal(t, http.StatusOK, request(http.MethodPut, "/api/option/daily_usage", body, root).Code)
				var stored model.PublicDailyUsage
				require.NoError(t, db.First(&stored, "day = ?", yesterday).Error)
				assert.Equal(t, 93, stored.UpdatedBy, "client metadata cannot forge the editor")
				for range 2 {
					require.NoError(t, db.AutoMigrate(&model.PublicDailyUsage{}))
				}
				var rows int64
				require.NoError(t, db.Model(&model.PublicDailyUsage{}).Count(&rows).Error)
				assert.EqualValues(t, 4, rows, "repeated migrations preserve unique dates and stored zeroes")
				legacy, err := model.ListPublicDailyUsage(context.Background(), 30, false, now)
				require.NoError(t, err)
				assert.EqualValues(t, 250, legacy.Total)
				assert.Equal(t, "tokens", legacy.Unit)
				for _, point := range legacy.Items {
					if point.Date == legacyDay {
						require.NotNil(t, point.TokenCount)
						assert.EqualValues(t, 127, *point.TokenCount, "manual values survive unchanged")
					}
				}
				for _, scenario := range []struct {
					path     string
					total    int64
					recorded int
					last     string
				}{
					{"/api/public/usage/daily?days=7", 123, 2, yesterday},
					{"/api/option/daily_usage?days=7", 123, 2, yesterday},
				} {
					recorder := request(http.MethodGet, scenario.path, "", root)
					require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
					var reply struct {
						Success bool                         `json:"success"`
						Data    model.PublicDailyUsageSeries `json:"data"`
					}
					require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &reply))
					assert.Equal(t, scenario.total, reply.Data.Total)
					assert.Equal(t, scenario.recorded, reply.Data.RecordedDays)
					assert.Equal(t, scenario.last, reply.Data.To)
					assert.Len(t, reply.Data.Items, 7)
					assert.Nil(t, reply.Data.Items[0].TokenCount)
					assert.Equal(t, "tokens", reply.Data.Unit)
					if strings.HasPrefix(scenario.path, "/api/public/") {
						assert.Equal(t, "consumption_logs", reply.Data.Source)
					}
					assert.NotContains(t, recorder.Body.String(), "private-log-payload")
					assert.NotContains(t, recorder.Body.String(), "private-token-user")
					assert.NotContains(t, recorder.Body.String(), "request_count")
					assert.NotContains(t, recorder.Body.String(), "updated_by")
					assert.NotContains(t, recorder.Body.String(), "private-usage-editor")
				}
				// Future values are manageable in advance but never exposed before
				// their UTC date has ended, even when the public caller supplies to.
				futureBody := `{"items":[{"date":"2999-01-01","token_count":456000000},{"date":"2998-12-31","token_count":0}]}`
				require.Equal(t, http.StatusOK, request(http.MethodPut, "/api/option/daily_usage", futureBody, root).Code)
				futureRead := request(http.MethodGet, "/api/option/daily_usage?days=7&to=2999-01-01", "", root)
				require.Equal(t, http.StatusOK, futureRead.Code, futureRead.Body.String())
				var futureReply struct {
					Data model.PublicDailyUsageSeries `json:"data"`
				}
				require.NoError(t, common.Unmarshal(futureRead.Body.Bytes(), &futureReply))
				assert.Equal(t, "2999-01-01", futureReply.Data.To)
				assert.EqualValues(t, 456000000, futureReply.Data.Total)
				assert.Equal(t, 2, futureReply.Data.RecordedDays)
				require.Len(t, futureReply.Data.Items, 7)
				require.NotNil(t, futureReply.Data.Items[5].TokenCount)
				assert.Zero(t, *futureReply.Data.Items[5].TokenCount)
				assert.NotContains(t, futureRead.Body.String(), "updated_by")
				assert.NotContains(t, request(http.MethodGet, "/api/public/usage/daily?days=7", "", "").Body.String(), "2999")
				assert.Equal(t, http.StatusBadRequest, request(http.MethodGet, "/api/public/usage/daily?days=7&to=2999-01-01", "", "").Code)
				for _, suffix := range []string{"?to=", "?to=bad", "?to=2026-02-30", "?to=10000-01-01", "?to=2999-01-01&to=2999-01-02", "?to=2999-01-01&model=x"} {
					assert.Equal(t, http.StatusBadRequest, request(http.MethodGet, "/api/option/daily_usage"+suffix, "", root).Code, suffix)
				}
				futureDate, err := time.Parse("2006-01-02", "2999-01-01")
				require.NoError(t, err)
				published, err := model.ListPublicDailyUsage(context.Background(), 7, false, futureDate.AddDate(0, 0, 1))
				require.NoError(t, err)
				assert.EqualValues(t, 456000000, published.Total)
				require.Equal(t, http.StatusOK, request(http.MethodDelete, "/api/option/daily_usage/2999-01-01", "", root).Code)
				assert.Equal(t, http.StatusNotFound, request(http.MethodDelete, "/api/option/daily_usage/2999-01-01", "", root).Code)
				// Correction is an upsert, not a duplicate or an additive increment.
				correction := fmt.Sprintf(`{"items":[{"date":%q,"token_count":7}]}`, yesterday)
				require.Equal(t, http.StatusOK, request(http.MethodPut, "/api/option/daily_usage", correction, root).Code)
				publicAfterManual := request(http.MethodGet, "/api/public/usage/daily?days=7", "", "")
				var actual struct {
					Data model.PublicDailyUsageSeries `json:"data"`
				}
				require.NoError(t, common.Unmarshal(publicAfterManual.Body.Bytes(), &actual))
				assert.EqualValues(t, 123, actual.Data.Total, "manual corrections must not replace actual consumption totals")
				for _, invalid := range []string{
					`{"items":[]}`, `{"items":[{"date":"bad","token_count":1}]}`,
					fmt.Sprintf(`{"items":[{"date":%q,"token_count":-1}]}`, yesterday),
					fmt.Sprintf(`{"items":[{"date":%q,"token_count":1.5}]}`, yesterday),
					fmt.Sprintf(`{"items":[{"date":%q,"token_count":null}]}`, yesterday),
					fmt.Sprintf(`{"items":[{"date":%q,"token_count":1000000000001}]}`, yesterday),
					fmt.Sprintf(`{"items":[{"date":%q,"token_count":10},{"date":"2026-02-30","token_count":1}]}`, yesterday),
					fmt.Sprintf(`{"items":[{"date":%q,"token_count":1},{"date":%q,"token_count":2}]}`, yesterday, yesterday),
					fmt.Sprintf(`{"items":[{"date":%q,"request_count":1}]}`, yesterday),
					correction + correction,
				} {
					assert.Equal(t, http.StatusBadRequest, request(http.MethodPut, "/api/option/daily_usage", invalid, root).Code, invalid)
				}
				require.NoError(t, db.First(&stored, "day = ?", yesterday).Error)
				assert.EqualValues(t, 7, stored.TokenCount, "invalid batches leave existing data untouched")
				for _, suffix := range []string{"?days=366", "?days=7&days=30", "?days=", "?include_today=true", "?model=x", "?fields=updated_by"} {
					assert.Equal(t, http.StatusBadRequest, request(http.MethodGet, "/api/public/usage/daily"+suffix, "", "").Code)
				}
				assert.Equal(t, http.StatusNotFound, request(http.MethodPut, "/api/public/usage/daily", correction, root).Code)
				assert.Equal(t, http.StatusOK, request(http.MethodDelete, "/api/option/daily_usage/"+yesterday, "", root).Code)
				assert.Equal(t, http.StatusNotFound, request(http.MethodDelete, "/api/option/daily_usage/"+yesterday, "", root).Code)
				series, err := model.ListPublicDailyUsage(context.Background(), 7, false, now)
				require.NoError(t, err)
				assert.Nil(t, series.Items[6].TokenCount, "deletion restores missing, not a fabricated zero")
				require.NotNil(t, series.Items[5].TokenCount)
				assert.EqualValues(t, 0, *series.Items[5].TokenCount)
				// The retained archive helper can read this value next day, not the public consumption API.
				next, err := model.ListPublicDailyUsage(context.Background(), 7, false, now.AddDate(0, 0, 1))
				require.NoError(t, err)
				assert.EqualValues(t, 999, *next.Items[6].TokenCount)
				maximum := model.MaxPublicDailyUsageCount
				batch := make([]model.DailyUsageInput, 0, 366)
				for i := range 366 {
					batch = append(batch, model.DailyUsageInput{Date: now.AddDate(0, 0, -i).Format("2006-01-02"), TokenCount: &maximum})
				}
				require.NoError(t, model.SetPublicDailyUsage(context.Background(), batch, 93, now))
				year, err := model.ListPublicDailyUsage(context.Background(), 365, false, now)
				require.NoError(t, err)
				assert.EqualValues(t, 365000000000000, year.Total, "full batches remain portable and JSON-integer-safe")
				assert.Equal(t, 365, year.RecordedDays)
				assert.ErrorIs(t, model.SetPublicDailyUsage(context.Background(), append(batch, batch[0]), 93, now), model.ErrInvalidDailyUsage)
			})

			t.Run("invalid and private query inputs are rejected", func(t *testing.T) {
				for _, path := range []string{
					"/api/public/platform?model=x", "/api/public/platform?fields=key", "/api/public/platform?days=7&days=30", "/api/public/platform?to=2999-01-01",
					"/api/public/channels?sort_by=upstream_ratio", "/api/public/channels?sort_by=key",
					"/api/public/channels?model=", "/api/public/channels?model=a%2Cb", "/api/public/channels?model=%0A",
					"/api/public/channels?model=model-a&model=model-b", "/api/public/models?model=model-a", "/api/public/models?sort_by=ttft", "/api/public/models?search=%20", "/api/public/channels?search=model-a", "/api/public/models?search=x&search=y",
					"/api/public/channels?sort_order=desc%3BDROP", "/api/public/channels?is_online=1",
					"/api/public/channels?is_degraded=invalid", "/api/public/channels?is_online=",
					"/api/public/channels?is_online=true&is_online=false", "/api/public/channels?fields=upstream_ratio",
					"/api/public/channels?is_online=%ZZ", "/api/public/channels?is_online=false;is_degraded=true",
					"/api/public/channels?p=0", "/api/public/channels?p=-1", "/api/public/channels?p=10001",
					"/api/public/channels?p=999999999999999999999999", "/api/public/channels?page_size=0",
					"/api/public/channels?page_size=101", "/api/public/channels?provider_id=0",
					"/api/public/providers/abc/channels", "/api/public/providers/0/channels",
					"/api/public/providers/101/channels?provider_id=102", "/api/public/providers?sort_by=ttft",
				} {
					recorder, reply := requestPublicCatalog(t, engine, path)
					assert.Equal(t, http.StatusBadRequest, recorder.Code, path)
					assert.False(t, reply.Success)
				}
			})

			t.Run("public registration does not open administrative routes", func(t *testing.T) {
				for _, path := range []string{"/api/providers/", "/api/channel/"} {
					recorder, _ := requestPublicCatalog(t, engine, path)
					assert.Equal(t, http.StatusUnauthorized, recorder.Code)
				}
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/public/channels", nil))
				assert.Equal(t, http.StatusNotFound, recorder.Code, "public catalog is read-only")
			})

			t.Run("storage errors never expose database details", func(t *testing.T) {
				name := "public_catalog_failure"
				failure := func(tx *gorm.DB) {
					tx.AddError(errors.New("private-key-canary upstream_ratio=92837.625 private.example"))
				}
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register(name, failure))
				require.NoError(t, db.Callback().Row().Before("gorm:row").Register(name, failure))
				defer func() {
					require.NoError(t, db.Callback().Query().Remove(name))
					require.NoError(t, db.Callback().Row().Remove(name))
				}()
				for _, path := range []string{"/api/public/providers", "/api/public/channels", "/api/public/providers/101/channels", "/api/public/providers/101/history", "/api/public/usage/daily", "/api/public/platform"} {
					recorder, reply := requestPublicCatalog(t, engine, path)
					assert.Equal(t, http.StatusInternalServerError, recorder.Code)
					assert.False(t, reply.Success)
					if strings.HasSuffix(path, "/usage/daily") {
						assert.Equal(t, "daily usage is temporarily unavailable", reply.Message)
					} else if strings.HasSuffix(path, "/history") {
						assert.Equal(t, "evaluation history is temporarily unavailable", reply.Message)
					} else if strings.HasSuffix(path, "/platform") {
						assert.Equal(t, "platform statistics are temporarily unavailable", reply.Message)
					} else {
						assert.Equal(t, "public catalog is temporarily unavailable", reply.Message)
					}
				}
			})
		})
	}
}
