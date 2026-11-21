package profiles_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/config"
	"github.com/Vicar-c/deepseekharness-go/internal/profiles"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func rowByID(t *testing.T, tree *profiles.Tree, id string) profiles.Row {
	t.Helper()
	for _, row := range tree.Rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("row %q not found in %v", id, tree.Rows)
	return profiles.Row{}
}

func TestResolveDefaultProfile(t *testing.T) {
	tree, err := profiles.Resolve(profiles.Options{Home: t.TempDir()})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if tree.Profile != profiles.DefaultProfile {
		t.Fatalf("profile = %q", tree.Profile)
	}
	if got, want := strings.Join(tree.Bundles, ","), "base,web-app,headless"; got != want {
		t.Fatalf("bundles = %q, want %q", got, want)
	}
	wantLayers := []string{"bundle:base", "bundle:web-app", "bundle:headless"}
	if strings.Join(tree.Layers, ",") != strings.Join(wantLayers, ",") {
		t.Fatalf("layers = %v, want %v", tree.Layers, wantLayers)
	}
	for _, id := range []string{"tools.fs", "tools.shell", "tools.time", "tools.todo", "transport.web", "transport.cli"} {
		if !tree.Enabled(id) {
			t.Fatalf("row %q should be enabled", id)
		}
	}
	if got := rowByID(t, tree, "tools.fs").Source; got != "bundle:base" {
		t.Fatalf("tools.fs source = %q", got)
	}
	if len(tree.Settings) != 0 {
		t.Fatalf("bundles should not contribute settings, got %v", tree.Settings)
	}
}

func TestResolveNarrowsTransportsPerProfile(t *testing.T) {
	home := t.TempDir()
	headless, err := profiles.Resolve(profiles.Options{Name: "headless", Home: home})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if headless.Enabled("transport.web") {
		t.Fatal("headless profile must not enable the web transport")
	}
	if !headless.Enabled("transport.cli") {
		t.Fatal("headless profile must enable the CLI transport")
	}
	web, err := profiles.Resolve(profiles.Options{Name: "web", Home: home})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !web.Enabled("transport.web") || web.Enabled("transport.cli") {
		t.Fatalf("web profile transports: web=%v cli=%v",
			web.Enabled("transport.web"), web.Enabled("transport.cli"))
	}
}

func TestResolveUnknownProfileListsBuiltins(t *testing.T) {
	_, err := profiles.Resolve(profiles.Options{Name: "nope", Home: t.TempDir()})
	if err == nil {
		t.Fatal("expected an error for an unknown profile")
	}
	if !strings.Contains(err.Error(), "default, headless, web") {
		t.Fatalf("error should list the built-in profiles: %v", err)
	}
}

func TestUserProfileOverridesBuiltinBundles(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "profiles", "web", "package.json"),
		`{"name":"web","dsh":{"profile":{"bundles":["base"]}}}`)
	writeFile(t, filepath.Join(home, "profiles", "web", "cordis.patch.json"),
		`[{"id":"tools.shell","disabled":true}]`)

	tree, err := profiles.Resolve(profiles.Options{Name: "web", Home: home})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := strings.Join(tree.Bundles, ","); got != "base" {
		t.Fatalf("bundles = %q, want base", got)
	}
	if tree.Enabled("transport.web") {
		t.Fatal("overridden web profile should not carry the web transport")
	}
	shell := rowByID(t, tree, "tools.shell")
	if !shell.Disabled {
		t.Fatal("tools.shell should be disabled by the profile patch")
	}
	if shell.Source != "profile:web" {
		t.Fatalf("tools.shell source = %q, want profile:web", shell.Source)
	}
}

func TestPatchLayeringOrderAndProvenance(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "cordis.patch.json"),
		`[{"id":"runtime","config":{"model":"home-model","toolTimeoutSec":30}}]`)
	patch := filepath.Join(t.TempDir(), "extra.json")
	writeFile(t, patch, `[{"id":"runtime","config":{"model":"patch-model"}},
		{"id":"tools.shell","disabled":true}]`)

	tree, err := profiles.Resolve(profiles.Options{Home: home, Patches: []string{patch}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cfg := config.Default()
	if err := tree.ApplySettings(&cfg); err != nil {
		t.Fatalf("ApplySettings: %v", err)
	}
	if cfg.Model != "patch-model" {
		t.Fatalf("model = %q, want the --patch value", cfg.Model)
	}
	if cfg.ToolTimeoutSec != 30 {
		t.Fatalf("toolTimeoutSec = %d, want the home patch value to survive field-by-field merging", cfg.ToolTimeoutSec)
	}
	if got := cfg.Source(config.FieldModel); got != "patch:"+patch {
		t.Fatalf("model source = %q", got)
	}
	if got := cfg.Source(config.FieldToolTimeoutSec); got != "home" {
		t.Fatalf("toolTimeoutSec source = %q", got)
	}
	if !rowByID(t, tree, "tools.shell").Disabled {
		t.Fatal("--patch should disable tools.shell")
	}
	if got := cfg.Source(config.FieldMaxIterations); got != config.SourceDefault {
		t.Fatalf("untouched setting source = %q, want default", got)
	}
	if len(tree.EffectiveSettings()) != 2 {
		t.Fatalf("effective settings = %v", tree.EffectiveSettings())
	}
}

func TestPatchCanReEnableARow(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "cordis.patch.json"), `[{"id":"tools.shell","disabled":true}]`)
	tree, err := profiles.Resolve(profiles.Options{Home: home})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if tree.Enabled("tools.shell") {
		t.Fatal("tools.shell should be disabled")
	}
	reenable := filepath.Join(t.TempDir(), "reenable.json")
	writeFile(t, reenable, `[{"id":"tools.shell","disabled":false}]`)
	tree, err = profiles.Resolve(profiles.Options{Home: home, Patches: []string{reenable}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !tree.Enabled("tools.shell") {
		t.Fatal("a later layer should be able to re-enable the row")
	}
}

func TestPatchRejectsBadInput(t *testing.T) {
	cases := []struct {
		name    string
		patch   string
		wantErr string
	}{
		{"unknown row", `[{"id":"tools.nope","disabled":true}]`, "unknown row"},
		{"unknown setting", `[{"id":"runtime","config":{"nope":1}}]`, "unknown setting"},
		{"plugin row config", `[{"id":"tools.fs","config":{"x":1}}]`, "does not accept config"},
		{"empty entry", `[{}]`, "neither an insert nor an id"},
		{"empty patch", `[]`, "is empty"},
		{"bad json", `[`, "parse patch"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			patch := filepath.Join(t.TempDir(), "patch.json")
			writeFile(t, patch, testCase.patch)
			_, err := profiles.Resolve(profiles.Options{Home: t.TempDir(), Patches: []string{patch}})
			if err == nil {
				t.Fatalf("expected an error for %s", testCase.name)
			}
			if !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.wantErr)
			}
		})
	}
}

func TestPatchRejectsInvalidSettingValue(t *testing.T) {
	patch := filepath.Join(t.TempDir(), "patch.json")
	writeFile(t, patch, `[{"id":"runtime","config":{"maxParallelToolCalls":0}}]`)
	tree, err := profiles.Resolve(profiles.Options{Home: t.TempDir(), Patches: []string{patch}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cfg := config.Default()
	err = tree.ApplySettings(&cfg)
	if err == nil || !strings.Contains(err.Error(), "positive integer") {
		t.Fatalf("ApplySettings error = %v", err)
	}
}

func TestDumpCarriesSourcesAndRows(t *testing.T) {
	home := t.TempDir()
	patch := filepath.Join(t.TempDir(), "extra.json")
	writeFile(t, patch, `[{"id":"runtime","config":{"maxParallelToolCalls":4}},{"id":"tools.todo","disabled":true}]`)
	tree, err := profiles.Resolve(profiles.Options{Home: home, Patches: []string{patch}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cfg := config.Default()
	if err := tree.ApplySettings(&cfg); err != nil {
		t.Fatalf("ApplySettings: %v", err)
	}

	var buffer bytes.Buffer
	if err := tree.Dump(cfg).Encode(&buffer); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var document struct {
		Profile string            `json:"profile"`
		Bundles []string          `json:"bundles"`
		Layers  []string          `json:"layers"`
		Plugins []profiles.Row    `json:"plugins"`
		Config  config.Config     `json:"config"`
		Sources map[string]string `json:"sources"`
	}
	if err := json.Unmarshal(buffer.Bytes(), &document); err != nil {
		t.Fatalf("decode dump: %v\n%s", err, buffer.String())
	}
	if document.Profile != "default" || len(document.Bundles) != 3 {
		t.Fatalf("dump header = %q %v", document.Profile, document.Bundles)
	}
	if got := document.Config.MaxParallelToolCalls; got != 4 {
		t.Fatalf("dumped maxParallelToolCalls = %d", got)
	}
	if got := document.Sources[config.FieldMaxParallelToolCalls]; got != "patch:"+patch {
		t.Fatalf("dumped source = %q", got)
	}
	if document.Sources[config.FieldModel] != config.SourceDefault {
		t.Fatalf("model source = %q", document.Sources[config.FieldModel])
	}
	if len(document.Plugins) != 6 {
		t.Fatalf("plugins = %v", document.Plugins)
	}
	for _, row := range document.Plugins {
		if row.Source == "" {
			t.Fatalf("row %q has no source", row.ID)
		}
	}
	if !strings.Contains(buffer.String(), `"disabled": true`) {
		t.Fatalf("dump should show the disabled row:\n%s", buffer.String())
	}
}

// TestHomeIgnoresUpstreamDSHHOME pins the isolation rule: the installed desktop
// app exports DSH_HOME=~/.dsh into child processes, and reading it would make the
// Go rewrite load the other product's profiles.
func TestHomeIgnoresUpstreamDSHHOME(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "home", "config.json")
	t.Setenv("DSH_HOME", filepath.Join(t.TempDir(), "upstream"))
	t.Setenv("DSH_CONFIG", configPath)
	if got, want := profiles.Home(), filepath.Dir(configPath); got != want {
		t.Fatalf("Home = %q, want %q", got, want)
	}
}

func TestHomeFallsBackToConfigDirectory(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "custom", "config.json")
	t.Setenv("DSH_HOME", "")
	t.Setenv("DSH_CONFIG", configPath)
	if got, want := profiles.Home(), filepath.Dir(configPath); got != want {
		t.Fatalf("Home = %q, want %q", got, want)
	}
}
