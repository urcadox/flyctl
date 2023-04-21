package run

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/samber/lo"
	"github.com/spf13/cobra"

	"github.com/superfly/flyctl/api"
	"github.com/superfly/flyctl/client"
	"github.com/superfly/flyctl/flaps"
	"github.com/superfly/flyctl/internal/appconfig"
	"github.com/superfly/flyctl/internal/command"
	"github.com/superfly/flyctl/internal/command/ssh"
	"github.com/superfly/flyctl/internal/flag"
	"github.com/superfly/flyctl/iostreams"
)

func New() *cobra.Command {
	const (
		usage = "run --machine <id> <command> [args]"
		short = "Run a command in an existing machine"
		long  = `Run a command in an existing machine. If the command name matches an
alias in the [commands] section of fly.toml, the full command is
substituted and any additional arguments are appended. Otherwise, the
command and arguments are run as-is.`
	)
	cmd := command.New(usage, short, long, runRun, command.RequireSession, command.RequireAppName)

	cmd.Args = cobra.MinimumNArgs(1)
	flag.Add(
		cmd,
		flag.App(),
		flag.AppConfig(),
		flag.String{
			Name:        "machine",
			Shorthand:   "m",
			Description: "ID of the machine to connect to",
		},
		flag.String{
			Name:        "user",
			Shorthand:   "u",
			Description: "Unix username to connect as",
			Default:     ssh.DefaultSshUsername,
		},
	)
	cmd.MarkFlagRequired("machine")

	return cmd
}

func runRun(ctx context.Context) error {
	appName := appconfig.NameFromContext(ctx)
	apiClient := client.FromContext(ctx).API()

	app, err := apiClient.GetAppCompact(ctx, appName)
	if err != nil {
		return fmt.Errorf("failed to get app: %w", err)
	}

	if app.PlatformVersion != "machines" {
		return errors.New("run is only supported for the machines platform")
	}

	flapsClient, err := flaps.New(ctx, app)
	if err != nil {
		return fmt.Errorf("failed to create flaps client: %w", err)
	}
	ctx = flaps.NewContext(ctx, flapsClient)

	appConfig := appconfig.ConfigFromContext(ctx)
	if appConfig == nil {
		appConfig, err = appconfig.FromRemoteApp(ctx, appName)
		if err != nil {
			return fmt.Errorf("failed to fetch app config from backend: %w", err)
		}
	}

	if err, extraInfo := appConfig.ValidateForMachinesPlatform(ctx); err != nil {
		fmt.Fprintln(iostreams.FromContext(ctx).ErrOut, extraInfo)
		return err
	}

	command := selectCommand(ctx, appConfig)

	machine, err := selectMachine(ctx)
	if err != nil {
		return err
	}

	_, dialer, err := ssh.BringUpAgent(ctx, apiClient, app, false)
	if err != nil {
		return err
	}

	params := &ssh.ConnectParams{
		Ctx:            ctx,
		Org:            app.Organization,
		Dialer:         dialer,
		Username:       flag.GetString(ctx, "user"),
		DisableSpinner: false,
	}
	sshClient, err := ssh.Connect(params, machine.PrivateIP)
	if err != nil {
		return err
	}

	return ssh.Console(ctx, sshClient, command, true)
}

func selectCommand(ctx context.Context, appConfig *appconfig.Config) string {
	ourArgs := flag.Args(ctx)
	specified := ourArgs[0]
	base := quote(specified)
	for name, cmd := range appConfig.Commands {
		if name == specified {
			base = cmd
			break
		}
	}

	cmdArgs := append([]string{base}, lo.Map(ourArgs[1:], func(arg string, _ int) string {
		return quote(arg)
	})...)
	return strings.Join(cmdArgs, " ")
}

func quote(s string) string {
	var builder strings.Builder
	for {
		switch i := strings.Index(s, "'"); i {
		case -1:
			if s != "" {
				builder.WriteByte('\'')
				builder.WriteString(s)
				builder.WriteByte('\'')
			}
			return builder.String()
		case 0:
			builder.WriteString("\\'")
			s = s[1:]
		default:
			builder.WriteByte('\'')
			builder.WriteString(s[0:i])
			builder.WriteByte('\'')
			builder.WriteString("\\'")
			s = s[i+1:]
		}
	}
}

func selectMachine(ctx context.Context) (*api.Machine, error) {
	flapsClient := flaps.FromContext(ctx)

	machineID := flag.GetString(ctx, "machine")
	machine, err := flapsClient.Get(ctx, machineID)
	if err != nil {
		return nil, err
	}
	if machine.State != api.MachineStateStarted {
		return nil, fmt.Errorf("machine %s is not started", machineID)
	}
	if machine.IsFlyAppsReleaseCommand() {
		return nil, fmt.Errorf("machine %s is a release command machine", machineID)
	}

	return machine, nil
}
