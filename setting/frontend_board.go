package setting

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const HomepageBoardOptionKey = "frontend.homepage_board"
const DefaultHomepageBoardModel = "gpt-6-astra"

// An empty Models list keeps the legacy behavior: show the public directory
// (up to the homepage's existing 100-item limit). A configured list is
// restricted to the homepage; the full directory remains unchanged.
type HomepageBoardConfig struct {
	Models       []string `json:"models"`
	DefaultModel string   `json:"default_model"`
}

func DefaultHomepageBoardConfig() HomepageBoardConfig {
	return HomepageBoardConfig{Models: []string{}, DefaultModel: DefaultHomepageBoardModel}
}

func ParseHomepageBoardConfig(raw string) (HomepageBoardConfig, error) {
	var config HomepageBoardConfig
	if err := common.UnmarshalJsonStr(raw, &config); err != nil {
		return config, errors.New("invalid homepage board configuration")
	}
	if len(config.Models) > 100 || config.DefaultModel == "" || len(config.DefaultModel) > 200 ||
		strings.TrimSpace(config.DefaultModel) != config.DefaultModel || strings.ContainsAny(config.DefaultModel, ",\r\n\x00") {
		return config, errors.New("invalid default model or too many homepage models (maximum 100)")
	}
	seen := make(map[string]bool, len(config.Models))
	for _, name := range config.Models {
		if name == "" || len(name) > 200 || strings.TrimSpace(name) != name || strings.ContainsAny(name, ",\r\n\x00") || seen[name] {
			return config, errors.New("homepage models must be unique, non-empty model names (one per line)")
		}
		seen[name] = true
	}
	if len(config.Models) > 0 && !seen[config.DefaultModel] {
		return config, errors.New("default model must be included in homepage models")
	}
	if config.Models == nil {
		config.Models = []string{}
	}
	return config, nil
}

func GetHomepageBoardConfig() HomepageBoardConfig {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[HomepageBoardOptionKey]
	common.OptionMapRWMutex.RUnlock()
	if raw == "" {
		return DefaultHomepageBoardConfig()
	}
	config, err := ParseHomepageBoardConfig(raw)
	if err != nil {
		return DefaultHomepageBoardConfig()
	}
	return config
}
