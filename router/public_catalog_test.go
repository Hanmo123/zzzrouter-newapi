package router

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
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
	for _, secret := range []string{"upstream_ratio", "92837.625", "private-key-canary", "private-provider-description", "private-code", "private.example", "private-setting-canary", "private-remark-canary", "private-group"} {
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
			model.DB = db
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			t.Cleanup(func() {
				require.NoError(t, db.Migrator().DropTable(&model.Channel{}, &model.Provider{}))
				model.DB = previousDB
				common.SetMainDatabaseType(previousType)
				require.NoError(t, sqlDB.Close())
			})
			var version string
			versionQuery := "SELECT version()"
			if dialect == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("database version: %s", version)
			require.NoError(t, db.AutoMigrate(&model.Provider{}, &model.Channel{}))
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
				channel := model.Channel{Id: row.id, ProviderID: row.provider, Status: row.status, Type: 1, Name: fmt.Sprintf("Channel %d", row.id), Models: "model-a,model-b", Group: "private-group", Key: "private-key-canary", PublicRatio: &row.ratio, UpstreamRatio: &privateRatio, IsOnline: row.online, IsDegraded: row.degraded, TTFTMs: row.ttft, LastCheckedAt: &checked, BaseURL: &baseURL, Setting: &channelSetting, Remark: &remark}
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
					{"/api/public/channels?p=2&page_size=2", []int{3, 4}, 5, 2, 2},
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
						assert.Equal(t, scenario.total, reply.Data.Total)
						assert.Equal(t, scenario.page, reply.Data.Page)
						assert.Equal(t, scenario.size, reply.Data.PageSize)
						ids := make([]int, 0, len(reply.Data.Items))
						for _, item := range reply.Data.Items {
							ids = append(ids, int(item["id"].(float64)))
							keys := make([]string, 0, len(item))
							for key := range item {
								keys = append(keys, key)
							}
							assert.ElementsMatch(t, []string{"id", "provider_id", "provider_name", "name", "type", "models", "public_ratio", "is_degraded", "is_online", "ttft_ms", "last_checked_at"}, keys, "the public response is an allowlist, not an admin channel")
							assert.Equal(t, "model-a,model-b", item["models"])
							assert.EqualValues(t, checked, item["last_checked_at"])
							if item["id"] == float64(1) {
								assert.EqualValues(t, 0.5, item["public_ratio"])
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
						assert.Equal(t, scenario.ids, ids)
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
					require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", id).Update("models", names).Error)
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
					assert.Equal(t, "gpt-6-astra", options.Data.DefaultModel)
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
				assert.Equal(t, []string{"model-b", "model-a"}, reply.Data.HomepageModels)
				assert.Equal(t, "model-b", reply.Data.DefaultModel)
				assert.Equal(t, []string{"model-a", "model-b"}, reply.Data.Items)
				assert.Equal(t, 2, reply.Data.Total)
			})

			t.Run("invalid and private query inputs are rejected", func(t *testing.T) {
				for _, path := range []string{
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
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
					tx.AddError(errors.New("private-key-canary upstream_ratio=92837.625 private.example"))
				}))
				defer func() { require.NoError(t, db.Callback().Query().Remove(name)) }()
				for _, path := range []string{"/api/public/providers", "/api/public/channels", "/api/public/providers/101/channels"} {
					recorder, reply := requestPublicCatalog(t, engine, path)
					assert.Equal(t, http.StatusInternalServerError, recorder.Code)
					assert.False(t, reply.Success)
					assert.Equal(t, "public catalog is temporarily unavailable", reply.Message)
				}
			})
		})
	}
}
