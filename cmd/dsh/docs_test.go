package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/builtin"
	"github.com/Vicar-c/deepseekharness-go/internal/config"
	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
)

// The README is the public contract of the CLI, so its command list, tool list
// and configuration example are pinned here. A batch that changes the CLI, a
// tool or a setting must update the README in the same commit or these tests
// fail (AGENTS.md §1.14).

const readmePath = "../../README.md"

func readmeText(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	return string(data)
}

// readmeBlock returns the lines between the <!-- name:start --> and
// <!-- name:end --> markers.
func readmeBlock(t *testing.T, name string) []string {
	t.Helper()
	text := readmeText(t)
	start := strings.Index(text, "<!-- "+name+":start -->")
	end := strings.Index(text, "<!-- "+name+":end -->")
	if start < 0 || end < 0 || end < start {
		t.Fatalf("README is missing the %q block", name)
	}
	return strings.Split(text[start:end], "\n")
}

// usageCommands parses the "  dsh <command>" lines of the usage text.
func usageCommands(t *testing.T) []string {
	t.Helper()
	matches := regexp.MustCompile(`(?m)^  dsh ([a-z]+)`).FindAllStringSubmatch(usage, -1)
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match[1])
	}
	return names
}

// switchCommands parses the subcommand switch inside run(), so a command that is
// documented but no longer dispatched is caught too.
func switchCommands(t *testing.T) []string {
	t.Helper()
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	text := string(source)
	start := strings.Index(text, "switch args[0] {")
	if start < 0 {
		t.Fatal("main.go: cannot find the subcommand switch")
	}
	rest := text[start:]
	end := strings.Index(rest, "\n\t}\n")
	if end < 0 {
		t.Fatal("main.go: cannot find the end of the subcommand switch")
	}
	block := rest[:end]

	var names []string
	wordRe := regexp.MustCompile(`"([^"]+)"`)
	for _, match := range regexp.MustCompile(`case ((?:"[^"]*"(?:,\s*)?)+):`).FindAllStringSubmatch(block, -1) {
		for _, word := range wordRe.FindAllStringSubmatch(match[1], -1) {
			if strings.HasPrefix(word[1], "-") {
				continue // flag aliases such as --version
			}
			names = append(names, word[1])
		}
	}
	return names
}

func sortedUnique(names []string) []string {
	seen := make(map[string]bool, len(names))
	unique := make([]string, 0, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		unique = append(unique, name)
	}
	sort.Strings(unique)
	return unique
}

func TestReadmeCommandListMatchesTheCLI(t *testing.T) {
	var documented []string
	for _, line := range readmeBlock(t, "cli") {
		for _, match := range regexp.MustCompile("`dsh ([a-z]+)").FindAllStringSubmatch(line, -1) {
			documented = append(documented, match[1])
		}
	}
	documented = sortedUnique(documented)
	dispatched := sortedUnique(switchCommands(t))
	documentedInUsage := sortedUnique(usageCommands(t))

	if strings.Join(documented, ",") != strings.Join(dispatched, ",") {
		t.Fatalf("README commands = %v, main.go dispatches %v", documented, dispatched)
	}
	if strings.Join(documented, ",") != strings.Join(documentedInUsage, ",") {
		t.Fatalf("README commands = %v, usage text lists %v", documented, documentedInUsage)
	}
}

// shellAliases are the two platform spellings of the one shell tool.
var shellAliases = map[string]bool{"pwsh": true, "bash": true}

func registeredToolNames(t *testing.T) map[string]bool {
	t.Helper()
	registry := tool.NewRegistry()
	services := kernel.NewServices()
	kernel.Provide(services, registry)
	workspace := builtin.NewWorkspace(t.TempDir())
	appContext := &kernel.Context{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Events:   kernel.NewEvents(),
		Services: services,
	}
	for _, plugin := range []kernel.Plugin{
		builtin.NewFsPlugin(workspace),
		builtin.NewShellPlugin(workspace),
		builtin.NewTimePlugin(),
		builtin.NewTodoPlugin(),
	} {
		if err := plugin.Apply(appContext); err != nil {
			t.Fatalf("apply %s: %v", plugin.Name(), err)
		}
	}
	names := make(map[string]bool, registry.Len())
	for _, definition := range registry.Definitions() {
		names[definition.Name] = true
	}
	return names
}

func TestReadmeToolTableMatchesTheRegistry(t *testing.T) {
	documented := make(map[string]bool)
	for _, line := range readmeBlock(t, "tools") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "| `") {
			continue // header and separator rows
		}
		first := strings.SplitN(strings.TrimPrefix(trimmed, "|"), "|", 2)[0]
		for _, token := range strings.Split(first, "/") {
			name := strings.Trim(strings.TrimSpace(token), "`")
			if name != "" {
				documented[name] = true
			}
		}
	}
	if len(documented) == 0 {
		t.Fatal("README tool table is empty")
	}
	registered := registeredToolNames(t)

	for name := range registered {
		if !documented[name] {
			t.Errorf("tool %q is registered but missing from the README tool table", name)
		}
	}
	for name := range documented {
		if registered[name] || shellAliases[name] {
			continue
		}
		t.Errorf("README documents tool %q that is not registered", name)
	}
	if !documented["pwsh"] || !documented["bash"] {
		t.Error("README must document both platform spellings of the shell tool")
	}
}

// TestRunDispatchesSafeCommands exercises the CLI entry point without booting a
// kernel or touching the network: the help/version paths must succeed and an
// unknown command must exit with the usage-error code.
func TestRunDispatchesSafeCommands(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{"no arguments prints usage", nil, 0, "dsh web"},
		{"help", []string{"help"}, 0, "dsh chat"},
		{"long help flag", []string{"--help"}, 0, "Common flags:"},
		{"version", []string{"version"}, 0, "deepseekharness-go"},
		{"unknown command", []string{"definitely-not-a-command"}, 2, "unknown command"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			if code := run(testCase.args, &stdout, &stderr); code != testCase.wantCode {
				t.Fatalf("run(%v) = %d, want %d (stderr: %s)", testCase.args, code, testCase.wantCode, stderr.String())
			}
			combined := stdout.String() + stderr.String()
			if !strings.Contains(combined, testCase.wantOut) {
				t.Fatalf("run(%v) output = %q, want it to contain %q", testCase.args, combined, testCase.wantOut)
			}
		})
	}
}

// jsonBlocks returns every fenced ```json block of the README.
func jsonBlocks(t *testing.T) []string {
	t.Helper()
	var blocks []string
	var current []string
	inBlock := false
	for _, line := range strings.Split(readmeText(t), "\n") {
		switch {
		case line == "```json":
			inBlock = true
			current = nil
		case inBlock && line == "```":
			inBlock = false
			blocks = append(blocks, strings.Join(current, "\n"))
		case inBlock:
			current = append(current, line)
		}
	}
	return blocks
}

// TestReadmeConfigExampleMatchesTheSettings keeps the documented config example
// in sync with the real settings: a new field must appear, an unknown one fails.
func TestReadmeConfigExampleMatchesTheSettings(t *testing.T) {
	var example string
	var keys []string
	for _, block := range jsonBlocks(t) {
		var candidate map[string]json.RawMessage
		if err := json.Unmarshal([]byte(block), &candidate); err != nil {
			continue
		}
		if _, ok := candidate["model"]; !ok {
			continue // the profile patch example, not the config example
		}
		example = block
		for key := range candidate {
			keys = append(keys, key)
		}
	}
	if example == "" {
		t.Fatal("README has no configuration example")
	}
	keys = sortedUnique(keys)
	fields := sortedUnique(config.Fields())
	if strings.Join(keys, ",") != strings.Join(fields, ",") {
		t.Fatalf("README config keys = %v, config.Fields() = %v", keys, fields)
	}

	decoder := json.NewDecoder(bytes.NewBufferString(example))
	decoder.DisallowUnknownFields()
	var parsed config.Config
	if err := decoder.Decode(&parsed); err != nil {
		t.Fatalf("README config example does not decode into config.Config: %v", err)
	}
}
