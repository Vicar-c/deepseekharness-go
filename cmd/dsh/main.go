// Command dsh is the entry point of the Go rewrite of DeepSeek Harness.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	mathrand "math/rand/v2"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/agent"
	"github.com/Vicar-c/deepseekharness-go/internal/builtin"
	"github.com/Vicar-c/deepseekharness-go/internal/config"
	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/llm"
	"github.com/Vicar-c/deepseekharness-go/internal/profiles"
	"github.com/Vicar-c/deepseekharness-go/internal/prompt"
	"github.com/Vicar-c/deepseekharness-go/internal/server"
	"github.com/Vicar-c/deepseekharness-go/internal/session"
	"github.com/Vicar-c/deepseekharness-go/internal/tool"
	"github.com/Vicar-c/deepseekharness-go/internal/version"
)

const usage = `DeepSeek Harness (Go rewrite)

Usage:
  dsh version                     Print version information
  dsh web [flags]                 Start the Web UI
  dsh chat [flags]                Chat in the terminal
  dsh tools                       List registered tools
  dsh sessions [flags] [id]       List stored sessions, or show one
  dsh config [init]               Show the effective config, or write defaults
  dsh help                        Show this help

Common flags:
  --config <path>       Config file (default $DSH_CONFIG or ~/.deepseekharness-go/config.json)
  --profile <name>      Profile to boot (default "default"; built-in: default, headless, web)
  --patch <path>        Patch file applied after the profile (repeatable)
  --dump-config         Print the resolved profile and configuration, then exit
  --model <name>        Model name (default deepseek-chat)
  --base-url <url>      OpenAI-compatible base URL (default https://api.deepseek.com)
  --workspace <dir>     Workspace directory (default current directory)
  --log-level <level>   debug|info|warn|error (default info)

Profiles compose the plugins the harness boots. A user profile lives in
<config dir>/profiles/<name>/ with a package.json declaring "dsh.profile.bundles"
plus an optional cordis.patch.json; <config dir>/cordis.patch.json applies to
every profile. Both patch layers run after the environment, before the flags.

Environment:
  DEEPSEEK_API_KEY      API key used by the model client
  DSH_CONFIG            Config file, which also selects the profile directory
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return 0
	}
	switch args[0] {
	case "version", "--version", "-v", "-version":
		fmt.Fprintln(stdout, "deepseekharness-go", version.String())
		return 0
	case "help", "--help", "-h", "-help":
		fmt.Fprint(stdout, usage)
		return 0
	case "web":
		return runWeb(args[1:], stdout, stderr)
	case "chat":
		return runChat(args[1:], stdout, stderr)
	case "tools":
		return runTools(args[1:], stdout, stderr)
	case "sessions":
		return runSessions(args[1:], stdout, stderr)
	case "config":
		return runConfig(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "dsh: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

type commonFlags struct {
	configPath string
	model      string
	baseURL    string
	workspace  string
	logLevel   string
	profile    string
	patches    patchList
	dumpConfig bool
}

// patchList collects the repeatable --patch flag in command-line order.
type patchList []string

func (p *patchList) String() string { return strings.Join(*p, ",") }

func (p *patchList) Set(value string) error {
	*p = append(*p, value)
	return nil
}

func addCommonFlags(fs *flag.FlagSet) *commonFlags {
	flags := &commonFlags{}
	fs.StringVar(&flags.configPath, "config", "", "config file path")
	fs.StringVar(&flags.model, "model", "", "model name")
	fs.StringVar(&flags.baseURL, "base-url", "", "OpenAI-compatible base URL")
	fs.StringVar(&flags.workspace, "workspace", "", "workspace directory")
	fs.StringVar(&flags.logLevel, "log-level", "", "debug|info|warn|error")
	fs.StringVar(&flags.profile, "profile", "", "profile to boot")
	fs.Var(&flags.patches, "patch", "patch file applied after the profile (repeatable)")
	fs.BoolVar(&flags.dumpConfig, "dump-config", false, "print the resolved profile and configuration, then exit")
	return flags
}

// loaded is everything a command needs from the configuration layers.
type loaded struct {
	cfg    config.Config
	tree   *profiles.Tree
	logger *slog.Logger
	// envFiles lists the .env layers that supplied values, in precedence order.
	envFiles []config.EnvLayer
	closeLog func()
}

func (flags *commonFlags) load(stderr io.Writer) (*loaded, error) {
	migrated, err := config.MigrateLegacyDataDir()
	if err != nil {
		return nil, err
	}
	if migrated {
		fmt.Fprintf(stderr, "dsh: migrated data directory to ~/%s\n", config.DataDirName)
	}
	// The .env layers are applied first: the configuration resolves the API key
	// from the environment while loading, so a key declared in a file has to be
	// in place before Load runs.
	envFiles, err := applyEnvFiles(stderr)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(flags.configPath)
	if err != nil {
		return nil, err
	}
	// Layer order matches the loader: file, environment, profile layers, then
	// explicit command-line flags.
	tree, err := profiles.Resolve(profiles.Options{Name: flags.profile, Patches: flags.patches})
	if err != nil {
		return nil, err
	}
	if err := tree.ApplySettings(&cfg); err != nil {
		return nil, err
	}
	cfg.ApplyFlags(flags.model, flags.baseURL, flags.workspace, flags.logLevel)
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}
	logger, closeLog := newLogger(cfg.LogLevel, cfg.LogFile, stderr)
	return &loaded{cfg: cfg, tree: tree, logger: logger, envFiles: envFiles, closeLog: closeLog}, nil
}

// applyEnvFiles reads the project and user .env layers and applies them to the
// process environment. A refusal (a .env file that declares a bootstrap
// variable) is an error, so the process stops instead of running with half of a
// configuration.
func applyEnvFiles(stderr io.Writer) ([]config.EnvLayer, error) {
	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		cwd = "."
	}
	return config.LoadEnvLayers(config.EnvLoadOptions{
		BinName: "dsh",
		Cwd:     cwd,
		Home:    config.HomeDir(),
		Warn:    func(line string) { fmt.Fprint(stderr, line) },
	})
}

// reportFailure prints one startup failure and saves its diagnostics report, so
// a user can paste a path instead of a wall of terminal output.
func (flags *commonFlags) reportFailure(stderr io.Writer, err error) {
	reportStartupFailure(stderr, diagnosticContext{
		Home:       config.HomeDir(),
		Version:    version.String(),
		Profile:    profileName(flags.profile),
		ConfigPath: configPathOf(flags.configPath),
	}, fmt.Errorf("dsh: %w", err))
}

// profileName reports the profile the run would boot, with the built-in default
// filled in.
func profileName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "default"
	}
	return name
}

// configPathOf reports the config file the run would read.
func configPathOf(path string) string {
	if strings.TrimSpace(path) == "" {
		return config.FilePath()
	}
	return path
}

// dumpIfRequested prints the resolved profile and configuration when
// --dump-config is set. The second result is the process exit code.
func dumpIfRequested(flags *commonFlags, cfg config.Config, tree *profiles.Tree, stdout, stderr io.Writer) (bool, int) {
	if !flags.dumpConfig {
		return false, 0
	}
	if err := tree.Dump(cfg).Encode(stdout); err != nil {
		fmt.Fprintf(stderr, "dsh: dump config: %v\n", err)
		return true, 1
	}
	return true, 0
}

type runtime struct {
	cfg      config.Config
	logger   *slog.Logger
	app      *kernel.App
	registry *tool.Registry
	pipeline *tool.Pipeline
	store    *session.Store
	// workspace and todos hold the state the builtin tools keep outside the
	// conversation; a resumed session restores them from its log.
	workspace *builtin.Workspace
	todos     *builtin.TodoStore
}

func bootstrap(cfg config.Config, tree *profiles.Tree, logger *slog.Logger) (*runtime, error) {
	app := kernel.New(logger)
	registry := tool.NewRegistry()
	pipeline := tool.NewPipeline(registry, tool.PipelineOptions{
		Logger:         logger,
		Events:         app.Events(),
		Timeout:        time.Duration(cfg.ToolTimeoutSec) * time.Second,
		MaxOutputBytes: cfg.MaxToolOutputBytes,
		MaxParallel:    cfg.MaxParallelToolCalls,
	})
	store := session.NewStore(cfg.SessionDir)
	store.SetWorkspace(cfg.Workspace)
	kernel.Provide(app.Services(), cfg)
	kernel.Provide(app.Services(), registry)
	kernel.Provide(app.Services(), pipeline)
	kernel.Provide(app.Services(), store)

	workspace := builtin.NewWorkspace(cfg.Workspace)
	todos := builtin.NewTodoStore()
	// The profile decides which plugins exist; bootstrap only maps row names to
	// implementations, so a patch that disables a row really removes its tools.
	for _, row := range tree.PluginRows() {
		plugin, err := pluginFor(row, workspace, todos)
		if err != nil {
			return nil, err
		}
		if err := app.Use(plugin); err != nil {
			return nil, err
		}
	}
	bootCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := app.Boot(bootCtx); err != nil {
		return nil, err
	}
	return &runtime{
		cfg: cfg, logger: logger, app: app, registry: registry, pipeline: pipeline, store: store,
		workspace: workspace, todos: todos,
	}, nil
}

// pluginFor maps one resolved profile row to its kernel plugin.
func pluginFor(row profiles.Row, workspace *builtin.Workspace, todos *builtin.TodoStore) (kernel.Plugin, error) {
	switch row.Name {
	case "builtin/fs":
		return builtin.NewFsPlugin(workspace), nil
	case "builtin/shell":
		return builtin.NewShellPlugin(workspace), nil
	case "builtin/time":
		return builtin.NewTimePlugin(), nil
	case "builtin/todo":
		return builtin.NewTodoPluginWithStore(todos), nil
	default:
		return nil, fmt.Errorf("dsh: profile row %q names unknown plugin %q", row.ID, row.Name)
	}
}

func (r *runtime) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = r.app.Shutdown(ctx)
	_ = r.store.Close()
}

func (r *runtime) newClient() (llm.Client, error) {
	apiKey := r.cfg.APIKey()
	if apiKey == "" {
		return nil, llm.ErrMissingAPIKey
	}
	return llm.NewDeepSeek(llm.DeepSeekOptions{
		BaseURL: r.cfg.BaseURL,
		APIKey:  apiKey,
		Model:   r.cfg.Model,
		Timeout: time.Duration(r.cfg.RequestTimeoutSec) * time.Second,
	})
}

func (r *runtime) newLoop(sess *session.Session) (*agent.Loop, error) {
	client, err := r.newClient()
	if err != nil {
		return nil, err
	}
	toolNames := make([]string, 0, r.registry.Len())
	for _, definition := range r.registry.Definitions() {
		toolNames = append(toolNames, definition.Name)
	}
	systemPrompt := prompt.Harness(prompt.Runtime{
		Model:     r.cfg.Model,
		Workspace: r.cfg.Workspace,
		Tools:     toolNames,
	}).Render()
	return agent.New(agent.Options{
		Client:               client,
		Pipeline:             r.pipeline,
		Session:              sess,
		Store:                r.store,
		Events:               r.app.Events(),
		Logger:               r.logger,
		SystemPrompt:         systemPrompt,
		MaxIterations:        r.cfg.MaxIterations,
		MaxParallelToolCalls: r.cfg.MaxParallelToolCalls,
		MaxTokens:            r.cfg.MaxTokens,
		ContextWindow:        r.cfg.ContextWindow,
		RetryPolicy:          retryPolicy(r.cfg),
		Random:               mathrand.Float64,
	})
}

// retryPolicy builds the model-call retry policy. The backoff shape follows the
// upstream provider defaults; only the retry budget is configurable, because a
// different backoff is a provider property rather than a user preference.
func retryPolicy(cfg config.Config) *llm.RetryPolicy {
	policy := llm.DefaultRetryPolicy()
	policy.MaxRetries = cfg.MaxRetries
	return policy
}

// restoreSession replays the state a session carries outside its message
// projection, so a resumed conversation keeps the todo list and the working
// directory it had. A fresh session resets both: one session must not inherit
// what another one changed.
func (r *runtime) restoreSession(sess *session.Session) error {
	r.workspace.ResetCwd()
	r.todos.Replace(nil)
	recording, err := r.store.Recording(sess.ID)
	if err != nil {
		return err
	}
	for _, event := range recording.Events {
		switch event.Type {
		case "todo/write":
			var data builtin.TodoWriteData
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return fmt.Errorf("dsh: decode todo/write: %w", err)
			}
			r.todos.Replace(data.Todos)
		case "working-directory/change":
			var data builtin.WorkingDirectoryData
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return fmt.Errorf("dsh: decode working-directory/change: %w", err)
			}
			// A directory deleted since the change must not block the resume: the
			// session continues from the workspace root instead.
			if err := r.workspace.RestoreCwd(data.Directory); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("dsh: restore working directory %q: %w", data.Directory, err)
			}
		}
	}
	return nil
}

func runWeb(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	fs.SetOutput(stderr)
	flags := addCommonFlags(fs)
	host := fs.String("host", "127.0.0.1", "listen host")
	port := fs.Int("port", 3080, "listen port")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	env, err := flags.load(stderr)
	if err != nil {
		flags.reportFailure(stderr, err)
		return 1
	}
	defer env.closeLog()
	if dumped, code := dumpIfRequested(flags, env.cfg, env.tree, stdout, stderr); dumped {
		return code
	}
	if !env.tree.Enabled(profiles.TransportWeb) {
		fmt.Fprintf(stderr, "dsh: profile %q does not enable the web transport (%s)\n", env.tree.Profile, profiles.TransportWeb)
		return 1
	}
	rt, err := bootstrap(env.cfg, env.tree, env.logger)
	if err != nil {
		flags.reportFailure(stderr, fmt.Errorf("boot: %w", err))
		return 1
	}
	defer rt.close()

	var runner server.ChatRunner
	if env.cfg.APIKey() == "" {
		env.logger.Warn("model API key is not configured; /api/chat will return 503", "env", env.cfg.APIKeyEnv)
	} else {
		runner = func(ctx context.Context, sess *session.Session, input string) (string, error) {
			if err := rt.restoreSession(sess); err != nil {
				return "", err
			}
			loop, err := rt.newLoop(sess)
			if err != nil {
				return "", err
			}
			return loop.Run(ctx, input)
		}
	}

	srv, err := server.New(server.Options{Config: env.cfg, Store: rt.store, Runner: runner, Events: rt.app.Events(), Logger: env.logger})
	if err != nil {
		fmt.Fprintf(stderr, "dsh: web: %v\n", err)
		return 1
	}
	address := net.JoinHostPort(*host, strconv.Itoa(*port))
	fmt.Fprintf(stdout, "dsh web: http://%s/\n", address)

	ctx, signals, stopSignals := newSignalContext()
	defer stopSignals()
	if err := srv.Run(ctx, address); err != nil {
		fmt.Fprintf(stderr, "dsh: web: %v\n", err)
		return failureCode(signals)
	}
	return signals.exitCode()
}

func runChat(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	flags := addCommonFlags(fs)
	prompt := fs.String("prompt", "", "run one prompt and exit")
	sessionID := fs.String("session", "", "resume an existing session id")
	title := fs.String("title", "", "title for a new session")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	env, err := flags.load(stderr)
	if err != nil {
		flags.reportFailure(stderr, err)
		return 1
	}
	defer env.closeLog()
	if dumped, code := dumpIfRequested(flags, env.cfg, env.tree, stdout, stderr); dumped {
		return code
	}
	if !env.tree.Enabled(profiles.TransportCLI) {
		fmt.Fprintf(stderr, "dsh: profile %q does not enable the CLI transport (%s)\n", env.tree.Profile, profiles.TransportCLI)
		return 1
	}
	rt, err := bootstrap(env.cfg, env.tree, env.logger)
	if err != nil {
		flags.reportFailure(stderr, fmt.Errorf("boot: %w", err))
		return 1
	}
	defer rt.close()

	var sess *session.Session
	if strings.TrimSpace(*sessionID) != "" {
		// Resume repairs a turn a crash left open before the new turn starts.
		sess, err = rt.store.Resume(*sessionID)
	} else {
		newTitle := strings.TrimSpace(*title)
		if newTitle == "" {
			newTitle = "CLI session"
		}
		sess, err = rt.store.Create(newTitle)
	}
	if err != nil {
		fmt.Fprintf(stderr, "dsh: %v\n", err)
		return 1
	}
	// A resumed session brings back its todo list and working directory; a new
	// one starts from the defaults.
	if err := rt.restoreSession(sess); err != nil {
		fmt.Fprintf(stderr, "dsh: %v\n", err)
		return 1
	}
	loop, err := rt.newLoop(sess)
	if err != nil {
		fmt.Fprintf(stderr, "dsh: %v\n", err)
		return 1
	}

	rt.app.Events().On(tool.EventToolExecuted, func(event kernel.Event) {
		payload, ok := event.Payload.(tool.ExecutionEvent)
		if !ok {
			return
		}
		outcome := "ok"
		if payload.Result.IsError {
			outcome = "error"
		}
		fmt.Fprintf(stderr, "[tool] %s %s %dms\n", payload.Call.Name, outcome, payload.Duration)
	})

	// A retry is worth one line on stderr: the user otherwise waits without
	// knowing that the harness is repeating a failed model call.
	rt.app.Events().On(agent.EventAgentRetry, func(event kernel.Event) {
		payload, ok := event.Payload.(agent.RetryEvent)
		if !ok {
			return
		}
		budget := "unbounded"
		if payload.Mode == llm.RetryModeNormal {
			budget = fmt.Sprintf("of %d", payload.MaxRetries)
		}
		fmt.Fprintf(stderr, "[retry] attempt %d %s in %s (%s)\n",
			payload.Retry, budget, payload.Delay.Round(time.Millisecond), payload.Code)
	})

	// Streaming deltas arrive as transient agent/stream events; the complete
	// assistant message is still persisted to the session log once.
	var streamed bool
	rt.app.Events().On(agent.EventAgentStream, func(event kernel.Event) {
		payload, ok := event.Payload.(agent.StreamEvent)
		if !ok || payload.Content == "" {
			return
		}
		streamed = true
		fmt.Fprint(stdout, payload.Content)
	})

	ctx, signals, stopSignals := newSignalContext()
	defer stopSignals()

	if *prompt != "" {
		reply, err := loop.Run(ctx, *prompt)
		if err != nil {
			fmt.Fprintf(stderr, "dsh: %v\n", err)
			return failureCode(signals)
		}
		if streamed {
			fmt.Fprintln(stdout)
		} else {
			fmt.Fprintln(stdout, reply)
		}
		return 0
	}

	fmt.Fprintf(stdout, "session %s (type `exit` to quit)\n", sess.ID)
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprint(stdout, "you> ")
		if !scanner.Scan() {
			fmt.Fprintln(stdout)
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}
		reply, err := loop.Run(ctx, line)
		if err != nil {
			fmt.Fprintf(stderr, "dsh: %v\n", err)
			if signals.signaled() {
				return failureCode(signals)
			}
			continue
		}
		if streamed {
			fmt.Fprint(stdout, "\n\n")
		} else {
			fmt.Fprintf(stdout, "\ndsh> %s\n\n", reply)
		}
		streamed = false
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(stderr, "dsh: read input: %v\n", err)
		return 1
	}
	return 0
}

func runTools(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	fs.SetOutput(stderr)
	flags := addCommonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	env, err := flags.load(stderr)
	if err != nil {
		flags.reportFailure(stderr, err)
		return 1
	}
	defer env.closeLog()
	if dumped, code := dumpIfRequested(flags, env.cfg, env.tree, stdout, stderr); dumped {
		return code
	}
	rt, err := bootstrap(env.cfg, env.tree, env.logger)
	if err != nil {
		flags.reportFailure(stderr, fmt.Errorf("boot: %w", err))
		return 1
	}
	defer rt.close()

	definitions := rt.registry.Definitions()
	fmt.Fprintf(stdout, "%d tools\n\n", len(definitions))
	width := 12
	for _, definition := range definitions {
		if len(definition.Name) > width {
			width = len(definition.Name)
		}
	}
	for _, definition := range definitions {
		fmt.Fprintf(stdout, "%-*s  %s\n", width, definition.Name, definition.Description)
	}
	return 0
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	flags := addCommonFlags(fs)
	writeDefaults := len(args) > 0 && args[0] == "init"
	if writeDefaults {
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path := flags.configPath
	if path == "" {
		path = config.FilePath()
	}
	if writeDefaults {
		cfg := config.Default()
		cfg.ApplyFlags(flags.model, flags.baseURL, flags.workspace, flags.logLevel)
		if err := cfg.Save(path); err != nil {
			fmt.Fprintf(stderr, "dsh: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "wrote %s\n", path)
		return 0
	}
	env, err := flags.load(stderr)
	if err != nil {
		flags.reportFailure(stderr, err)
		return 1
	}
	defer env.closeLog()
	if dumped, code := dumpIfRequested(flags, env.cfg, env.tree, stdout, stderr); dumped {
		return code
	}
	data, err := json.MarshalIndent(env.cfg, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "dsh: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "config file: %s\nenv files: %s\napi key: %s\n\n%s\n",
		path, envFileSummary(env.envFiles), apiKeyStatus(env.cfg), data)
	return 0
}

// envFileSummary names the .env layers that supplied values, or "none" when no
// layer file exists.
func envFileSummary(layers []config.EnvLayer) string {
	if len(layers) == 0 {
		return "none"
	}
	paths := make([]string, 0, len(layers))
	for _, layer := range layers {
		paths = append(paths, layer.Path)
	}
	return strings.Join(paths, ", ")
}

func apiKeyStatus(cfg config.Config) string {
	if cfg.APIKey() == "" {
		return "not set (" + cfg.APIKeyEnv + ")"
	}
	return "set (" + cfg.APIKeyEnv + ")"
}
