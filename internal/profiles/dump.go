package profiles

import (
	"encoding/json"
	"io"

	"github.com/Vicar-c/deepseekharness-go/internal/config"
)

// Dump is the machine-readable document printed by `--dump-config`: the resolved
// plugin rows, the effective configuration and the layer each setting came from.
type Dump struct {
	Profile string            `json:"profile"`
	Bundles []string          `json:"bundles"`
	Layers  []string          `json:"layers"`
	Plugins []Row             `json:"plugins"`
	Config  config.Config     `json:"config"`
	Sources map[string]string `json:"sources"`
}

// Dump builds the dump document for the resolved tree and configuration.
func (t *Tree) Dump(cfg config.Config) Dump {
	sources := make(map[string]string, len(config.Fields()))
	for _, field := range config.Fields() {
		if source := cfg.Source(field); source != "" {
			sources[field] = source
		}
	}
	plugins := make([]Row, len(t.Rows))
	copy(plugins, t.Rows)
	return Dump{
		Profile: t.Profile,
		Bundles: append([]string{}, t.Bundles...),
		Layers:  append([]string{}, t.Layers...),
		Plugins: plugins,
		Config:  cfg,
		Sources: sources,
	}
}

// Encode writes the dump as indented JSON.
func (d Dump) Encode(w io.Writer) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(d)
}
