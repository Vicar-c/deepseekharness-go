package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Setting names as they appear in the JSON file. They double as the keys of the
// provenance map, so `--dump-config` can explain where every value came from.
const (
	FieldModel                = "model"
	FieldBaseURL              = "baseUrl"
	FieldAPIKeyEnv            = "apiKeyEnv"
	FieldWorkspace            = "workspace"
	FieldSessionDir           = "sessionDir"
	FieldMaxIterations        = "maxIterations"
	FieldRequestTimeoutSec    = "requestTimeoutSec"
	FieldToolTimeoutSec       = "toolTimeoutSec"
	FieldMaxToolOutputBytes   = "maxToolOutputBytes"
	FieldMaxParallelToolCalls = "maxParallelToolCalls"
	FieldLogLevel             = "logLevel"
	FieldLogFile              = "logFile"
	FieldMaxRetries           = "maxRetries"
)

// SettingSources are the prefixes of the provenance values recorded for each
// configuration field. A bare "default" means the built-in value survived every
// layer.
const (
	SourceDefault    = "default"
	SourceFilePrefix = "file:"
	SourceEnvPrefix  = "env:"
	SourceFlagPrefix = "flag:"
)

// Fields lists every configurable setting in documentation order.
func Fields() []string {
	return []string{
		FieldModel,
		FieldBaseURL,
		FieldAPIKeyEnv,
		FieldWorkspace,
		FieldSessionDir,
		FieldMaxIterations,
		FieldRequestTimeoutSec,
		FieldToolTimeoutSec,
		FieldMaxToolOutputBytes,
		FieldMaxParallelToolCalls,
		FieldLogLevel,
		FieldLogFile,
		FieldMaxRetries,
	}
}

// mark records where a setting came from. A nil map is created on first use so
// a zero Config can still be annotated.
func (c *Config) mark(field, source string) {
	if source == "" {
		return
	}
	if c.sources == nil {
		c.sources = make(map[string]string, len(Fields()))
	}
	c.sources[field] = source
}

// Source returns the recorded origin of one setting, or "" when unknown.
func (c Config) Source(field string) string {
	return c.sources[field]
}

// Sources returns a copy of the provenance map.
func (c Config) Sources() map[string]string {
	copied := make(map[string]string, len(c.sources))
	for field, source := range c.sources {
		copied[field] = source
	}
	return copied
}

// markDefaults records the built-in origin for every setting, so a field the
// user never touched is explained as "default" instead of being absent.
func (c *Config) markDefaults() {
	for _, field := range Fields() {
		c.mark(field, SourceDefault)
	}
}

// Set overrides one setting and records the layer that supplied it. It is the
// entry point used by the profile bundle/patch layering; unknown fields are
// rejected so a typo in a patch file fails loudly instead of being ignored.
func (c *Config) Set(field string, value any, source string) error {
	switch field {
	case FieldModel, FieldBaseURL, FieldAPIKeyEnv, FieldWorkspace, FieldSessionDir, FieldLogLevel, FieldLogFile:
		text, err := asString(field, value)
		if err != nil {
			return err
		}
		c.setString(field, text)
		c.mark(field, source)
		return nil
	case FieldMaxRetries:
		// Zero is a legitimate retry budget: it disables retries.
		number, err := asInt(field, value)
		if err != nil {
			return err
		}
		if number < 0 {
			return fmt.Errorf("config: %s must not be negative, got %d", field, number)
		}
		c.MaxRetries = number
		c.mark(field, source)
		return nil
	case FieldMaxIterations, FieldRequestTimeoutSec, FieldToolTimeoutSec, FieldMaxToolOutputBytes, FieldMaxParallelToolCalls:
		number, err := asInt(field, value)
		if err != nil {
			return err
		}
		if number <= 0 {
			return fmt.Errorf("config: %s must be a positive integer, got %d", field, number)
		}
		c.setInt(field, number)
		c.mark(field, source)
		return nil
	default:
		return fmt.Errorf("config: unknown setting %q (known: %s)", field, strings.Join(Fields(), ", "))
	}
}

func (c *Config) setString(field, value string) {
	switch field {
	case FieldModel:
		c.Model = value
	case FieldBaseURL:
		c.BaseURL = value
	case FieldAPIKeyEnv:
		c.APIKeyEnv = value
	case FieldWorkspace:
		c.Workspace = value
	case FieldSessionDir:
		c.SessionDir = value
	case FieldLogLevel:
		c.LogLevel = value
	case FieldLogFile:
		c.LogFile = value
	}
}

func (c *Config) setInt(field string, value int) {
	switch field {
	case FieldMaxIterations:
		c.MaxIterations = value
	case FieldRequestTimeoutSec:
		c.RequestTimeoutSec = value
	case FieldToolTimeoutSec:
		c.ToolTimeoutSec = value
	case FieldMaxToolOutputBytes:
		c.MaxToolOutputBytes = value
	case FieldMaxParallelToolCalls:
		c.MaxParallelToolCalls = value
	}
}

func asString(field string, value any) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("config: %s must be a string, got %T", field, value)
	}
	return text, nil
}

func asInt(field string, value any) (int, error) {
	switch typed := value.(type) {
	case float64:
		if typed != float64(int(typed)) {
			return 0, fmt.Errorf("config: %s must be an integer, got %v", field, typed)
		}
		return int(typed), nil
	case int:
		return typed, nil
	case json.Number:
		number, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("config: %s must be an integer: %w", field, err)
		}
		return int(number), nil
	case string:
		number, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, fmt.Errorf("config: %s must be an integer: %w", field, err)
		}
		return number, nil
	default:
		return 0, fmt.Errorf("config: %s must be an integer, got %T", field, value)
	}
}

// IsField reports whether name is a known configuration setting.
func IsField(name string) bool {
	for _, field := range Fields() {
		if field == name {
			return true
		}
	}
	return false
}

// StripBOM removes a leading UTF-8 byte order mark. Windows editors add one and
// encoding/json rejects it, so a hand-edited config or patch file would fail for
// a reason that has nothing to do with its content.
func StripBOM(data []byte) []byte {
	const bom = "\xef\xbb\xbf"
	if len(data) >= len(bom) && string(data[:len(bom)]) == bom {
		return data[len(bom):]
	}
	return data
}
