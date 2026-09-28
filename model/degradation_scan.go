package model

import (
	"context"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DegradationModelPolicy follows the catalog's exact / prefix / suffix /
// contains precedence. Missing or unset metadata defaults to disabled;
// a provider-level opt-out is applied separately to the channel query.
type DegradationModelPolicy struct{ records []Model }

func LoadDegradationModelPolicy(ctx context.Context) (DegradationModelPolicy, error) {
	var policy DegradationModelPolicy
	err := DB.WithContext(ctx).Model(&Model{}).Select("model_name", "name_rule", "degradation_check_enabled").Order("id ASC").Find(&policy.records).Error
	return policy, err
}

func (policy DegradationModelPolicy) Enabled(name string) bool {
	if name == "" {
		return false
	}
	matched := resolveModelMetadata(policy.records, []string{name})[name]
	return matched != nil && matched.DegradationCheckEnabled != nil && *matched.DegradationCheckEnabled
}

// OpenDegradationDatabase opens only an existing primary database. No server,
// account creation, schema migration, log database or background jobs are started.
func OpenDegradationDatabase() (func() error, error) {
	config := &gorm.Config{Logger: logger.Discard}
	dsn := os.Getenv("SQL_DSN")
	var dialect gorm.Dialector
	dbType := common.DatabaseTypeSQLite
	switch {
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"), strings.HasPrefix(dsn, "host="):
		dbType = common.DatabaseTypePostgreSQL
		dialect = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
	case dsn == "" || strings.HasPrefix(dsn, "local"):
		path := os.Getenv("SQLITE_PATH")
		if path == "" {
			path = common.SQLitePath
		}
		if !strings.HasPrefix(path, "file:") {
			path = "file:" + path
		}
		parsed, err := url.Parse(path)
		if err != nil {
			return nil, errors.New("invalid SQLite database configuration")
		}
		query := parsed.Query()
		query.Set("mode", "rw")
		parsed.RawQuery = query.Encode()
		dialect = sqlite.Open(parsed.String())
	default:
		if isClickHouseDSN(dsn) {
			return nil, errors.New("unsupported primary database")
		}
		dbType = common.DatabaseTypeMySQL
		if !strings.Contains(dsn, "parseTime") {
			separator := "?"
			if strings.Contains(dsn, "?") {
				separator = "&"
			}
			dsn += separator + "parseTime=true"
		}
		dialect = mysql.Open(dsn)
	}
	db, err := gorm.Open(dialect, config)
	if err != nil {
		return nil, errors.New("could not open primary database")
	}
	pool, err := db.DB()
	if err != nil {
		return nil, errors.New("could not open primary database")
	}
	pool.SetMaxOpenConns(4)
	pool.SetMaxIdleConns(2)
	for _, table := range []any{&Channel{}, &Provider{}, &Model{}, &ChannelDegradationSample{}, &SystemTask{}, &SystemTaskLock{}} {
		if !db.Migrator().HasTable(table) {
			_ = pool.Close()
			return nil, errors.New("database schema not ready; start the upgraded service first")
		}
	}
	if !db.Migrator().HasColumn(&Channel{}, "InstanceID") {
		_ = pool.Close()
		return nil, errors.New("database schema not ready; start the upgraded service first")
	}
	DB = db
	common.SetMainDatabaseType(dbType)
	initCol()
	return func() error { DB = nil; return pool.Close() }, nil
}

// BeginDegradationScan publishes a running task and its lease atomically. A
// scheduler cannot steal a pending CLI task or silently discard its filters.
// A pending/running periodic job or another CLI scan causes a safe busy error.
func BeginDegradationScan(ctx context.Context, runnerID string, payload any, lockUntil int64) (*SystemTask, error) {
	id, err := GenerateSystemTaskID()
	if err != nil {
		return nil, err
	}
	encoded, err := marshalSystemTaskJSON(payload)
	if err != nil {
		return nil, err
	}
	taskType := SystemTaskTypeChannelDegradation
	task := &SystemTask{TaskID: id, Type: taskType, Status: SystemTaskStatusRunning, ActiveKey: &taskType, LockedBy: runnerID, Payload: encoded}
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var previous SystemTaskLock
		err := lockForUpdate(tx).Where("type = ?", taskType).First(&previous).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil {
			if previous.LockedUntil >= common.GetTimestamp() {
				return errors.New("degradation task busy")
			}
			if err := tx.Model(&SystemTask{}).Where("task_id = ? AND status = ?", previous.TaskID, SystemTaskStatusRunning).Updates(map[string]any{
				"status": SystemTaskStatusFailed, "active_key": nil, "error": "task lease expired", "updated_at": common.GetTimestamp(),
			}).Error; err != nil {
				return err
			}
			if err := tx.Where("type = ? AND task_id = ?", taskType, previous.TaskID).Delete(&SystemTaskLock{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&SystemTaskLock{Type: taskType, TaskID: id, LockedBy: runnerID, LockedUntil: lockUntil}).Error; err != nil {
			return err
		}
		return tx.Create(task).Error
	})
	return task, err
}

// ApplyDegradationScanSummary publishes a whole-channel verdict only after all
// selected models were measured. Partial model filters never call this method.
// Per-model facts stay append-only; they are not fake channel aggregate samples.
func ApplyDegradationScanSummary(ctx context.Context, source *Channel, taskID, runnerID string, startedAt, completedAt int64, degraded *bool, online bool, ttft *int64) (bool, error) {
	applied := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lease SystemTaskLock
		err := lockForUpdate(tx).Where("type = ? AND task_id = ? AND locked_by = ? AND locked_until >= ?", SystemTaskTypeChannelDegradation, taskID, runnerID, common.GetTimestamp()).First(&lease).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSystemTaskLockLost
		}
		if err != nil {
			return err
		}
		var current Channel
		err = lockForUpdate(tx).First(&current, source.Id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if current.Status != common.ChannelStatusEnabled || current.SkipDegradationCheck || !reflect.DeepEqual(channelDegradationInputs(source), channelDegradationInputs(&current)) || (current.LastCheckedAt != nil && *current.LastCheckedAt > startedAt/1000) {
			return nil
		}
		updates := map[string]any{"is_degraded": degraded, "is_online": online, "last_checked_at": completedAt / 1000}
		if ttft != nil && *ttft >= 0 {
			updates["ttft_ms"] = *ttft
		}
		if err := tx.Model(&Channel{}).Where("id = ?", source.Id).Updates(updates).Error; err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}
