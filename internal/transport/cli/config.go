package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/userInner/kern/internal/configuration"
)

func runConfig(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("config requires path, get, or set")
	}
	switch args[0] {
	case "path":
		if len(args) != 1 {
			return errors.New("config path does not accept arguments")
		}
		_, err := fmt.Fprintln(stdout, configuration.DefaultPath())
		return err
	case "get":
		return getConfig(args[1:], stdout, stderr)
	case "set":
		return setConfig(args[1:], stdout)
	default:
		return fmt.Errorf("unknown config command %q", args[0])
	}
}

func getConfig(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("config get", flag.ContinueOnError)
	flags.SetOutput(stderr)
	effective := flags.Bool("effective", false, "include environment overrides")
	output := flags.String("output", "json", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("config get accepts at most one key")
	}
	path := configuration.DefaultPath()
	var config configuration.File
	var err error
	if *effective {
		config, err = configuration.LoadEffective(path)
	} else {
		config, err = configuration.Load(path)
	}
	if err != nil {
		return err
	}
	if flags.NArg() == 0 {
		if *output != "json" {
			return errors.New("config get without a key requires --output json")
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(config)
	}
	value, err := configValue(config, flags.Arg(0))
	if err != nil {
		return err
	}
	if *output == "json" {
		return json.NewEncoder(stdout).Encode(map[string]any{"key": flags.Arg(0), "value": value})
	}
	if *output != "text" {
		return fmt.Errorf("unsupported output format %q", *output)
	}
	_, err = fmt.Fprintln(stdout, value)
	return err
}

func setConfig(args []string, stdout io.Writer) error {
	if len(args) != 2 {
		return errors.New("config set requires KEY VALUE")
	}
	path := configuration.DefaultPath()
	config, err := configuration.Load(path)
	if err != nil {
		return err
	}
	if err := setConfigValue(&config, args[0], args[1]); err != nil {
		return err
	}
	if err := configuration.Save(path, config); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s=%v\n", args[0], mustConfigValue(config, args[0]))
	return err
}

func configValue(config configuration.File, key string) (any, error) {
	switch key {
	case "server.address":
		return config.Server.Address, nil
	case "storage.data_dir":
		return config.Storage.DataDir, nil
	case "storage.workspace_dir":
		return config.Storage.WorkspaceDir, nil
	case "storage.retention_days":
		return config.Storage.RetentionDays, nil
	case "runtime.max_active_tasks":
		return config.Runtime.MaxActiveTasks, nil
	case "runtime.task_timeout":
		return config.Runtime.TaskTimeout.String(), nil
	case "runtime.max_turns":
		return config.Runtime.MaxTurns, nil
	case "runtime.max_tool_calls":
		return config.Runtime.MaxToolCalls, nil
	case "runtime.max_tokens":
		return config.Runtime.MaxTokens, nil
	case "runtime.max_cost_usd":
		return config.Runtime.MaxCostUSD, nil
	case "policy.profile":
		return config.Policy.Profile, nil
	case "plugins.auto_activate":
		return config.Plugins.AutoActivate, nil
	case "eval.workers":
		return config.Eval.Workers, nil
	case "observability.log_level":
		return config.Observability.LogLevel, nil
	case "observability.metrics_enabled":
		return config.Observability.MetricsEnabled, nil
	default:
		return nil, fmt.Errorf("unsupported config key %q", key)
	}
}

func mustConfigValue(config configuration.File, key string) any {
	value, _ := configValue(config, key)
	return value
}

func setConfigValue(config *configuration.File, key, raw string) error {
	raw = strings.TrimSpace(raw)
	parseInt := func() (int, error) {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return 0, fmt.Errorf("config set %s: %w", key, err)
		}
		return value, nil
	}
	switch key {
	case "server.address":
		config.Server.Address = raw
	case "storage.data_dir":
		config.Storage.DataDir = raw
	case "storage.workspace_dir":
		config.Storage.WorkspaceDir = raw
	case "storage.retention_days":
		value, err := parseInt()
		if err != nil {
			return err
		}
		config.Storage.RetentionDays = value
	case "runtime.max_active_tasks":
		value, err := parseInt()
		if err != nil {
			return err
		}
		config.Runtime.MaxActiveTasks = value
	case "runtime.task_timeout":
		value, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("config set %s: %w", key, err)
		}
		config.Runtime.TaskTimeout = configuration.Duration{Duration: value}
	case "runtime.max_turns":
		value, err := parseInt()
		if err != nil {
			return err
		}
		config.Runtime.MaxTurns = value
	case "runtime.max_tool_calls":
		value, err := parseInt()
		if err != nil {
			return err
		}
		config.Runtime.MaxToolCalls = value
	case "runtime.max_tokens":
		value, err := parseInt()
		if err != nil {
			return err
		}
		config.Runtime.MaxTokens = value
	case "runtime.max_cost_usd":
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("config set %s: %w", key, err)
		}
		config.Runtime.MaxCostUSD = value
	case "policy.profile":
		config.Policy.Profile = raw
	case "plugins.auto_activate":
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("config set %s: %w", key, err)
		}
		config.Plugins.AutoActivate = value
	case "eval.workers":
		value, err := parseInt()
		if err != nil {
			return err
		}
		config.Eval.Workers = value
	case "observability.log_level":
		config.Observability.LogLevel = raw
	case "observability.metrics_enabled":
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("config set %s: %w", key, err)
		}
		config.Observability.MetricsEnabled = value
	default:
		return fmt.Errorf("unsupported config key %q", key)
	}
	return configuration.Validate(*config)
}
