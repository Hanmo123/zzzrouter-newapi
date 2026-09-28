package service

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/gorm"
)

type DegradationScanOptions struct {
	ProviderIDs []int                 `json:"provider_ids"`
	ChannelIDs  []int                 `json:"channel_ids"`
	Models      []string              `json:"models"`
	Status      string                `json:"status"`
	DryRun      bool                  `json:"dry_run"`
	Config      DegradationTaskConfig `json:"config"`
}

type DegradationScanSummary struct {
	Kind            string `json:"kind"`
	TaskID          string `json:"task_id,omitempty"`
	DryRun          bool   `json:"dry_run"`
	Channels        int    `json:"channels"`
	Targets         int    `json:"targets"`
	Normal          int    `json:"normal"`
	Abnormal        int    `json:"abnormal"`
	Errors          int    `json:"errors"`
	Skipped         int    `json:"skipped"`
	SnapshotApplied int    `json:"snapshot_applied"`
}

type degradationScanEvent struct {
	Kind        string             `json:"kind"`
	TaskID      string             `json:"task_id,omitempty"`
	ProviderID  int                `json:"provider_id"`
	ChannelID   int                `json:"channel_id"`
	Model       string             `json:"model"`
	State       string             `json:"state"`
	Reason      string             `json:"reason,omitempty"`
	Observation *DegradationResult `json:"observation,omitempty"`
}

// RunDegradationCLI synchronously scans configured channel/model pairs. Database
// access is an operator privilege; there is no public HTTP trigger or key argument.
func RunDegradationCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("degradation-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := DegradationScanOptions{}
	var help bool
	for _, dimension := range []struct {
		name   string
		values *[]int
	}{{"provider-id", &options.ProviderIDs}, {"channel-id", &options.ChannelIDs}} {
		flags.Func(dimension.name, "positive IDs, comma-separated or repeatable; dimensions intersect", func(raw string) error {
			for part := range strings.SplitSeq(raw, ",") {
				value, err := strconv.Atoi(strings.TrimSpace(part))
				if err != nil || value <= 0 {
					return errors.New("IDs must be positive integers")
				}
				*dimension.values = append(*dimension.values, value)
			}
			return nil
		})
	}
	flags.Func("model", "exact configured model names, comma-separated or repeatable (before mapping)", func(raw string) error {
		for part := range strings.SplitSeq(raw, ",") {
			value := strings.TrimSpace(part)
			if value == "" || len(value) > 255 {
				return errors.New("invalid model filter")
			}
			options.Models = append(options.Models, value)
		}
		return nil
	})
	flags.StringVar(&options.Status, "status", "all", "channel status: all, enabled, disabled")
	flags.StringVar(&options.Config.Format, "format", "chat", "OpenAI protocol: chat or responses; native Anthropic is automatic")
	flags.DurationVar(&options.Config.Timeout, "timeout", 30*time.Second, "per-request timeout (1s..300s)")
	flags.IntVar(&options.Config.Concurrency, "concurrency", defaultDegradationConcurrency, "concurrent probes (1..64); defaults to DEGRADATION_CHECK_CONCURRENCY or 8")
	flags.BoolVar(&options.Config.AllowHTTP, "allow-http", false, "explicitly allow plaintext HTTP to operator-managed endpoints")
	flags.BoolVar(&options.DryRun, "dry-run", false, "list selected targets without requests or result/task writes")
	flags.BoolVar(&help, "help", false, "show usage")
	flags.BoolVar(&help, "h", false, "show usage")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "invalid degradation-check arguments; use --help")
		return 2
	}
	if help {
		fmt.Fprintln(stdout, "Usage: newapi degradation-check [--provider-id IDs] [--channel-id IDs] [--model NAMES] [options]")
		flags.SetOutput(stdout)
		flags.PrintDefaults()
		fmt.Fprintln(stdout, "Defaults to all live providers, all channels (including disabled), and every distinct channel model. Uses existing SQL_DSN/SQLITE_PATH and channel credentials. Reads .env. Executes once, independently of the timer switch. JSONL results and a final summary; exit 0 all normal, 1 abnormal, 2 incomplete/error/empty. Unsupported targets are explicitly skipped. Heuristic only; real upstream requests may cost money.")
		return 0
	}
	concurrencyExplicit := false
	flags.Visit(func(option *flag.Flag) {
		if option.Name == "concurrency" {
			concurrencyExplicit = true
		}
	})
	if !slices.Contains([]string{"all", "enabled", "disabled"}, options.Status) || !slices.Contains([]string{"chat", "responses"}, options.Config.Format) || options.Config.Timeout < time.Second || options.Config.Timeout > 300*time.Second || options.Config.Concurrency < 1 || options.Config.Concurrency > 64 {
		fmt.Fprintln(stderr, "invalid scan configuration; use --help")
		return 2
	}
	if model.DB == nil {
		if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(stderr, "could not load .env")
			return 2
		}
	}
	if !concurrencyExplicit {
		value, err := degradationConcurrencyFromEnv()
		if err != nil {
			fmt.Fprintln(stderr, err.Error())
			return 2
		}
		options.Config.Concurrency = value
	}
	if model.DB == nil {
		closeDB, err := model.OpenDegradationDatabase()
		if err != nil {
			fmt.Fprintln(stderr, "could not open existing primary database; check configuration and run the upgraded service migrations first")
			return 2
		}
		defer closeDB()
	}
	var summary DegradationScanSummary
	var scanErr error
	if options.DryRun {
		summary, scanErr = RunDegradationScan(ctx, options, "", "", stdout)
	} else {
		runnerID := "degradation-cli-" + uuid.NewString()
		task, err := model.BeginDegradationScan(ctx, runnerID, options, systemTaskLockUntil())
		if err != nil {
			fmt.Fprintln(stderr, "could not acquire degradation task; another scan or periodic job may be pending/running")
			return 2
		}
		runWithLeaseHeartbeat(task, runnerID, func(leaseCtx context.Context) {
			runCtx, cancel := context.WithCancel(ctx)
			stop := context.AfterFunc(leaseCtx, cancel)
			defer cancel()
			defer stop()
			summary, scanErr = RunDegradationScan(runCtx, options, task.TaskID, runnerID, stdout)
		})
		status, message := model.SystemTaskStatusSucceeded, ""
		if scanErr != nil {
			status, message = model.SystemTaskStatusFailed, "degradation scan incomplete"
		}
		if err := model.FinishSystemTask(task.TaskID, runnerID, status, summary, message); err != nil {
			scanErr = err
		}
	}
	if scanErr != nil {
		fmt.Fprintln(stderr, "degradation scan incomplete; partial observations already written remain available")
	}
	data, err := common.Marshal(summary)
	if err != nil {
		return 2
	}
	if _, err := fmt.Fprintln(stdout, string(data)); err != nil {
		return 2
	}
	if scanErr != nil || summary.Targets == 0 || (!options.DryRun && (summary.Errors > 0 || summary.Skipped > 0)) {
		return 2
	}
	if summary.Abnormal > 0 {
		return 1
	}
	return 0
}

// RunDegradationScan walks every page once up to the initial channel ID ceiling.
// The periodic batch limit is deliberately NOT a total scan limit. Channel
// configuration is captured once per channel, without holding locks over HTTP.
func RunDegradationScan(ctx context.Context, options DegradationScanOptions, taskID, runnerID string, output io.Writer) (DegradationScanSummary, error) {
	summary := DegradationScanSummary{Kind: "summary", TaskID: taskID, DryRun: options.DryRun}
	policy, err := model.LoadDegradationModelPolicy(ctx)
	if err != nil {
		return summary, err
	}
	activeProviders := model.DB.Model(&model.Provider{}).Where("skip_degradation_check = ? OR skip_degradation_check IS NULL", false).Select("id")
	query := model.DB.WithContext(ctx).Model(&model.Channel{}).Where("provider_id IN (?) AND (skip_degradation_check = ? OR skip_degradation_check IS NULL)", activeProviders, false)
	if len(options.ProviderIDs) > 0 {
		query = query.Where("provider_id IN ?", options.ProviderIDs)
	}
	if len(options.ChannelIDs) > 0 {
		query = query.Where("id IN ?", options.ChannelIDs)
	}
	switch options.Status {
	case "enabled":
		query = query.Where("status = ?", common.ChannelStatusEnabled)
	case "disabled":
		query = query.Where("status <> ?", common.ChannelStatusEnabled)
	}
	var ceiling int
	if err := query.Session(&gorm.Session{}).Select("COALESCE(MAX(id), 0)").Scan(&ceiling).Error; err != nil {
		return summary, err
	}
	cursor := 0
	for {
		if ctx.Err() != nil {
			return summary, ctx.Err()
		}
		var channels []model.Channel
		if err := query.Session(&gorm.Session{}).Where("id > ? AND id <= ?", cursor, ceiling).Order("id ASC").Limit(100).Find(&channels).Error; err != nil {
			return summary, err
		}
		if len(channels) == 0 {
			return summary, nil
		}
		pageStartedAt := time.Now().UnixMilli()
		indices := make(map[int]map[string]int)
		var targets []DegradationTarget
		if !options.DryRun {
			for i := range channels {
				channel := &channels[i]
				indices[channel.Id] = make(map[string]int)
				for raw := range strings.SplitSeq(channel.Models, ",") {
					name := strings.TrimSpace(raw)
					if name == "" || (len(options.Models) > 0 && !slices.Contains(options.Models, name)) {
						continue
					}
					if _, exists := indices[channel.Id][name]; exists {
						continue
					}
					target, _, skip := channelDegradationTarget(channel, options.Config, name, policy.Enabled)
					if skip == "" {
						indices[channel.Id][name] = len(targets)
						targets = append(targets, target)
					}
				}
			}
		}
		observations, err := probeDegradationTargets(ctx, targets, options.Config.Concurrency, nil)
		if err != nil {
			return summary, err
		}
		for _, channel := range channels {
			cursor = channel.Id
			names := make([]string, 0)
			seen := make(map[string]bool)
			for raw := range strings.SplitSeq(channel.Models, ",") {
				name := strings.TrimSpace(raw)
				if name != "" && !seen[name] && (len(options.Models) == 0 || slices.Contains(options.Models, name)) {
					names = append(names, name)
					seen[name] = true
				}
			}
			if len(names) == 0 && len(options.Models) > 0 {
				continue
			}
			summary.Channels++
			if len(names) == 0 {
				names = append(names, "")
			}
			normal, abnormal, failed, skipped := 0, 0, 0, 0
			startedAt, completedAt := pageStartedAt, int64(0)
			var ttft *int64
			for _, name := range names {
				if ctx.Err() != nil {
					return summary, ctx.Err()
				}
				summary.Targets++
				event := degradationScanEvent{Kind: "result", TaskID: taskID, ProviderID: channel.ProviderID, ChannelID: channel.Id, Model: name}
				target, _, skip := channelDegradationTarget(&channel, options.Config, name, policy.Enabled)
				if name == "" {
					skip = "no_models"
				}
				switch {
				case skip != "":
					event.State, event.Reason = "skipped", skip
					summary.Skipped++
					if skip != "model_check_disabled" {
						skipped++
					}
				case options.DryRun:
					event.State = "selected"
				default:
					observation := observations[indices[channel.Id][name]]
					if observation.State != "error" && observation.TTFTMs != nil && (ttft == nil || *observation.TTFTMs > *ttft) {
						value := *observation.TTFTMs
						ttft = &value
					}
					if ctx.Err() != nil {
						return summary, ctx.Err()
					}
					event.State, event.Observation = observation.State, &observation
					sample := model.ChannelDegradationSample{
						ID: observation.ID, TaskID: taskID, Model: name, UpstreamModel: target.Model,
						RuleVersion: observation.RuleVersion, State: observation.State, MatchedMarker: observation.MatchedMarker,
						Protocol: target.Format, OutputTokenLimit: degradationOutputTokens, TimeoutMs: target.Timeout.Milliseconds(),
						ErrorCode: observation.ErrorCode, HTTPStatus: observation.HTTPStatus,
						StartedAt: observation.StartedAt, CompletedAt: observation.CompletedAt, DurationMs: observation.DurationMs, TTFTMs: observation.TTFTMs,
					}
					if err := model.RecordChannelDegradation(ctx, &channel, &sample, runnerID, true); err != nil {
						return summary, err
					}
					completedAt = max(completedAt, observation.CompletedAt)
					switch observation.State {
					case "normal":
						normal++
						summary.Normal++
					case "abnormal":
						abnormal++
						summary.Abnormal++
					default:
						failed++
						summary.Errors++
					}
				}
				encoded, err := common.Marshal(event)
				if err != nil {
					return summary, err
				}
				if _, err := fmt.Fprintln(output, string(encoded)); err != nil {
					return summary, err
				}
			}
			// No last-model-wins: only a complete, unfiltered channel evaluation may
			// publish an aggregate. Any abnormal model wins over normal models; errors
			// without an abnormal verdict remain unknown. Skips preserve the old snapshot.
			if !options.DryRun && channel.Status == common.ChannelStatusEnabled && len(options.Models) == 0 && skipped == 0 && completedAt > 0 {
				var degraded *bool
				if abnormal > 0 || failed == 0 {
					value := abnormal > 0
					degraded = &value
				}
				applied, err := model.ApplyDegradationScanSummary(ctx, &channel, taskID, runnerID, startedAt, completedAt, degraded, normal+abnormal > 0, ttft)
				if err != nil {
					return summary, err
				}
				if applied {
					summary.SnapshotApplied++
				}
			}
		}
	}
}
