// Command dsh is the entry point of the Go rewrite of DeepSeek Harness.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/agent"
	"github.com/Vicar-c/deepseekharness-go/internal/builtin"
	"github.com/Vicar-c/deepseekharness-go/internal/config"
	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
	"github.com/Vicar-c/deepseekharness-go/internal/llm"
	"github.com/Vicar-c/deepseekharness-go/internal/profiles"
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

func (flags *commonFlags) load(stderr io.Writer) (config.Config, *profiles.Tree, *slog.Logger, error) {
	migrated, err := config.MigrateLegacyDataDir()
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	if migrated {
		fmt.Fprintf(stderr, "dsh: migrated data directory to ~/%s\n", config.DataDirName)
	}
	cfg, err := config.Load(flags.configPath)
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	// Layer order matches the loader: file, environment, profile layers, then
	// explicit command-line flags.
	tree, err := profiles.Resolve(profiles.Options{Name: flags.profile, Patches: flags.patches})
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	if err := tree.ApplySettings(&cfg); err != nil {
		return config.Config{}, nil, nil, err
	}
	cfg.ApplyFlags(flags.model, flags.baseURL, flags.workspace, flags.logLevel)
	if err := cfg.Normalize(); err != nil {
		return config.Config{}, nil, nil, err
	}
	logger := newLogger(cfg.LogLevel)
	return cfg, tree, logger, nil
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

func newLogger(level string) *slog.Logger {
	var parsed slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		parsed = slog.LevelDebug
	case "warn", "warning":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	default:
		parsed = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parsed}))
	slog.SetDefault(logger)
	return logger
}

type runtime struct {
	cfg      config.Config
	logger   *slog.Logger
	app      *kernel.App
	registry *tool.Registry
	pipeline *tool.Pipeline
	store    *session.Store
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
	// The profile decides which plugins exist; bootstrap only maps row names to
	// implementations, so a patch that disables a row really removes its tools.
	for _, row := range tree.PluginRows() {
		plugin, err := pluginFor(row, workspace)
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
	return &runtime{cfg: cfg, logger: logger, app: app, registry: registry, pipeline: pipeline, store: store}, nil
}

// pluginFor maps one resolved profile row to its kernel plugin.
func pluginFor(row profiles.Row, workspace *builtin.Workspace) (kernel.Plugin, error) {
	switch row.Name {
	case "builtin/fs":
		return builtin.NewFsPlugin(workspace), nil
	case "builtin/shell":
		return builtin.NewShellPlugin(workspace), nil
	case "builtin/time":
		return builtin.NewTimePlugin(), nil
	case "builtin/todo":
		return builtin.NewTodoPlugin(), nil
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
	systemPrompt := agent.DefaultSystemPrompt + fmt.Sprintf(
		"\n\nRuntime context:\n- model: %s\n- workspace: %s\n- tools: %s",
		r.cfg.Model, r.cfg.Workspace, strings.Join(toolNames, ", "),
	)
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
	})
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
	cfg, tree, logger, err := flags.load(stderr)
	if err != nil {
		fmt.Fprintf(stderr, "dsh: %v\n", err)
		return 1
	}
	if dumped, code := dumpIfRequested(flags, cfg, tree, stdout, stderr); dumped {
		return code
	}
	if !tree.Enabled(profiles.TransportWeb) {
		fmt.Fprintf(stderr, "dsh: profile %q does not enable the web transport (%s)\n", tree.Profile, profiles.TransportWeb)
		return 1
	}
	rt, err := bootstrap(cfg, tree, logger)
	if err != nil {
		fmt.Fprintf(stderr, "dsh: boot: %v\n", err)
		return 1
	}
	defer rt.close()

	var runner server.ChatRunner
	if cfg.APIKey() == "" {
		logger.Warn("model API key is not configured; /api/chat will return 503", "env", cfg.APIKeyEnv)
	} else {
		runner = func(ctx context.Context, sess *session.Session, input string) (string, error) {
			loop, err := rt.newLoop(sess)
			if err != nil {
				return "", err
			}
			return loop.Run(ctx, input)
		}
	}

	srv, err := server.New(server.Options{Config: cfg, Store: rt.store, Runner: runner, Events: rt.app.Events(), Logger: logger})
	if err != nil {
		fmt.Fprintf(stderr, "dsh: web: %v\n", err)
		return 1
	}
	address := net.JoinHostPort(*host, strconv.Itoa(*port))
	fmt.Fprintf(stdout, "dsh web: http://%s/\n", address)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Run(ctx, address); err != nil {
		fmt.Fprintf(stderr, "dsh: web: %v\n", err)
		return 1
	}
	return 0
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
	cfg, tree, logger, err := flags.load(stderr)
	if err != nil {
		fmt.Fprintf(stderr, "dsh: %v\n", err)
		return 1
	}
	if dumped, code := dumpIfRequested(flags, cfg, tree, stdout, stderr); dumped {
		return code
	}
	if !tree.Enabled(profiles.TransportCLI) {
		fmt.Fprintf(stderr, "dsh: profile %q does not enable the CLI transport (%s)\n", tree.Profile, profiles.TransportCLI)
		return 1
	}
	rt, err := bootstrap(cfg, tree, logger)
	if err != nil {
		fmt.Fprintf(stderr, "dsh: boot: %v\n", err)
		return 1
	}
	defer rt.close()

	var sess *session.Session
	if strings.TrimSpace(*sessionID) != "" {
		sess, err = rt.store.Load(*sessionID)
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *prompt != "" {
		reply, err := loop.Run(ctx, *prompt)
		if err != nil {
			fmt.Fprintf(stderr, "dsh: %v\n", err)
			return 1
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
	cfg, tree, logger, err := flags.load(stderr)
	if err != nil {
		fmt.Fprintf(stderr, "dsh: %v\n", err)
		return 1
	}
	if dumped, code := dumpIfRequested(flags, cfg, tree, stdout, stderr); dumped {
		return code
	}
	rt, err := bootstrap(cfg, tree, logger)
	if err != nil {
		fmt.Fprintf(stderr, "dsh: boot: %v\n", err)
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
	cfg, tree, _, err := flags.load(stderr)
	if err != nil {
		fmt.Fprintf(stderr, "dsh: %v\n", err)
		return 1
	}
	if dumped, code := dumpIfRequested(flags, cfg, tree, stdout, stderr); dumped {
		return code
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "dsh: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "config file: %s\napi key: %s\n\n%s\n", path, apiKeyStatus(cfg), data)
	return 0
}

func apiKeyStatus(cfg config.Config) string {
	if cfg.APIKey() == "" {
		return "not set (" + cfg.APIKeyEnv + ")"
	}
	return "set (" + cfg.APIKeyEnv + ")"
}
