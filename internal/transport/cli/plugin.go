package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/userInner/kern/internal/configuration"
	"github.com/userInner/kern/internal/plugin"
)

type pluginCommandOptions struct {
	dataDir      *string
	workspaceDir *string
	output       *string
}

func bindPluginCommandOptions(
	flags *flag.FlagSet,
	defaults configuration.File,
) pluginCommandOptions {
	return pluginCommandOptions{
		dataDir:      flags.String("data-dir", defaults.Storage.DataDir, "local Kern data directory"),
		workspaceDir: flags.String("workspace", configuredWorkspace(defaults), "task workspace directory"),
		output:       flags.String("output", "text", "output format: text or json"),
	}
}

func runPlugin(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	logger *slog.Logger,
	defaults configuration.File,
) error {
	if len(args) == 0 {
		return errors.New("plugin requires digest, list, inspect, install, enable, disable, or remove")
	}
	switch args[0] {
	case "digest":
		return digestPlugin(args[1:], stdout, stderr)
	case "list":
		return listPlugins(ctx, args[1:], stdout, stderr, logger, defaults)
	case "inspect", "enable", "disable", "remove":
		return pluginAction(ctx, args[0], args[1:], stdout, stderr, logger, defaults)
	case "install":
		return installPlugin(ctx, args[1:], stdout, stderr, logger, defaults)
	default:
		return fmt.Errorf("unknown plugin command %q", args[0])
	}
}

func digestPlugin(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("plugin digest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("output", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("plugin digest requires exactly one local directory")
	}
	digest, err := plugin.PackageDigest(flags.Arg(0))
	if err != nil {
		return err
	}
	switch *output {
	case "text":
		_, err = fmt.Fprintln(stdout, digest)
		return err
	case "json":
		return json.NewEncoder(stdout).Encode(map[string]string{
			"schema_version": plugin.SchemaVersion,
			"digest":         digest,
		})
	default:
		return fmt.Errorf("unsupported output format %q", *output)
	}
}

func listPlugins(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	logger *slog.Logger,
	defaults configuration.File,
) error {
	flags := flag.NewFlagSet("plugin list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := bindPluginCommandOptions(flags, defaults)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("plugin list does not accept arguments")
	}
	runtime, err := openRuntime(ctx, *options.dataDir, *options.workspaceDir, logger, defaults)
	if err != nil {
		return err
	}
	defer runtime.Close()
	items, err := runtime.Plugins.List(ctx)
	if err != nil {
		return err
	}
	if *options.output == "json" {
		return json.NewEncoder(stdout).Encode(map[string]any{
			"schema_version": plugin.SchemaVersion,
			"plugins":        items,
		})
	}
	if *options.output != "text" {
		return fmt.Errorf("unsupported output format %q", *options.output)
	}
	for _, item := range items {
		if err := writePlugin(stdout, "text", item); err != nil {
			return err
		}
	}
	return nil
}

func installPlugin(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	logger *slog.Logger,
	defaults configuration.File,
) error {
	flags := flag.NewFlagSet("plugin install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := bindPluginCommandOptions(flags, defaults)
	enable := flags.Bool("enable", false, "explicitly enable after successful installation")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("plugin install requires exactly one local directory")
	}
	runtime, err := openRuntime(ctx, *options.dataDir, *options.workspaceDir, logger, defaults)
	if err != nil {
		return err
	}
	defer runtime.Close()
	item, _, err := runtime.Plugins.Install(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	if *enable && !item.Enabled {
		item, err = runtime.Plugins.Enable(ctx, item.ID)
		if err != nil {
			return err
		}
	}
	return writePlugin(stdout, *options.output, item)
}

func pluginAction(
	ctx context.Context,
	action string,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	logger *slog.Logger,
	defaults configuration.File,
) error {
	flags := flag.NewFlagSet("plugin "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := bindPluginCommandOptions(flags, defaults)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("plugin %s requires exactly one plugin ID", action)
	}
	runtime, err := openRuntime(ctx, *options.dataDir, *options.workspaceDir, logger, defaults)
	if err != nil {
		return err
	}
	defer runtime.Close()
	pluginID := flags.Arg(0)
	switch action {
	case "inspect":
		item, err := runtime.Plugins.Get(ctx, pluginID)
		if err != nil {
			return err
		}
		return writePlugin(stdout, *options.output, item)
	case "enable":
		item, err := runtime.Plugins.Enable(ctx, pluginID)
		if err != nil {
			return err
		}
		return writePlugin(stdout, *options.output, item)
	case "disable":
		item, err := runtime.Plugins.Disable(ctx, pluginID)
		if err != nil {
			return err
		}
		return writePlugin(stdout, *options.output, item)
	case "remove":
		if err := runtime.Plugins.Remove(ctx, pluginID); err != nil {
			return err
		}
		if *options.output == "json" {
			return json.NewEncoder(stdout).Encode(map[string]any{
				"schema_version": plugin.SchemaVersion,
				"id":             pluginID,
				"removed":        true,
			})
		}
		if *options.output != "text" {
			return fmt.Errorf("unsupported output format %q", *options.output)
		}
		_, err := fmt.Fprintf(stdout, "%s\tremoved\n", pluginID)
		return err
	default:
		return fmt.Errorf("unsupported plugin action %q", action)
	}
}

func writePlugin(output io.Writer, format string, item plugin.Installed) error {
	if format == "json" {
		return json.NewEncoder(output).Encode(item)
	}
	if format != "text" {
		return fmt.Errorf("unsupported output format %q", format)
	}
	state := "disabled"
	if item.Enabled {
		state = "enabled"
	}
	_, err := fmt.Fprintf(output, "%s\t%s\t%s\t%s\n", item.ID, item.Version, state, item.Name)
	return err
}
