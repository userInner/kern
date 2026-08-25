// Package cli implements Kern's command line interface.
package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/userInner/kern/internal/app"
	"github.com/userInner/kern/internal/configuration"
	"github.com/userInner/kern/internal/evalservice"
	"github.com/userInner/kern/internal/pluginruntime/wazerosandbox"
	"github.com/userInner/kern/internal/secret/systemvault"
	"github.com/userInner/kern/internal/task"
	"github.com/userInner/kern/internal/transport/httpapi"
)

var Version = "dev"

// Execute parses args, runs one command, and returns an exit code.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return ExecuteWithInput(ctx, args, os.Stdin, stdout, stderr)
}

// ExecuteWithInput is Execute with an injectable input stream for chat clients and tests.
func ExecuteWithInput(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" || args[0] == "version" {
		if args[0] == "version" {
			_, err := fmt.Fprintln(stdout, Version)
			if err != nil {
				fmt.Fprintf(stderr, "kern: %v\n", err)
				return 1
			}
			return 0
		}
		printUsage(stdout)
		return 0
	}
	if args[0] == "config" {
		if err := runConfig(args[1:], stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "kern: %v\n", err)
			return 1
		}
		return 0
	}
	defaults, err := configuration.LoadEffective(configuration.DefaultPath())
	if err != nil {
		fmt.Fprintf(stderr, "kern: %v\n", err)
		return 1
	}
	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{
		Level: configuredLogLevel(defaults.Observability.LogLevel),
	}))
	switch args[0] {
	case "run":
		err = runTask(ctx, args[1:], stdout, stderr, logger, defaults)
	case "chat":
		err = runChat(ctx, args[1:], stdin, stdout, stderr, logger, defaults)
	case "task":
		err = runTaskCommand(ctx, args[1:], stdout, stderr, logger, defaults)
	case "plugin":
		err = runPlugin(ctx, args[1:], stdout, stderr, logger, defaults)
	case "eval":
		err = runEval(ctx, args[1:], stdout, stderr, logger, defaults)
	case "doctor":
		err = runDoctor(ctx, args[1:], stdout, stderr, logger, defaults)
	case "web":
		err = runWeb(ctx, args[1:], stderr, logger, defaults)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "kern: %v\n", err)
		return 1
	}
	return 0
}

func runChat(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	logger *slog.Logger,
	defaults configuration.File,
) error {
	flags := flag.NewFlagSet("chat", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", defaults.Storage.DataDir, "local Kern data directory")
	workspaceDir := flags.String("workspace", configuredWorkspace(defaults), "task workspace directory")
	output := flags.String("output", "text", "output format: text or jsonl")
	once := flags.Bool("once", false, "run the positional message and exit")
	var enablePlugins, disablePlugins stringListFlag
	flags.Var(&enablePlugins, "plugin", "force-enable an installed plugin for this task (repeatable)")
	flags.Var(&disablePlugins, "disable-plugin", "disable an installed plugin for this task (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *output != "text" && *output != "jsonl" {
		return fmt.Errorf("unsupported output format %q", *output)
	}
	runtime, err := openRuntime(ctx, *dataDir, *workspaceDir, logger, defaults)
	if err != nil {
		return err
	}
	defer runtime.Close()

	taskID := ""
	process := func(input string) error {
		input = strings.TrimSpace(input)
		if input == "" {
			return nil
		}
		var current task.Task
		var err error
		if taskID == "" {
			current, err = runtime.SubmitWithPlugins(ctx, "", input, "", enablePlugins, disablePlugins)
		} else {
			current, err = runtime.SubmitInput(ctx, taskID, input)
		}
		if err != nil {
			return err
		}
		taskID = current.ID
		current, err = runtime.WaitForBoundary(ctx, current.ID)
		if err != nil {
			return err
		}
		if current.Status == task.StatusWaitingApproval {
			return fmt.Errorf("task %s requires approval; continue in Kern Web or through the approval API", current.ID)
		}
		if current.Status == task.StatusWaitingInput {
			return fmt.Errorf("task %s paused before producing a result", current.ID)
		}
		if *output == "jsonl" {
			return json.NewEncoder(stdout).Encode(current)
		}
		if current.Status != task.StatusCompleted && current.Status != task.StatusPartiallyCompleted {
			return fmt.Errorf("task ended with status %s: %s", current.Status, current.ErrorMessage)
		}
		_, err = fmt.Fprintln(stdout, current.Result)
		return err
	}

	initial := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if initial != "" {
		if err := process(initial); err != nil {
			return err
		}
		if *once {
			return nil
		}
	} else if *once {
		return errors.New("chat --once requires a message")
	}
	fmt.Fprintln(stderr, "Kern chat 已启动。输入 /exit 结束；每条消息都会在同一任务的新 Attempt 中继续。")
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 4<<10), 64<<10)
	for {
		if _, err := fmt.Fprint(stderr, "kern> "); err != nil {
			return err
		}
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "/exit" || line == "/quit" {
			break
		}
		if err := process(line); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func runTaskCommand(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	logger *slog.Logger,
	defaults configuration.File,
) error {
	if len(args) == 0 {
		return errors.New("task requires list, show, pause, resume, cancel, or retry")
	}
	switch args[0] {
	case "list":
		return listTasks(ctx, args[1:], stdout, stderr, logger, defaults)
	case "show":
		return showTask(ctx, args[1:], stdout, stderr, logger, defaults)
	case "pause", "resume", "cancel", "retry":
		return controlTask(ctx, args[0], args[1:], stdout, stderr, logger, defaults)
	default:
		return fmt.Errorf("unknown task command %q", args[0])
	}
}

func listTasks(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	logger *slog.Logger,
	defaults configuration.File,
) error {
	flags := flag.NewFlagSet("task list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", defaults.Storage.DataDir, "local Kern data directory")
	workspaceDir := flags.String("workspace", configuredWorkspace(defaults), "task workspace directory")
	output := flags.String("output", "text", "output format: text, json, or jsonl")
	limit := flags.Int("limit", 50, "maximum tasks to return")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *limit < 1 || *limit > 1000 {
		return errors.New("task list requires no arguments and limit must be between 1 and 1000")
	}
	runtime, err := openRuntime(ctx, *dataDir, *workspaceDir, logger, defaults)
	if err != nil {
		return err
	}
	defer runtime.Close()
	items, err := runtime.Store.ListTasks(ctx, *limit)
	if err != nil {
		return err
	}
	switch *output {
	case "text":
		for _, item := range items {
			if _, err := fmt.Fprintf(stdout, "%s\t%s\t%s\n", item.ID, item.Status, item.Title); err != nil {
				return err
			}
		}
		return nil
	case "json":
		return json.NewEncoder(stdout).Encode(map[string]any{"schema_version": task.SchemaVersion, "tasks": items})
	case "jsonl":
		encoder := json.NewEncoder(stdout)
		for _, item := range items {
			if err := encoder.Encode(item); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", *output)
	}
}

func showTask(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	logger *slog.Logger,
	defaults configuration.File,
) error {
	flags := flag.NewFlagSet("task show", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", defaults.Storage.DataDir, "local Kern data directory")
	workspaceDir := flags.String("workspace", configuredWorkspace(defaults), "task workspace directory")
	output := flags.String("output", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("task show requires exactly one task ID")
	}
	runtime, err := openRuntime(ctx, *dataDir, *workspaceDir, logger, defaults)
	if err != nil {
		return err
	}
	defer runtime.Close()
	item, err := runtime.Store.GetTask(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	switch *output {
	case "text":
		_, err = fmt.Fprintf(
			stdout,
			"ID: %s\nStatus: %s\nTitle: %s\nGoal: %s\nResult: %s\nError: %s\n",
			item.ID,
			item.Status,
			item.Title,
			item.Goal,
			item.Result,
			item.ErrorMessage,
		)
		return err
	case "json":
		return json.NewEncoder(stdout).Encode(item)
	default:
		return fmt.Errorf("unsupported output format %q", *output)
	}
}

func controlTask(
	ctx context.Context,
	action string,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	logger *slog.Logger,
	defaults configuration.File,
) error {
	flags := flag.NewFlagSet("task "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", defaults.Storage.DataDir, "local Kern data directory")
	workspaceDir := flags.String("workspace", configuredWorkspace(defaults), "task workspace directory")
	output := flags.String("output", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("task %s requires exactly one task ID", action)
	}
	runtime, err := openRuntime(ctx, *dataDir, *workspaceDir, logger, defaults)
	if err != nil {
		return err
	}
	defer runtime.Close()
	var item task.Task
	switch action {
	case "pause":
		item, err = runtime.Pause(ctx, flags.Arg(0))
	case "resume":
		item, err = runtime.Resume(ctx, flags.Arg(0))
	case "cancel":
		item, err = runtime.Cancel(ctx, flags.Arg(0))
	case "retry":
		item, err = runtime.Retry(ctx, flags.Arg(0))
	default:
		return fmt.Errorf("unsupported task action %q", action)
	}
	if err != nil {
		return err
	}
	if *output == "json" {
		return json.NewEncoder(stdout).Encode(item)
	}
	if *output != "text" {
		return fmt.Errorf("unsupported output format %q", *output)
	}
	_, err = fmt.Fprintf(stdout, "%s\t%s\n", item.ID, item.Status)
	return err
}

func runDoctor(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	logger *slog.Logger,
	defaults configuration.File,
) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", defaults.Storage.DataDir, "local Kern data directory")
	workspaceDir := flags.String("workspace", configuredWorkspace(defaults), "task workspace directory")
	output := flags.String("output", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("doctor does not accept positional arguments")
	}
	runtime, err := openRuntime(ctx, *dataDir, *workspaceDir, logger, defaults)
	if err != nil {
		return err
	}
	defer runtime.Close()
	models, err := runtime.Store.ListModelConfigs(ctx)
	if err != nil {
		return err
	}
	capabilities := runtime.Capabilities()
	modelExecutionConfigured := runtime.Mode != "offline-baseline"
	for _, modelConfig := range models {
		modelExecutionConfigured = modelExecutionConfigured || modelConfig.Enabled
	}
	warnings := make([]string, 0, 3)
	if !modelExecutionConfigured {
		warnings = append(warnings, "model_execution_unconfigured")
	}
	if !capabilities.WritableCredentialStore {
		warnings = append(warnings, "system_credential_store_unavailable")
	}
	if !capabilities.WASMPluginRuntime {
		warnings = append(warnings, "wasm_plugin_runtime_unavailable")
	}
	productionAdaptersReady := capabilities.WritableCredentialStore && capabilities.WASMPluginRuntime
	report := struct {
		SchemaVersion            string                  `json:"schema_version"`
		Status                   string                  `json:"status"`
		ProductionAdaptersReady  bool                    `json:"production_adapters_ready"`
		ModelExecutionConfigured bool                    `json:"model_execution_configured"`
		Capabilities             app.RuntimeCapabilities `json:"capabilities"`
		Warnings                 []string                `json:"warnings"`
		Mode                     string                  `json:"mode"`
		ConfigPath               string                  `json:"config_path"`
		DataDir                  string                  `json:"data_dir"`
		Workspace                string                  `json:"workspace"`
		ModelConfigs             int                     `json:"model_configs"`
		Workers                  int                     `json:"workers"`
		TaskTimeout              string                  `json:"task_timeout"`
	}{
		SchemaVersion:            task.SchemaVersion,
		Status:                   "ok",
		ProductionAdaptersReady:  productionAdaptersReady,
		ModelExecutionConfigured: modelExecutionConfigured,
		Capabilities:             capabilities,
		Warnings:                 warnings,
		Mode:                     runtime.Mode,
		ConfigPath:               configuration.DefaultPath(),
		DataDir:                  *dataDir,
		Workspace:                runtime.Workspace.Root(),
		ModelConfigs:             len(models),
		Workers:                  defaults.Runtime.MaxActiveTasks,
		TaskTimeout:              defaults.Runtime.TaskTimeout.String(),
	}
	switch *output {
	case "text":
		_, err = fmt.Fprintf(
			stdout,
			"Kern doctor: ok\nProduction adapters: %t\nModel execution configured: %t\nSystem credential store: %t\nWASM plugin runtime: %t\nWarnings: %s\nMode: %s\nConfig: %s\nData: %s\nWorkspace: %s\nWorkers: %d\nTask timeout: %s\nModel connections: %d\n",
			report.ProductionAdaptersReady,
			modelExecutionConfigured,
			report.Capabilities.WritableCredentialStore,
			report.Capabilities.WASMPluginRuntime,
			strings.Join(report.Warnings, ", "),
			report.Mode,
			report.ConfigPath,
			report.DataDir,
			report.Workspace,
			report.Workers,
			report.TaskTimeout,
			report.ModelConfigs,
		)
		return err
	case "json":
		return json.NewEncoder(stdout).Encode(report)
	default:
		return fmt.Errorf("unsupported output format %q", *output)
	}
}

func openRuntime(
	ctx context.Context,
	dataDir string,
	workspaceDir string,
	logger *slog.Logger,
	defaults configuration.File,
) (*app.Runtime, error) {
	config, err := applicationConfig(defaults.Runtime)
	if err != nil {
		return nil, err
	}
	config.DataDir = dataDir
	config.WorkspaceDir = workspaceDir
	config.Logger = logger
	config.AutoActivatePlugins = defaults.Plugins.AutoActivate
	config.PolicyProfile = defaults.Policy.Profile
	config.RetentionDays = defaults.Storage.RetentionDays
	config.SettingsPath = configuration.DefaultPath()
	return app.Open(ctx, config)
}

func runTask(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	logger *slog.Logger,
	defaults configuration.File,
) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", defaults.Storage.DataDir, "local Kern data directory")
	workspaceDir := flags.String("workspace", configuredWorkspace(defaults), "task workspace directory")
	output := flags.String("output", "text", "output format: text or json")
	var enablePlugins, disablePlugins stringListFlag
	flags.Var(&enablePlugins, "plugin", "force-enable an installed plugin for this task (repeatable)")
	flags.Var(&disablePlugins, "disable-plugin", "disable an installed plugin for this task (repeatable)")
	budgets := bindBudgetFlags(flags, defaults.Runtime)
	if err := flags.Parse(args); err != nil {
		return err
	}
	budgetConfig, err := budgets.config()
	if err != nil {
		return err
	}
	attachProductionAdapters(&budgetConfig)
	goal := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if goal == "" {
		return errors.New("run requires a task goal")
	}
	budgetConfig.DataDir = *dataDir
	budgetConfig.WorkspaceDir = *workspaceDir
	budgetConfig.Logger = logger
	budgetConfig.WorkerCount = defaults.Runtime.MaxActiveTasks
	budgetConfig.AutoActivatePlugins = defaults.Plugins.AutoActivate
	budgetConfig.PolicyProfile = defaults.Policy.Profile
	budgetConfig.RetentionDays = defaults.Storage.RetentionDays
	budgetConfig.SettingsPath = configuration.DefaultPath()
	runtime, err := app.Open(ctx, budgetConfig)
	if err != nil {
		return err
	}
	defer runtime.Close()
	fmt.Fprintf(stderr, "Kern mode: %s\n", runtime.Mode)

	created, err := runtime.SubmitWithPlugins(ctx, "", goal, "", enablePlugins, disablePlugins)
	if err != nil {
		return err
	}
	completed, err := runtime.WaitForBoundary(ctx, created.ID)
	if err != nil {
		return err
	}
	if completed.Status == task.StatusWaitingApproval {
		return fmt.Errorf("task %s requires approval; continue in Kern Web or through the approval API", completed.ID)
	}
	if completed.Status != task.StatusCompleted {
		return fmt.Errorf("task ended with status %s: %s", completed.Status, completed.ErrorMessage)
	}
	switch *output {
	case "text":
		_, err = fmt.Fprintln(stdout, completed.Result)
	case "json":
		err = json.NewEncoder(stdout).Encode(completed)
	default:
		return fmt.Errorf("unsupported output format %q", *output)
	}
	return err
}

func runWeb(
	ctx context.Context,
	args []string,
	stderr io.Writer,
	logger *slog.Logger,
	defaults configuration.File,
) error {
	flags := flag.NewFlagSet("web", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", defaults.Storage.DataDir, "local Kern data directory")
	workspaceDir := flags.String("workspace", configuredWorkspace(defaults), "task workspace directory")
	evalRoot := flags.String("eval-root", "", "trusted evaluation suite directory (optional)")
	addr := flags.String("addr", defaults.Server.Address, "local listen address")
	openPage := flags.Bool("open", true, "open Kern in the default browser")
	budgets := bindBudgetFlags(flags, defaults.Runtime)
	if err := flags.Parse(args); err != nil {
		return err
	}
	budgetConfig, err := budgets.config()
	if err != nil {
		return err
	}
	attachProductionAdapters(&budgetConfig)
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return fmt.Errorf("parsing listen address: %w", err)
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return errors.New("web: first release only binds to a loopback address")
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("web: opening loopback listener: %w", err)
	}
	defer listener.Close()
	webURL, err := loopbackWebURL(listener)
	if err != nil {
		return err
	}

	budgetConfig.DataDir = *dataDir
	budgetConfig.WorkspaceDir = *workspaceDir
	budgetConfig.Logger = logger
	budgetConfig.WorkerCount = defaults.Runtime.MaxActiveTasks
	budgetConfig.AutoActivatePlugins = defaults.Plugins.AutoActivate
	budgetConfig.PolicyProfile = defaults.Policy.Profile
	budgetConfig.RetentionDays = defaults.Storage.RetentionDays
	budgetConfig.SettingsPath = configuration.DefaultPath()
	runtime, err := app.Open(ctx, budgetConfig)
	if err != nil {
		return err
	}
	defer runtime.Close()
	workspaceRoot := runtime.Workspace.Root()
	pluginImportRoot, err := os.OpenRoot(workspaceRoot)
	if err != nil {
		return fmt.Errorf("web: opening plugin import root: %w", err)
	}
	defer pluginImportRoot.Close()
	evals, err := evalservice.New(ctx, evalservice.Config{
		DataRoot:       filepath.Join(*dataDir, "evals"),
		EvalRoot:       strings.TrimSpace(*evalRoot),
		WritableRoots:  []string{workspaceRoot},
		Store:          runtime.Store,
		PluginResolver: runtime.Plugins,
		MaxActive:      1,
		MaxTurns:       budgetConfig.MaxTurns, MaxToolCalls: budgetConfig.MaxToolCalls,
		Logger: logger,
	})
	if err != nil {
		return err
	}
	defer evals.Close()
	token, err := sessionToken()
	if err != nil {
		return err
	}
	handler, err := httpapi.New(httpapi.Config{
		Store:                  runtime.Store,
		Artifacts:              runtime.Artifacts,
		Submitter:              runtime,
		Models:                 runtime,
		Plugins:                runtime.Plugins,
		Evals:                  evals,
		MetricsEnabled:         defaults.Observability.MetricsEnabled,
		Token:                  token,
		Mode:                   runtime.Mode,
		Logger:                 logger,
		PluginImportRoot:       pluginImportRoot,
		Origin:                 webURL,
		AllowPlainHTTPLoopback: true,
	})
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}
	fmt.Fprintf(stderr, "Kern Web: %s (%s)\n", webURL, runtime.Mode)
	if *openPage {
		go openBrowser(webURL, logger)
	}
	return serveWebUntilCancelled(ctx, server, listener, handler, logger)
}

type shutdownResult struct {
	triggered bool
	err       error
}

func serveWebUntilCancelled(
	ctx context.Context,
	server *http.Server,
	listener net.Listener,
	handler *httpapi.Server,
	logger *slog.Logger,
) error {
	watchStopped := make(chan struct{})
	shutdownDone := make(chan shutdownResult, 1)
	go func() {
		select {
		case <-ctx.Done():
			shutdownDone <- shutdownResult{triggered: true, err: stopWebServer(server, handler, logger)}
		case <-watchStopped:
			shutdownDone <- shutdownResult{}
		}
	}()

	serveErr := server.Serve(listener)
	close(watchStopped)
	result := <-shutdownDone
	if errors.Is(serveErr, http.ErrServerClosed) {
		if result.triggered {
			return result.err
		}
		return stopWebServer(server, handler, logger)
	}

	if !result.triggered {
		result.err = stopWebServer(server, handler, logger)
	}
	return errors.Join(fmt.Errorf("serving web: %w", serveErr), result.err)
}

func stopWebServer(server *http.Server, handler *httpapi.Server, logger *slog.Logger) error {
	handler.BeginShutdown()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := server.Shutdown(shutdownCtx)
	cancel()
	if shutdownErr != nil {
		logger.Warn("graceful HTTP shutdown did not finish; closing active connections", "error", shutdownErr)
		shutdownErr = server.Close()
	}
	idleErr := handler.WaitForIdle(context.Background())
	return errors.Join(shutdownErr, idleErr)
}

func loopbackWebURL(listener net.Listener) (string, error) {
	if listener == nil || listener.Addr() == nil {
		return "", errors.New("web: loopback listener is unavailable")
	}
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return "", fmt.Errorf("web: reading loopback listener address: %w", err)
	}
	address := net.ParseIP(host)
	if address == nil || !address.IsLoopback() {
		return "", errors.New("web: resolved listener address is not loopback")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", errors.New("web: resolved listener port is invalid")
	}
	return "http://" + net.JoinHostPort(address.String(), port), nil
}

func sessionToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func openBrowser(url string, logger *slog.Logger) {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if err := command.Run(); err != nil {
		logger.Debug("browser did not open", "error", err)
	}
}

func configuredWorkspace(config configuration.File) string {
	if config.Storage.WorkspaceDir != "" {
		return config.Storage.WorkspaceDir
	}
	directory, err := os.Getwd()
	if err != nil {
		return "."
	}
	return directory
}

type budgetFlags struct {
	maxTurns     *int
	maxToolCalls *int
	maxTokens    *int
	maxCostUSD   *float64
	maxDuration  *time.Duration
}

type stringListFlag []string

func (values *stringListFlag) String() string {
	return strings.Join(*values, ",")
}

func (values *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("plugin ID must not be empty")
	}
	*values = append(*values, value)
	return nil
}

func bindBudgetFlags(flags *flag.FlagSet, defaults configuration.Runtime) budgetFlags {
	return budgetFlags{
		maxTurns:     flags.Int("max-turns", defaults.MaxTurns, "maximum model turns per task"),
		maxToolCalls: flags.Int("max-tool-calls", defaults.MaxToolCalls, "maximum tool calls per task"),
		maxTokens:    flags.Int("max-tokens", defaults.MaxTokens, "maximum input plus output tokens per task"),
		maxCostUSD:   flags.Float64("max-cost-usd", defaults.MaxCostUSD, "maximum provider-reported model cost per task"),
		maxDuration:  flags.Duration("max-duration", defaults.TaskTimeout.Duration, "maximum model execution duration per task"),
	}
}

func (b budgetFlags) config() (app.Config, error) {
	invalidCount := *b.maxTurns < 0 || *b.maxToolCalls < 0 || *b.maxTokens < 0
	invalidCost := math.IsNaN(*b.maxCostUSD) || math.IsInf(*b.maxCostUSD, 0) || *b.maxCostUSD < 0
	if invalidCount || invalidCost || *b.maxDuration < 0 {
		return app.Config{}, errors.New("task budgets must be finite and non-negative")
	}
	costMicros := math.Round(*b.maxCostUSD * 1_000_000)
	if math.IsInf(costMicros, 0) || costMicros > float64(math.MaxInt64) {
		return app.Config{}, errors.New("max-cost-usd is too large")
	}
	return app.Config{
		MaxTurns:        *b.maxTurns,
		MaxToolCalls:    *b.maxToolCalls,
		MaxTokens:       *b.maxTokens,
		MaxCostMicros:   int64(costMicros),
		MaxTaskDuration: *b.maxDuration,
	}, nil
}

func applicationConfig(defaults configuration.Runtime) (app.Config, error) {
	flags := budgetFlags{
		maxTurns:     &defaults.MaxTurns,
		maxToolCalls: &defaults.MaxToolCalls,
		maxTokens:    &defaults.MaxTokens,
		maxCostUSD:   &defaults.MaxCostUSD,
		maxDuration:  &defaults.TaskTimeout.Duration,
	}
	config, err := flags.config()
	if err != nil {
		return app.Config{}, err
	}
	config.WorkerCount = defaults.MaxActiveTasks
	attachProductionAdapters(&config)
	return config, nil
}

func attachProductionAdapters(config *app.Config) {
	config.SecretVault = systemvault.New()
	config.WASMSandbox = wazerosandbox.New()
}

func configuredLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func printUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, `Kern Core

Usage:
  kern run [--data-dir PATH] [--workspace PATH] [--output text|json] [budget flags] <goal>
  kern chat [--data-dir PATH] [--workspace PATH] [--output text|jsonl] [--once] [message]
  kern web [--data-dir PATH] [--workspace PATH] [--eval-root PATH] [--addr 127.0.0.1:8787] [--open=true] [budget flags]
  kern task list [--data-dir PATH] [--output text|json|jsonl]
  kern task show|pause|resume|cancel|retry [flags] TASK_ID
  kern plugin digest|list|inspect|install|enable|disable|remove
  kern eval validate|run|compare|show
  kern config path|get|set
  kern doctor [--data-dir PATH] [--workspace PATH] [--output text|json]
  kern version`)
}
