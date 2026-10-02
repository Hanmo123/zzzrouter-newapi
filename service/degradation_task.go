package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

type DegradationTaskConfig struct {
	Enabled     bool
	Interval    time.Duration
	Timeout     time.Duration
	BatchSize   int
	Concurrency int
	AllowHTTP   bool
	Format      string
}

func DegradationConfigFromEnv() (DegradationTaskConfig, error) {
	config := DegradationTaskConfig{Interval: time.Hour, Timeout: 30 * time.Second, BatchSize: 100, Concurrency: defaultDegradationConcurrency, Format: "chat"}
	if raw, present := os.LookupEnv("DEGRADATION_CHECK_ENABLED"); present {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return config, errors.New("invalid DEGRADATION_CHECK_ENABLED")
		}
		config.Enabled = value
	}
	if !config.Enabled {
		return config, nil
	}
	concurrency, err := degradationConcurrencyFromEnv()
	if err != nil {
		return config, err
	}
	config.Concurrency = concurrency
	minutes, seconds := 60, 30
	for _, option := range []struct {
		name        string
		max         int
		destination *int
	}{
		{"DEGRADATION_CHECK_INTERVAL_MINUTES", 10080, &minutes},
		{"DEGRADATION_CHECK_TIMEOUT_SECONDS", 300, &seconds},
		{"DEGRADATION_CHECK_BATCH_SIZE", 1000, &config.BatchSize},
	} {
		if raw, present := os.LookupEnv(option.name); present {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > option.max {
				return config, fmt.Errorf("invalid %s", option.name)
			}
			*option.destination = value
		}
	}
	config.Interval, config.Timeout = time.Duration(minutes)*time.Minute, time.Duration(seconds)*time.Second
	if raw, present := os.LookupEnv("DEGRADATION_CHECK_ALLOW_HTTP"); present {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return config, errors.New("invalid DEGRADATION_CHECK_ALLOW_HTTP")
		}
		config.AllowHTTP = value
	}
	if raw, present := os.LookupEnv("DEGRADATION_CHECK_FORMAT"); present {
		if raw != "chat" && raw != "responses" {
			return config, errors.New("invalid DEGRADATION_CHECK_FORMAT")
		}
		config.Format = raw
	}
	return config, nil
}

// Reuse the system-task scheduler, cross-master DB lease and cancellation, not
// another independent ticker. Disabled masters must not claim enabled peers' jobs.
func RegisterChannelDegradationTask() {
	if !common.IsMasterNode {
		return
	}
	config, err := DegradationConfigFromEnv()
	if err != nil {
		common.SysError("channel degradation task disabled: " + err.Error())
		return
	}
	if config.Enabled {
		RegisterSystemTaskHandler(channelDegradationHandler{})
	}
}

type channelDegradationHandler struct{}

type degradationTaskPayload struct {
	AfterID int `json:"after_id"`
}

type degradationTaskResult struct {
	Cursor          int            `json:"cursor"`
	Processed       int            `json:"processed"`
	Normal          int            `json:"normal"`
	Abnormal        int            `json:"abnormal"`
	Errors          int            `json:"errors"`
	Skipped         int            `json:"skipped"`
	SnapshotApplied int            `json:"snapshot_applied"`
	SkipReasons     map[string]int `json:"skip_reasons"`
}

func (channelDegradationHandler) Type() string { return model.SystemTaskTypeChannelDegradation }
func (channelDegradationHandler) Enabled() bool {
	config, err := DegradationConfigFromEnv()
	return common.IsMasterNode && err == nil && config.Enabled
}
func (channelDegradationHandler) Interval() time.Duration {
	config, err := DegradationConfigFromEnv()
	if err != nil {
		return time.Hour
	}
	return config.Interval
}
func (channelDegradationHandler) NewPayload() any {
	// Bounded round-robin batches avoid both unbounded provider spend and
	// starvation when early channels have unsupported configurations.
	payload := degradationTaskPayload{}
	latest, err := model.GetLatestSystemTasks([]string{model.SystemTaskTypeChannelDegradation})
	if err == nil && latest[model.SystemTaskTypeChannelDegradation] != nil {
		var previous degradationTaskResult
		if common.UnmarshalJsonStr(latest[model.SystemTaskTypeChannelDegradation].Result, &previous) == nil && previous.Cursor > 0 {
			payload.AfterID = previous.Cursor
		}
	}
	return payload
}

func (handler channelDegradationHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	config, err := DegradationConfigFromEnv()
	status, message := model.SystemTaskStatusSucceeded, ""
	result := degradationTaskResult{SkipReasons: make(map[string]int)}
	if err != nil || !config.Enabled || !common.IsMasterNode {
		result.SkipReasons["disabled_or_invalid_configuration"] = 1
	} else {
		var payload degradationTaskPayload
		if task.DecodePayload(&payload) != nil || payload.AfterID < 0 {
			status, message = model.SystemTaskStatusFailed, "invalid degradation task payload"
		} else {
			result, err = runChannelDegradationBatch(ctx, task.TaskID, runnerID, payload.AfterID, config, NewSystemTaskProgressReporter(task, runnerID))
			if err != nil {
				status, message = model.SystemTaskStatusFailed, "degradation batch could not complete"
			}
		}
	}
	if err := model.FinishSystemTask(task.TaskID, runnerID, status, result, message); err != nil {
		common.SysError("could not finish channel degradation task")
	}
}

func runChannelDegradationBatch(ctx context.Context, taskID, runnerID string, afterID int, config DegradationTaskConfig, progress func(int, int)) (degradationTaskResult, error) {
	result := degradationTaskResult{Cursor: afterID, SkipReasons: make(map[string]int)}
	activeProviders := model.DB.Model(&model.Provider{}).Where("skip_degradation_check = ? OR skip_degradation_check IS NULL", false).Select("id")
	eligible := model.DB.WithContext(ctx).Model(&model.Channel{}).
		Where("status = ? AND type IN ? AND provider_id IN (?) AND (skip_degradation_check = ? OR skip_degradation_check IS NULL)", common.ChannelStatusEnabled,
			[]int{constant.ChannelTypeOpenAI, constant.ChannelTypeAnthropic, constant.ChannelTypeOpenRouter, constant.ChannelTypeNewAPI, constant.ChannelTypeVLLM, constant.ChannelTypeSGLang, constant.ChannelTypeAdvancedCustom}, activeProviders, false)
	policy, err := model.LoadDegradationModelPolicy(ctx)
	if err != nil {
		return result, err
	}
	var ids []int
	if err := eligible.Where("id > ?", afterID).Order("id ASC").Limit(config.BatchSize).Pluck("id", &ids).Error; err != nil {
		return result, err
	}
	if len(ids) == 0 && afterID > 0 {
		// Construct a fresh query; don't retain the old id > cursor predicate.
		return runChannelDegradationBatch(ctx, taskID, runnerID, 0, config, progress)
	}
	if progress != nil {
		progress(0, len(ids))
	}
	channels := make([]model.Channel, len(ids))
	targets := make([]DegradationTarget, 0, len(ids))
	names, skips := make([]string, len(ids)), make([]string, len(ids))
	indices := make([]int, len(ids))
	for i, id := range ids {
		if err := model.DB.WithContext(ctx).First(&channels[i], id).Error; err != nil {
			return result, err
		}
		target, name, skip := channelDegradationTarget(&channels[i], config, "", policy.Enabled)
		if channels[i].Status != common.ChannelStatusEnabled {
			skip = "channel_disabled"
		} else if channels[i].SkipDegradationCheck {
			skip = "channel_check_disabled"
		}
		names[i], skips[i], indices[i] = name, skip, len(targets)
		if skip == "" {
			targets = append(targets, target)
		}
	}
	observations, probeErr := probeDegradationTargets(ctx, targets, config.Concurrency, func() bool { return common.GetEnvOrDefaultBool("DEGRADATION_CHECK_ENABLED", false) })
	for i, id := range ids {
		channel, publicModel, skip := channels[i], names[i], skips[i]
		if skip != "" {
			result.Skipped++
			result.SkipReasons[skip]++
		} else {
			target, observation := targets[indices[i]], observations[indices[i]]
			if observation.ID == "" {
				continue // Never dispatched: there is no evaluation to invent.
			}
			historyOnly := probeErr != nil || !common.GetEnvOrDefaultBool("DEGRADATION_CHECK_ENABLED", false)
			sample, writeErr := recordDegradationObservation(ctx, &channel, publicModel, target, observation, taskID, runnerID, historyOnly)
			if writeErr != nil {
				probeErr = errors.Join(probeErr, writeErr)
			}
			if sample.SnapshotApplied {
				result.SnapshotApplied++
			}
			switch observation.ErrorCode {
			case "invalid_target", "invalid_format", "invalid_proxy", "invalid_request":
				result.Skipped++
				result.SkipReasons[observation.ErrorCode]++
			default:
				switch observation.State {
				case "normal":
					result.Normal++
				case "abnormal":
					result.Abnormal++
				default:
					result.Errors++
				}
			}
		}
		if probeErr == nil && ctx.Err() == nil && common.GetEnvOrDefaultBool("DEGRADATION_CHECK_ENABLED", false) {
			result.Cursor, result.Processed = id, result.Processed+1
			if progress != nil {
				progress(result.Processed, len(ids))
			}
		}
	}
	if !common.GetEnvOrDefaultBool("DEGRADATION_CHECK_ENABLED", false) {
		probeErr = errors.Join(probeErr, errors.New("degradation task disabled"))
	}
	return result, errors.Join(probeErr, ctx.Err())
}

// An image-generation endpoint cannot answer the text-only refusal prompt.
func knownImageModel(name string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "gpt-image-") || strings.HasPrefix(name, "dall-e-")
}

// Probe a representative model/key, not every model or credential on a channel.
// Do not pretend to support configurations that rewrite the canonical prompt,
// authentication or request body; those are skipped rather than called degraded.
func channelDegradationTarget(channel *model.Channel, config DegradationTaskConfig, publicModel string, modelAllowed ...func(string) bool) (DegradationTarget, string, string) {
	allowed := func(name string) bool { return true }
	if len(modelAllowed) > 0 && modelAllowed[0] != nil {
		allowed = modelAllowed[0]
	}
	target := DegradationTarget{Timeout: config.Timeout, AllowHTTP: config.AllowHTTP, Format: config.Format}
	switch channel.Type {
	case constant.ChannelTypeOpenAI, constant.ChannelTypeOpenRouter, constant.ChannelTypeNewAPI, constant.ChannelTypeVLLM, constant.ChannelTypeSGLang:
	case constant.ChannelTypeAnthropic:
		target.Format = "anthropic"
	case constant.ChannelTypeAdvancedCustom:
	default:
		return target, "", "unsupported_channel_type"
	}
	settings := dto.ChannelSettings{}
	if channel.Setting != nil && strings.TrimSpace(*channel.Setting) != "" {
		if common.UnmarshalJsonStr(*channel.Setting, &settings) != nil {
			return target, "", "unsupported_configuration"
		}
	}
	if settings.SystemPrompt != "" || settings.SystemPromptOverride || settings.ThinkingToContent || settings.ResponsesWebSocketEnabled || len(settings.TaskPluginBindings()) != 0 || settings.ValidateHTTPTransport() != nil {
		return target, "", "unsupported_configuration"
	}
	for _, raw := range []*string{channel.ParamOverride, channel.HeaderOverride} {
		if raw != nil && strings.TrimSpace(*raw) != "" {
			var overrides map[string]any
			if common.UnmarshalJsonStr(*raw, &overrides) != nil || len(overrides) != 0 {
				return target, "", "unsupported_configuration"
			}
		}
	}
	other := dto.ChannelOtherSettings{}
	if channel.OtherSettings != "" && common.UnmarshalJsonStr(channel.OtherSettings, &other) != nil {
		return target, "", "unsupported_configuration"
	}
	if other.ClaudeBetaQuery || other.IsOpenRouterEnterprise() {
		return target, "", "unsupported_configuration"
	}
	if publicModel == "" && channel.TestModel != nil {
		candidate := strings.TrimSpace(*channel.TestModel)
		if slices.ContainsFunc(channel.GetModels(), func(name string) bool { return strings.TrimSpace(name) == candidate }) && allowed(candidate) && (channel.Type != constant.ChannelTypeAdvancedCustom || !knownImageModel(candidate)) {
			publicModel = candidate
		}
	}
	if publicModel == "" {
		for name := range strings.SplitSeq(channel.Models, ",") {
			if name = strings.TrimSpace(name); name != "" && allowed(name) && (channel.Type != constant.ChannelTypeAdvancedCustom || !knownImageModel(name)) {
				publicModel = name
				break
			}
		}
	}
	if publicModel == "" || len(publicModel) > 255 {
		return target, publicModel, "no_enabled_model"
	}
	if !allowed(publicModel) {
		return target, publicModel, "model_check_disabled"
	}
	target.Model = publicModel
	mapping := map[string]string{}
	if channel.ModelMapping != nil && strings.TrimSpace(*channel.ModelMapping) != "" {
		if common.UnmarshalJsonStr(*channel.ModelMapping, &mapping) != nil {
			return target, publicModel, "invalid_model_mapping"
		}
	}
	visited := make(map[string]bool)
	for {
		visited[target.Model] = true
		next := mapping[target.Model]
		if next == "" || next == target.Model {
			break
		}
		if visited[next] {
			return target, publicModel, "invalid_model_mapping"
		}
		target.Model = next
	}
	if channel.ChannelInfo.IsMultiKey {
		for index, key := range channel.GetKeys() {
			status, set := channel.ChannelInfo.MultiKeyStatusList[index]
			if (!set || status == common.ChannelStatusEnabled) && strings.TrimSpace(key) != "" {
				target.APIKey = strings.TrimSpace(key)
				break
			}
		}
	} else {
		target.APIKey = strings.TrimSpace(channel.Key)
	}
	if target.APIKey == "" && channel.Type != constant.ChannelTypeAdvancedCustom {
		return target, publicModel, "no_enabled_key"
	}
	if strings.HasPrefix(target.APIKey, "{") || strings.HasPrefix(target.APIKey, "[") || strings.HasPrefix(target.APIKey, `"`) {
		return target, publicModel, "unsupported_credentials"
	}
	path := "/v1/chat/completions"
	if target.Format == "responses" {
		path = "/v1/responses"
	}
	if target.Format == "anthropic" {
		path = "/v1/messages"
	}
	if channel.Type == constant.ChannelTypeAdvancedCustom {
		if knownImageModel(publicModel) {
			return target, publicModel, "unsupported_model_modality"
		}
		if other.AdvancedCustom == nil || other.AdvancedCustom.Validate() != nil {
			return target, publicModel, "unsupported_configuration"
		}
		route, ok := other.AdvancedCustom.MatchPathForModel(path, publicModel)
		if !ok {
			alternate, format := "/v1/responses", "responses"
			if target.Format == "responses" {
				alternate, format = "/v1/chat/completions", "chat"
			}
			route, ok = other.AdvancedCustom.MatchPathForModel(alternate, publicModel)
			if !ok {
				return target, publicModel, "unsupported_model_route"
			}
			target.Format, path = format, alternate
		}
		if !route.IsNative() || route.PassThroughBodyEnabled {
			return target, publicModel, "unsupported_route_converter"
		}
		upstream := strings.ReplaceAll(strings.TrimSpace(route.UpstreamPath), "{model}", url.PathEscape(target.Model))
		if strings.HasPrefix(upstream, "/") {
			upstream = relaycommon.GetFullRequestURL(strings.TrimRight(channel.GetBaseURL(), "/"), upstream, channel.Type)
		}
		target.Endpoint = upstream
		if (route.Auth == nil || (route.Auth.Type != dto.AdvancedCustomAuthTypeNone && strings.Contains(route.Auth.Value, "{api_key}"))) && target.APIKey == "" {
			return target, publicModel, "no_enabled_key"
		}
		if route.Auth != nil {
			target.SkipDefaultAuth = true
			switch strings.TrimSpace(route.Auth.Type) {
			case dto.AdvancedCustomAuthTypeNone:
			case dto.AdvancedCustomAuthTypeHeader:
				target.AuthHeader = strings.TrimSpace(route.Auth.Name)
				target.AuthValue = strings.ReplaceAll(route.Auth.Value, "{api_key}", target.APIKey)
			case dto.AdvancedCustomAuthTypeQuery:
				parsed, err := url.Parse(target.Endpoint)
				if err != nil {
					return target, publicModel, "invalid_target"
				}
				query := parsed.Query()
				query.Set(strings.TrimSpace(route.Auth.Name), strings.ReplaceAll(route.Auth.Value, "{api_key}", target.APIKey))
				parsed.RawQuery = query.Encode()
				target.Endpoint, target.AllowRouteQuery = parsed.String(), true
			default:
				return target, publicModel, "unsupported_configuration"
			}
		}
		// Route URLs may include non-authentication query parameters.
		target.AllowRouteQuery = true
	} else {
		target.Endpoint = relaycommon.GetFullRequestURL(strings.TrimRight(channel.GetBaseURL(), "/"), path, channel.Type)
	}
	target.Proxy, target.TransportSettings = settings.Proxy, settings
	if channel.OpenAIOrganization != nil {
		target.Organization = *channel.OpenAIOrganization
	}
	return target, publicModel, ""
}
