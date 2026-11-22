package config_test

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/config"
)

// fakeEnvironment is an injected environment so tests never read or mutate the
// process environment.
type fakeEnvironment struct {
	values map[string]string
	set    []string
	warned []string
}

func newFakeEnvironment(values map[string]string) *fakeEnvironment {
	copied := make(map[string]string, len(values))
	for name, value := range values {
		copied[name] = value
	}
	return &fakeEnvironment{values: copied}
}

func (f *fakeEnvironment) Lookup(name string) (string, bool) {
	value, ok := f.values[name]
	return value, ok
}

func (f *fakeEnvironment) Setenv(name, value string) error {
	f.values[name] = value
	f.set = append(f.set, name)
	return nil
}

func (f *fakeEnvironment) Warn(line string) {
	f.warned = append(f.warned, line)
}

func (f *fakeEnvironment) options(cwd, home string) config.EnvLoadOptions {
	return config.EnvLoadOptions{
		BinName: "dsh",
		Cwd:     cwd,
		Home:    home,
		Lookup:  f.Lookup,
		Setenv:  f.Setenv,
		Warn:    f.Warn,
	}
}

// writeEnvFile creates dir/.env with content and returns its path.
func writeEnvFile(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// checkLayers compares layer metadata without depending on struct equality.
func checkLayers(t *testing.T, got, want []config.EnvLayer) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("layers = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index].Path != want[index].Path ||
			got[index].Source != want[index].Source ||
			!slices.Equal(got[index].Names, want[index].Names) {
			t.Fatalf("layer %d = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func TestParseEnv(t *testing.T) {
	testCases := []struct {
		name      string
		content   string
		want      map[string]string
		wantNames []string
		wantErr   string
	}{
		{
			name:      "blank lines and comments",
			content:   "\n# a comment\n   \n\t# an indented comment\nKEY=value\n",
			want:      map[string]string{"KEY": "value"},
			wantNames: []string{"KEY"},
		},
		{
			name:      "empty content",
			content:   "",
			want:      map[string]string{},
			wantNames: []string{},
		},
		{
			name:      "unquoted values are trimmed",
			content:   "PLAIN=value\nSPACED =  spaced value   \n",
			want:      map[string]string{"PLAIN": "value", "SPACED": "spaced value"},
			wantNames: []string{"PLAIN", "SPACED"},
		},
		{
			name:      "export prefix",
			content:   "export EXPORTED=yes\nexportKEY=not-an-export\n",
			want:      map[string]string{"EXPORTED": "yes", "exportKEY": "not-an-export"},
			wantNames: []string{"EXPORTED", "exportKEY"},
		},
		{
			name:      "quoted values",
			content:   "DOUBLE=\"double quoted\"\nSINGLE='single quoted'\nPADDED=\" inner spaces \"\n",
			want:      map[string]string{"DOUBLE": "double quoted", "SINGLE": "single quoted", "PADDED": " inner spaces "},
			wantNames: []string{"DOUBLE", "SINGLE", "PADDED"},
		},
		{
			name:      "double quote escapes",
			content:   "ESCAPED=\"line1\\nline2\\ttabbed\\\"quoted\\\"\\\\backslash\"\n",
			want:      map[string]string{"ESCAPED": "line1\nline2\ttabbed\"quoted\"\\backslash"},
			wantNames: []string{"ESCAPED"},
		},
		{
			name:      "single quotes keep backslashes literal",
			content:   "LITERAL='a\\nb\\tc'\nWINPATH='C:\\temp\\new'\n",
			want:      map[string]string{"LITERAL": "a\\nb\\tc", "WINPATH": "C:\\temp\\new"},
			wantNames: []string{"LITERAL", "WINPATH"},
		},
		{
			name:      "trailing comments need preceding whitespace",
			content:   "DROPPED=value # a comment\nKEPT=value#anchor\nTABBED=value\t# comment\nONLY_COMMENT= # nothing\n",
			want:      map[string]string{"DROPPED": "value", "KEPT": "value#anchor", "TABBED": "value", "ONLY_COMMENT": ""},
			wantNames: []string{"DROPPED", "KEPT", "TABBED", "ONLY_COMMENT"},
		},
		{
			name:      "quotes protect comment characters",
			content:   "HASH=\"a # b\"\n",
			want:      map[string]string{"HASH": "a # b"},
			wantNames: []string{"HASH"},
		},
		{
			name:      "empty values and bare names",
			content:   "EMPTY=\nBARE\nSPACED_BARE   \n",
			want:      map[string]string{"EMPTY": "", "BARE": "", "SPACED_BARE": ""},
			wantNames: []string{"EMPTY", "BARE", "SPACED_BARE"},
		},
		{
			name:      "duplicate names keep the last value and first position",
			content:   "ALPHA=1\nBETA=2\nALPHA=3\n",
			want:      map[string]string{"ALPHA": "3", "BETA": "2"},
			wantNames: []string{"ALPHA", "BETA"},
		},
		{
			name:      "CRLF line endings",
			content:   "WINDOWS=yes\r\nSECOND=\"quoted\"\r\n",
			want:      map[string]string{"WINDOWS": "yes", "SECOND": "quoted"},
			wantNames: []string{"WINDOWS", "SECOND"},
		},
		{
			name:    "name starting with a digit",
			content: "1BAD=value\n",
			wantErr: "line 1: invalid variable name \"1BAD\"",
		},
		{
			name:    "name with a dash",
			content: "GOOD=1\nBAD-NAME=2\n",
			wantErr: "line 2: invalid variable name \"BAD-NAME\"",
		},
		{
			name:    "name with a dot",
			content: "BAD.NAME=2\n",
			wantErr: "line 1: invalid variable name \"BAD.NAME\"",
		},
		{
			name:    "empty name",
			content: "=value\n",
			wantErr: "line 1: invalid variable name \"\"",
		},
		{
			name:    "export without a name",
			content: "export\n",
			wantErr: "line 1: invalid variable name \"\"",
		},
		{
			name:    "unterminated double quote",
			content: "BROKEN=\"value\n",
			wantErr: "line 1: unterminated double-quoted value",
		},
		{
			name:    "unterminated single quote",
			content: "BROKEN='value\n",
			wantErr: "line 1: unterminated single-quoted value",
		},
		{
			name:    "text after a closing quote",
			content: "BROKEN=\"value\" trailing\n",
			wantErr: "line 1: unexpected text after quoted value",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			values, names, err := config.ParseEnv(testCase.content)
			if testCase.wantErr != "" {
				if err == nil || err.Error() != testCase.wantErr {
					t.Fatalf("error = %v, want %q", err, testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseEnv: %v", err)
			}
			if !maps.Equal(values, testCase.want) {
				t.Fatalf("values = %#v, want %#v", values, testCase.want)
			}
			if !slices.Equal(names, testCase.wantNames) {
				t.Fatalf("names = %#v, want %#v", names, testCase.wantNames)
			}
		})
	}
}

func TestLoadEnvLayersPrecedence(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	writeEnvFile(t, cwd, "EXPORTED=from-project\nPROJECT_ONLY=project\nSHARED=from-project\nEMPTY_EXPORTED=from-project\n")
	writeEnvFile(t, home, "SHARED=from-user\nUSER_ONLY=user\nEXPORTED=from-user\nEMPTY_EXPORTED=from-user\n")

	// An empty exported value is still an exported value, so the file must not
	// replace it.
	environment := newFakeEnvironment(map[string]string{
		"EXPORTED":       "from-environment",
		"EMPTY_EXPORTED": "",
	})
	layers, err := config.LoadEnvLayers(environment.options(cwd, home))
	if err != nil {
		t.Fatalf("LoadEnvLayers: %v", err)
	}

	wantValues := map[string]string{
		"EXPORTED":       "from-environment",
		"EMPTY_EXPORTED": "",
		"PROJECT_ONLY":   "project",
		"SHARED":         "from-project",
		"USER_ONLY":      "user",
	}
	if !maps.Equal(environment.values, wantValues) {
		t.Fatalf("environment = %#v, want %#v", environment.values, wantValues)
	}
	// The environment wins over the project layer, and the project layer wins
	// over the user layer; a skipped name is never set again.
	if want := []string{"PROJECT_ONLY", "SHARED", "USER_ONLY"}; !slices.Equal(environment.set, want) {
		t.Fatalf("applied names = %#v, want %#v", environment.set, want)
	}
	if len(layers) != 2 {
		t.Fatalf("layers = %#v, want both files reported", layers)
	}
}

func TestLoadEnvLayersRefusesBootstrapNamesInProject(t *testing.T) {
	home := t.TempDir()
	homeEnv := filepath.Join(home, ".env")
	testCases := []struct {
		name       string
		variable   string
		content    string
		wantRemedy string
	}{
		{
			name:       "exact name",
			variable:   "PATH",
			content:    "PATH=/tmp/evil\n",
			wantRemedy: "export PATH instead of putting it in a .env file",
		},
		{
			name:       "exact name in another letter case",
			variable:   "Editor",
			content:    "Editor=./script\n",
			wantRemedy: "export Editor instead of putting it in a .env file",
		},
		{
			name:       "bootstrap prefix",
			variable:   "DSH_PERMISSION_MODE",
			content:    "DSH_PERMISSION_MODE=danger-full-access\n",
			wantRemedy: "export DSH_PERMISSION_MODE instead of putting it in a .env file",
		},
		{
			name:       "bootstrap prefix in another letter case",
			variable:   "xdg_config_home",
			content:    "xdg_config_home=/elsewhere\n",
			wantRemedy: "export xdg_config_home instead of putting it in a .env file",
		},
		{
			name:       "proxy",
			variable:   "HTTP_PROXY",
			content:    "HTTP_PROXY=http://attacker.example\n",
			wantRemedy: fmt.Sprintf("export HTTP_PROXY, or put it in %s, which does not travel with a repository", homeEnv),
		},
		{
			name:       "lowercase proxy",
			variable:   "https_proxy",
			content:    "https_proxy=http://attacker.example\n",
			wantRemedy: fmt.Sprintf("export https_proxy, or put it in %s, which does not travel with a repository", homeEnv),
		},
		{
			name:       "endpoint",
			variable:   "DEEPSEEK_BASE_URL",
			content:    "DEEPSEEK_BASE_URL=http://attacker.example\n",
			wantRemedy: fmt.Sprintf("export DEEPSEEK_BASE_URL, or put it in %s, which does not travel with a repository", homeEnv),
		},
		{
			name:       "endpoint spelled another way",
			variable:   "deepseek_search_base_url",
			content:    "deepseek_search_base_url=http://attacker.example\n",
			wantRemedy: fmt.Sprintf("spell it DEEPSEEK_SEARCH_BASE_URL, then export it or put it in %s, which does not travel with a repository", homeEnv),
		},
		{
			name:       "trust anchor",
			variable:   "SSL_CERT_FILE",
			content:    "SSL_CERT_FILE=/tmp/ca.pem\n",
			wantRemedy: "export SSL_CERT_FILE instead of putting it in a .env file",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			project := t.TempDir()
			path := writeEnvFile(t, project, "PROJECT_ONLY=must-not-apply\n"+testCase.content)
			environment := newFakeEnvironment(nil)
			layers, err := config.LoadEnvLayers(environment.options(project, home))
			if err == nil {
				t.Fatalf("LoadEnvLayers succeeded, want a refusal for %s", testCase.variable)
			}
			wantMessage := fmt.Sprintf(
				`dsh: %s sets %q, which only the launching environment may set (it decides how this process starts, where its code and instructions load from, or how it reaches the network); %s`,
				path, testCase.variable, testCase.wantRemedy,
			)
			if err.Error() != wantMessage {
				t.Fatalf("error = %q, want %q", err, wantMessage)
			}
			if len(layers) != 0 {
				t.Fatalf("layers = %#v, want none", layers)
			}
			if len(environment.values) != 0 || len(environment.set) != 0 {
				t.Fatalf("environment changed despite the refusal: values=%#v set=%#v", environment.values, environment.set)
			}
		})
	}
}

func TestLoadEnvLayersHomeLayerExemptions(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	writeEnvFile(t, cwd, "PROJECT_SETTING=project\n")
	userPath := writeEnvFile(t, home, strings.Join([]string{
		"HTTP_PROXY=http://from-home:8080",
		"no_proxy=example.com",
		"HTTPS_PROXY=http://from-home:8443",
		"DEEPSEEK_BASE_URL=https://home.example/v1",
		"DEEPSEEK_SEARCH_BASE_URL=https://search.example/v1",
		"",
	}, "\n"))
	// The launching environment still outranks the file for the same variable.
	environment := newFakeEnvironment(map[string]string{"HTTPS_PROXY": "http://exported:8080"})

	layers, err := config.LoadEnvLayers(environment.options(cwd, home))
	if err != nil {
		t.Fatalf("LoadEnvLayers: %v", err)
	}
	if len(environment.warned) != 0 {
		t.Fatalf("warnings = %#v, want none", environment.warned)
	}
	wantValues := map[string]string{
		"PROJECT_SETTING":          "project",
		"HTTP_PROXY":               "http://from-home:8080",
		"no_proxy":                 "example.com",
		"HTTPS_PROXY":              "http://exported:8080",
		"DEEPSEEK_BASE_URL":        "https://home.example/v1",
		"DEEPSEEK_SEARCH_BASE_URL": "https://search.example/v1",
	}
	if !maps.Equal(environment.values, wantValues) {
		t.Fatalf("environment = %#v, want %#v", environment.values, wantValues)
	}
	wantApplied := []string{"PROJECT_SETTING", "HTTP_PROXY", "no_proxy", "DEEPSEEK_BASE_URL", "DEEPSEEK_SEARCH_BASE_URL"}
	if !slices.Equal(environment.set, wantApplied) {
		t.Fatalf("applied names = %#v, want %#v", environment.set, wantApplied)
	}
	checkLayers(t, layers, []config.EnvLayer{
		{Path: filepath.Join(cwd, ".env"), Source: "project-env", Names: []string{"PROJECT_SETTING"}},
		{Path: userPath, Source: "user-env", Names: []string{"HTTP_PROXY", "no_proxy", "HTTPS_PROXY", "DEEPSEEK_BASE_URL", "DEEPSEEK_SEARCH_BASE_URL"}},
	})
}

func TestLoadEnvLayersHomeLayerStillRefuses(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	homeEnv := filepath.Join(home, ".env")
	testCases := []struct {
		name       string
		variable   string
		content    string
		wantRemedy string
	}{
		{
			name:       "trust anchor",
			variable:   "SSL_CERT_FILE",
			content:    "SSL_CERT_FILE=/tmp/ca.pem\n",
			wantRemedy: "export SSL_CERT_FILE instead of putting it in a .env file",
		},
		{
			name:       "TLS verification switch",
			variable:   "NODE_TLS_REJECT_UNAUTHORIZED",
			content:    "NODE_TLS_REJECT_UNAUTHORIZED=0\n",
			wantRemedy: "export NODE_TLS_REJECT_UNAUTHORIZED instead of putting it in a .env file",
		},
		{
			name:       "home selector",
			variable:   "DSH_HOME",
			content:    "DSH_HOME=/elsewhere\n",
			wantRemedy: "export DSH_HOME instead of putting it in a .env file",
		},
		{
			name:       "endpoint spelled another way",
			variable:   "deepseek_base_url",
			content:    "deepseek_base_url=https://elsewhere.example/v1\n",
			wantRemedy: fmt.Sprintf("spell it DEEPSEEK_BASE_URL, then export it or put it in %s, which does not travel with a repository", homeEnv),
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			writeEnvFile(t, home, "USER_ONLY=must-not-apply\n"+testCase.content)
			environment := newFakeEnvironment(nil)
			layers, err := config.LoadEnvLayers(environment.options(project, home))
			if err == nil {
				t.Fatalf("LoadEnvLayers succeeded, want a refusal for %s", testCase.variable)
			}
			wantMessage := fmt.Sprintf(
				`dsh: %s sets %q, which only the launching environment may set (it decides how this process starts, where its code and instructions load from, or how it reaches the network); %s`,
				homeEnv, testCase.variable, testCase.wantRemedy,
			)
			if err.Error() != wantMessage {
				t.Fatalf("error = %q, want %q", err, wantMessage)
			}
			if len(layers) != 0 {
				t.Fatalf("layers = %#v, want none", layers)
			}
			if len(environment.values) != 0 || len(environment.set) != 0 {
				t.Fatalf("environment changed despite the refusal: values=%#v set=%#v", environment.values, environment.set)
			}
		})
	}
}

func TestLoadEnvLayersValidatesBothLayersBeforeApplying(t *testing.T) {
	t.Run("project refusal leaves its earlier names unapplied", func(t *testing.T) {
		cwd := t.TempDir()
		home := t.TempDir()
		path := writeEnvFile(t, cwd, "PROJECT_ONLY=project\nPATH=/tmp/evil\n")
		writeEnvFile(t, home, "USER_ONLY=user\n")

		environment := newFakeEnvironment(nil)
		_, err := config.LoadEnvLayers(environment.options(cwd, home))
		if err == nil {
			t.Fatal("LoadEnvLayers succeeded, want a refusal")
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("%s sets \"PATH\"", path)) {
			t.Fatalf("error = %v, want the project layer path", err)
		}
		if len(environment.values) != 0 || len(environment.set) != 0 {
			t.Fatalf("environment changed despite the refusal: values=%#v set=%#v", environment.values, environment.set)
		}
	})

	t.Run("user refusal leaves a valid project layer unapplied", func(t *testing.T) {
		cwd := t.TempDir()
		home := t.TempDir()
		writeEnvFile(t, cwd, "PROJECT_ONLY=project\n")
		homeEnv := writeEnvFile(t, home, "USER_ONLY=user\nHOME=/tmp/elsewhere\n")

		environment := newFakeEnvironment(nil)
		_, err := config.LoadEnvLayers(environment.options(cwd, home))
		if err == nil {
			t.Fatal("LoadEnvLayers succeeded, want a refusal")
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("%s sets \"HOME\"", homeEnv)) {
			t.Fatalf("error = %v, want the user layer path", err)
		}
		if len(environment.values) != 0 || len(environment.set) != 0 {
			t.Fatalf("environment changed despite the refusal: values=%#v set=%#v", environment.values, environment.set)
		}
	})
}

func TestLoadEnvLayersMissingFiles(t *testing.T) {
	t.Run("neither layer exists", func(t *testing.T) {
		cwd := t.TempDir()
		home := t.TempDir()
		environment := newFakeEnvironment(map[string]string{"INHERITED": "yes"})

		layers, err := config.LoadEnvLayers(environment.options(cwd, home))
		if err != nil {
			t.Fatalf("LoadEnvLayers: %v", err)
		}
		if len(layers) != 0 {
			t.Fatalf("layers = %#v, want none", layers)
		}
		if len(environment.warned) != 0 {
			t.Fatalf("warnings = %#v, want none for missing files", environment.warned)
		}
		if !maps.Equal(environment.values, map[string]string{"INHERITED": "yes"}) {
			t.Fatalf("environment = %#v, want only the inherited value", environment.values)
		}
	})

	t.Run("only the user layer exists", func(t *testing.T) {
		cwd := t.TempDir()
		home := t.TempDir()
		userPath := writeEnvFile(t, home, "USER_ONLY=user\n")
		environment := newFakeEnvironment(nil)

		layers, err := config.LoadEnvLayers(environment.options(cwd, home))
		if err != nil {
			t.Fatalf("LoadEnvLayers: %v", err)
		}
		checkLayers(t, layers, []config.EnvLayer{
			{Path: userPath, Source: "user-env", Names: []string{"USER_ONLY"}},
		})
		if environment.values["USER_ONLY"] != "user" {
			t.Fatalf("USER_ONLY = %q", environment.values["USER_ONLY"])
		}
	})
}

func TestLoadEnvLayersWarnsOnUnreadableLayer(t *testing.T) {
	t.Run("user layer is a directory", func(t *testing.T) {
		cwd := t.TempDir()
		home := t.TempDir()
		writeEnvFile(t, cwd, "PROJECT_ONLY=project\n")
		if err := os.Mkdir(filepath.Join(home, ".env"), 0o755); err != nil {
			t.Fatalf("create unreadable .env directory: %v", err)
		}

		environment := newFakeEnvironment(nil)
		layers, err := config.LoadEnvLayers(environment.options(cwd, home))
		if err != nil {
			t.Fatalf("LoadEnvLayers: %v", err)
		}
		if len(environment.warned) != 1 {
			t.Fatalf("warnings = %#v, want one", environment.warned)
		}
		warning := environment.warned[0]
		if !strings.HasPrefix(warning, "dsh: failed to load .env: ") || !strings.HasSuffix(warning, "\n") {
			t.Fatalf("warning = %q, want the documented one-line diagnostic", warning)
		}
		checkLayers(t, layers, []config.EnvLayer{
			{Path: filepath.Join(cwd, ".env"), Source: "project-env", Names: []string{"PROJECT_ONLY"}},
		})
		if environment.values["PROJECT_ONLY"] != "project" {
			t.Fatalf("PROJECT_ONLY = %q, want the project value", environment.values["PROJECT_ONLY"])
		}
	})

	t.Run("project layer is a directory and the user layer still loads", func(t *testing.T) {
		cwd := t.TempDir()
		home := t.TempDir()
		if err := os.Mkdir(filepath.Join(cwd, ".env"), 0o755); err != nil {
			t.Fatalf("create unreadable .env directory: %v", err)
		}
		userPath := writeEnvFile(t, home, "USER_ONLY=user\n")

		environment := newFakeEnvironment(nil)
		layers, err := config.LoadEnvLayers(environment.options(cwd, home))
		if err != nil {
			t.Fatalf("LoadEnvLayers: %v", err)
		}
		if len(environment.warned) != 1 {
			t.Fatalf("warnings = %#v, want one", environment.warned)
		}
		checkLayers(t, layers, []config.EnvLayer{
			{Path: userPath, Source: "user-env", Names: []string{"USER_ONLY"}},
		})
		if environment.values["USER_ONLY"] != "user" {
			t.Fatalf("USER_ONLY = %q, want the user value", environment.values["USER_ONLY"])
		}
	})
}

func TestLoadEnvLayersReadsSharedFileOnceWhenCwdIsHome(t *testing.T) {
	shared := t.TempDir()
	path := writeEnvFile(t, shared, "HTTP_PROXY=http://from-home:8080\nONLY=one\n")
	environment := newFakeEnvironment(nil)

	layers, err := config.LoadEnvLayers(environment.options(shared, shared))
	if err != nil {
		t.Fatalf("LoadEnvLayers: %v", err)
	}
	// The exemption follows the directory: the one file is read as the project
	// layer but may still set a proxy name.
	checkLayers(t, layers, []config.EnvLayer{
		{Path: path, Source: "project-env", Names: []string{"HTTP_PROXY", "ONLY"}},
	})
	if want := []string{"HTTP_PROXY", "ONLY"}; !slices.Equal(environment.set, want) {
		t.Fatalf("applied names = %#v, want %#v", environment.set, want)
	}
	if environment.values["HTTP_PROXY"] != "http://from-home:8080" {
		t.Fatalf("HTTP_PROXY = %q", environment.values["HTTP_PROXY"])
	}
}

func TestLoadEnvLayersReturnsLayerMetadata(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	projectPath := writeEnvFile(t, cwd, "ZETA=1\nALPHA=2\nZETA=3\n")
	userPath := writeEnvFile(t, home, "OMEGA=user\n")
	environment := newFakeEnvironment(map[string]string{"ALPHA": "from-environment"})

	layers, err := config.LoadEnvLayers(environment.options(cwd, home))
	if err != nil {
		t.Fatalf("LoadEnvLayers: %v", err)
	}
	// Paths and sources identify the files, names keep file order with
	// duplicates removed, and both layers come back project first.
	checkLayers(t, layers, []config.EnvLayer{
		{Path: projectPath, Source: "project-env", Names: []string{"ZETA", "ALPHA"}},
		{Path: userPath, Source: "user-env", Names: []string{"OMEGA"}},
	})
	wantValues := map[string]string{"ZETA": "3", "ALPHA": "from-environment", "OMEGA": "user"}
	if !maps.Equal(environment.values, wantValues) {
		t.Fatalf("environment = %#v, want %#v", environment.values, wantValues)
	}
	if want := []string{"ZETA", "OMEGA"}; !slices.Equal(environment.set, want) {
		t.Fatalf("applied names = %#v, want %#v", environment.set, want)
	}

	t.Run("empty file is still a layer", func(t *testing.T) {
		cwd := t.TempDir()
		home := t.TempDir()
		path := writeEnvFile(t, cwd, "# nothing to set\n")
		environment := newFakeEnvironment(nil)
		layers, err := config.LoadEnvLayers(environment.options(cwd, home))
		if err != nil {
			t.Fatalf("LoadEnvLayers: %v", err)
		}
		checkLayers(t, layers, []config.EnvLayer{
			{Path: path, Source: "project-env", Names: []string{}},
		})
		if len(environment.set) != 0 {
			t.Fatalf("applied names = %#v, want none", environment.set)
		}
	})
}

func TestLoadEnvLayersReturnsSetenvError(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	path := writeEnvFile(t, cwd, "PROJECT_ONLY=project\n")
	environment := newFakeEnvironment(nil)
	options := environment.options(cwd, home)
	options.Setenv = func(name, value string) error {
		return errors.New("environment is read-only")
	}

	layers, err := config.LoadEnvLayers(options)
	if err == nil {
		t.Fatal("LoadEnvLayers succeeded, want the setter error")
	}
	for _, want := range []string{"PROJECT_ONLY", path, "environment is read-only"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to mention %q", err, want)
		}
	}
	if len(layers) != 0 {
		t.Fatalf("layers = %#v, want none on error", layers)
	}
}

func TestLoadEnvLayersToleratesUTF8BOM(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	content := append([]byte{0xEF, 0xBB, 0xBF}, []byte("BOM_NAME=value\n")...)
	if err := os.WriteFile(filepath.Join(cwd, ".env"), content, 0o644); err != nil {
		t.Fatalf("write .env with BOM: %v", err)
	}

	environment := newFakeEnvironment(nil)
	layers, err := config.LoadEnvLayers(environment.options(cwd, home))
	if err != nil {
		t.Fatalf("LoadEnvLayers: %v", err)
	}
	if environment.values["BOM_NAME"] != "value" {
		t.Fatalf("BOM_NAME = %q, want value", environment.values["BOM_NAME"])
	}
	checkLayers(t, layers, []config.EnvLayer{
		{Path: filepath.Join(cwd, ".env"), Source: "project-env", Names: []string{"BOM_NAME"}},
	})
}
