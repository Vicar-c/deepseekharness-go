// Package profiles resolves the composition the harness boots: which plugin
// rows are present, which of them are enabled, and where every global setting
// came from.
//
// The layering mirrors the upstream Cordis loader while staying inside the
// standard library: bundles are compile-time compositions, a profile selects an
// ordered list of them, and JSON patch files replace whole rows by id. The
// resolved tree is dumpable with `--dump-config`, including per-setting
// provenance.
package profiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Vicar-c/deepseekharness-go/internal/config"
)

// DefaultProfile is used when no profile is selected on the command line.
const DefaultProfile = "default"

// RuntimeRowID is the synthetic row that carries global settings. Every patch
// layer may address it; settings merge per field instead of replacing the whole
// object, because each field is reported with its own source.
const RuntimeRowID = "runtime"

// TransportRowPrefix marks rows that declare a transport rather than a plugin
// instance. They are resolved by the CLI, not by the kernel.
const TransportRowPrefix = "transport."

// Row is one plugin row in the resolved tree.
type Row struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Disabled bool   `json:"disabled,omitempty"`
	Source   string `json:"source"`
}

// Setting is one global setting contributed by one layer.
type Setting struct {
	Field  string          `json:"field"`
	Value  json.RawMessage `json:"value"`
	Source string          `json:"source"`
}

// Tree is the fully layered composition of one profile.
type Tree struct {
	Profile  string
	Bundles  []string
	Layers   []string
	Rows     []Row
	Settings []Setting
}

// Enabled reports whether the row with this id exists and is not disabled.
func (t *Tree) Enabled(id string) bool {
	for _, row := range t.Rows {
		if row.ID == id {
			return !row.Disabled
		}
	}
	return false
}

// PluginRows returns the enabled rows that name a kernel plugin.
func (t *Tree) PluginRows() []Row {
	rows := make([]Row, 0, len(t.Rows))
	for _, row := range t.Rows {
		if row.Disabled || strings.HasPrefix(row.ID, TransportRowPrefix) {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

// EffectiveSettings returns the winning value of every setting, in field order.
func (t *Tree) EffectiveSettings() []Setting {
	latest := make(map[string]Setting, len(t.Settings))
	for _, setting := range t.Settings {
		latest[setting.Field] = setting
	}
	effective := make([]Setting, 0, len(latest))
	for _, field := range config.Fields() {
		if setting, ok := latest[field]; ok {
			effective = append(effective, setting)
		}
	}
	return effective
}

// ApplySettings writes the layered settings into cfg, recording the layer that
// supplied each one. Later layers win field by field.
func (t *Tree) ApplySettings(cfg *config.Config) error {
	for _, setting := range t.Settings {
		var value any
		if err := json.Unmarshal(setting.Value, &value); err != nil {
			return fmt.Errorf("profiles: setting %s from %s: %w", setting.Field, setting.Source, err)
		}
		if err := cfg.Set(setting.Field, value, setting.Source); err != nil {
			return fmt.Errorf("profiles: setting %s from %s: %w", setting.Field, setting.Source, err)
		}
	}
	return nil
}

// Options selects the profile and the extra patch layers.
type Options struct {
	// Name is the profile name; empty means DefaultProfile.
	Name string
	// Home is the harness data directory; empty resolves Home().
	Home string
	// Patches are extra patch files applied after the profile and home layers.
	Patches []string
}

// Home returns the harness data directory holding profiles and the home patch.
// It is the directory of the config file, so DSH_CONFIG keeps a redirected setup
// self-contained.
//
// The upstream DSH_HOME variable is deliberately NOT read here: the installed
// desktop app exports it as ~/.dsh, and honouring it would make the Go rewrite
// read and merge the other product's profiles, breaking the strict data
// directory isolation of D-0005.
func Home() string {
	if path := config.FilePath(); path != "" {
		return filepath.Dir(path)
	}
	return config.DataDirName
}

// Resolve layers the profile and returns the resulting tree.
func Resolve(options Options) (*Tree, error) {
	name := strings.TrimSpace(options.Name)
	if name == "" {
		name = DefaultProfile
	}
	home := options.Home
	if home == "" {
		home = Home()
	}
	dir := filepath.Join(home, "profiles", name)

	bundles, profilePatch, err := loadProfile(name, dir)
	if err != nil {
		return nil, err
	}

	tree := &Tree{Profile: name, Bundles: bundles}
	for _, bundleName := range bundles {
		bundle, ok := bundlesByName[bundleName]
		if !ok {
			return nil, fmt.Errorf("profiles: profile %q selects unknown bundle %q (known: %s)",
				name, bundleName, strings.Join(BundleNames(), ", "))
		}
		source := "bundle:" + bundle.name
		tree.Layers = append(tree.Layers, source)
		if err := tree.apply(bundle.insert, source); err != nil {
			return nil, err
		}
	}

	if profilePatch != "" {
		entries, err := readPatch(profilePatch)
		if err != nil {
			return nil, err
		}
		source := "profile:" + name
		tree.Layers = append(tree.Layers, source)
		if err := tree.apply(entries, source); err != nil {
			return nil, fmt.Errorf("%s: %w", profilePatch, err)
		}
	}

	homePatch := filepath.Join(home, "cordis.patch.json")
	if exists(homePatch) {
		entries, err := readPatch(homePatch)
		if err != nil {
			return nil, err
		}
		tree.Layers = append(tree.Layers, "home")
		if err := tree.apply(entries, "home"); err != nil {
			return nil, fmt.Errorf("%s: %w", homePatch, err)
		}
	}

	for _, path := range options.Patches {
		if strings.TrimSpace(path) == "" {
			continue
		}
		entries, err := readPatch(path)
		if err != nil {
			return nil, err
		}
		source := "patch:" + path
		tree.Layers = append(tree.Layers, source)
		if err := tree.apply(entries, source); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return tree, nil
}

// loadProfile resolves a profile into its ordered bundles and its local patch
// file. A user profile directory ($DSH_HOME/profiles/<name>) wins over the
// built-in definitions of the same name.
func loadProfile(name, dir string) (bundles []string, patchPath string, err error) {
	manifest := filepath.Join(dir, "package.json")
	data, readErr := os.ReadFile(manifest)
	switch {
	case readErr == nil:
		data = config.StripBOM(data)
		var document struct {
			Name string `json:"name"`
			DSH  struct {
				Profile struct {
					Bundles []string `json:"bundles"`
				} `json:"profile"`
			} `json:"dsh"`
		}
		if err := json.Unmarshal(data, &document); err != nil {
			return nil, "", fmt.Errorf("profiles: parse %s: %w", manifest, err)
		}
		bundles = document.DSH.Profile.Bundles
		if len(bundles) == 0 {
			return nil, "", fmt.Errorf("profiles: %s declares no dsh.profile.bundles", manifest)
		}
		candidate := filepath.Join(dir, "cordis.patch.json")
		if exists(candidate) {
			patchPath = candidate
		}
		return bundles, patchPath, nil
	case !errors.Is(readErr, os.ErrNotExist):
		return nil, "", fmt.Errorf("profiles: read %s: %w", manifest, readErr)
	}

	bundles, ok := builtinProfiles[name]
	if !ok {
		return nil, "", fmt.Errorf("profiles: unknown profile %q (built-in: %s; user profiles live in %s)",
			name, strings.Join(ProfileNames(), ", "), dir)
	}
	return bundles, "", nil
}

// rowSpec is one row as written in a patch file. Setting fields are pointers so
// "omitted" and "false" stay distinguishable.
type rowSpec struct {
	ID       string          `json:"id"`
	Name     string          `json:"name,omitempty"`
	Disabled *bool           `json:"disabled,omitempty"`
	Config   json.RawMessage `json:"config,omitempty"`
}

// patchEntry is one entry of a patch file: either an insert or a row addressed
// by id, matching the upstream `cordis.patch.yml` shape.
type patchEntry struct {
	Insert []rowSpec `json:"insert,omitempty"`
	rowSpec
}

func readPatch(path string) ([]patchEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("profiles: read patch %s: %w", path, err)
	}
	var entries []patchEntry
	if err := json.Unmarshal(config.StripBOM(data), &entries); err != nil {
		return nil, fmt.Errorf("profiles: parse patch %s: %w", path, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("profiles: patch %s is empty", path)
	}
	return entries, nil
}

// apply folds one patch layer into the tree.
func (t *Tree) apply(entries []patchEntry, source string) error {
	for _, entry := range entries {
		switch {
		case len(entry.Insert) > 0:
			for _, spec := range entry.Insert {
				if err := t.insert(spec, source); err != nil {
					return err
				}
			}
		case entry.ID == "":
			return fmt.Errorf("profiles: patch entry from %s has neither an insert nor an id", source)
		default:
			if err := t.edit(entry.rowSpec, source); err != nil {
				return err
			}
		}
	}
	return nil
}

// insert adds a row, or replaces the row with the same id in place so the row
// order of earlier layers survives an override.
func (t *Tree) insert(spec rowSpec, source string) error {
	if strings.TrimSpace(spec.ID) == "" {
		return fmt.Errorf("profiles: row from %s has no id", source)
	}
	if spec.ID == RuntimeRowID {
		return t.addSettings(spec.Config, source)
	}
	if strings.TrimSpace(spec.Name) == "" {
		return fmt.Errorf("profiles: row %q from %s has no name", spec.ID, source)
	}
	disabled := spec.Disabled != nil && *spec.Disabled
	row := Row{ID: spec.ID, Name: spec.Name, Disabled: disabled, Source: source}
	for i := range t.Rows {
		if t.Rows[i].ID == spec.ID {
			t.Rows[i] = row
			return nil
		}
	}
	t.Rows = append(t.Rows, row)
	return nil
}

// edit applies a patch entry that addresses an existing row by id.
func (t *Tree) edit(spec rowSpec, source string) error {
	if spec.ID == RuntimeRowID {
		return t.addSettings(spec.Config, source)
	}
	for i := range t.Rows {
		if t.Rows[i].ID != spec.ID {
			continue
		}
		if len(spec.Config) > 0 {
			// Plugin rows carry no configurable fields yet. Ignoring the value
			// would make a typo silently useless, so it is rejected loudly.
			return fmt.Errorf("profiles: row %q does not accept config in this build", spec.ID)
		}
		if spec.Disabled != nil {
			t.Rows[i].Disabled = *spec.Disabled
		}
		t.Rows[i].Source = source
		return nil
	}
	return fmt.Errorf("profiles: patch from %s addresses unknown row %q (known: %s)",
		source, spec.ID, strings.Join(t.rowIDs(), ", "))
}

// addSettings records the per-field settings of a runtime entry.
func (t *Tree) addSettings(spec json.RawMessage, source string) error {
	if len(spec) == 0 {
		return fmt.Errorf("profiles: runtime row from %s has no config", source)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(spec, &values); err != nil {
		return fmt.Errorf("profiles: runtime config from %s: %w", source, err)
	}
	if len(values) == 0 {
		return fmt.Errorf("profiles: runtime config from %s is empty", source)
	}
	fields := make([]string, 0, len(values))
	for field := range values {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		if !config.IsField(field) {
			return fmt.Errorf("profiles: runtime config from %s sets unknown setting %q (known: %s)",
				source, field, strings.Join(config.Fields(), ", "))
		}
		t.Settings = append(t.Settings, Setting{Field: field, Value: values[field], Source: source})
	}
	return nil
}

func (t *Tree) rowIDs() []string {
	ids := make([]string, 0, len(t.Rows))
	for _, row := range t.Rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
