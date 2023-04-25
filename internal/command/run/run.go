package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/samber/lo"
	"github.com/spf13/cobra"

	"github.com/superfly/flyctl/api"
	"github.com/superfly/flyctl/client"
	"github.com/superfly/flyctl/flaps"
	"github.com/superfly/flyctl/internal/appconfig"
	"github.com/superfly/flyctl/internal/command"
	"github.com/superfly/flyctl/internal/command/ssh"
	"github.com/superfly/flyctl/internal/flag"
	"github.com/superfly/flyctl/internal/prompt"
	"github.com/superfly/flyctl/iostreams"
	"github.com/superfly/flyctl/terminal"
)

func New() *cobra.Command {
	const (
		usage = "run <command> [args]"
		short = "Run a command in a new or existing machine"
		long  = `Run a command in a new or existing machine. A new machine is created by
default using the app's most recently deployed image. An existing
machine can be used instead with -m/--machine.

If the command name matches an alias in the [commands] section of
fly.toml, the full command is substituted and any additional arguments
are appended. Otherwise, the command and arguments are run as-is.`
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
		flag.Bool{
			Name:        "select",
			Shorthand:   "s",
			Description: "Select the machine on which to execute the command from a list",
			Default:     false,
		},
		flag.String{
			Name:        "user",
			Shorthand:   "u",
			Description: "Unix username to connect as",
			Default:     ssh.DefaultSshUsername,
		},
	)

	return cmd
}

func runRun(ctx context.Context) error {
	var (
		io        = iostreams.FromContext(ctx)
		colorize  = io.ColorScheme()
		appName   = appconfig.NameFromContext(ctx)
		apiClient = client.FromContext(ctx).API()
	)

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
		fmt.Fprintln(io.ErrOut, extraInfo)
		return err
	}

	command := selectCommand(ctx, appConfig)

	machine, ephemeral, err := selectMachine(ctx, app, appConfig)
	if err != nil {
		return err
	}

	if ephemeral {
		defer func() {
			const stopTimeout = 5 * time.Second

			stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
			defer cancel()

			stopInput := api.StopMachineInput{
				ID:      machine.ID,
				Timeout: api.Duration{Duration: stopTimeout},
			}
			if err := flapsClient.Stop(stopCtx, stopInput, ""); err != nil {
				terminal.Warnf("Failed to stop ephemeral runner machine: %v\n", err)
				terminal.Warn("You may need to destroy it manually (`fly machine destroy`).")
				return
			}

			fmt.Fprintf(io.Out, "Waiting for ephemeral runner machine %s to be destroyed ...", colorize.Bold(machine.ID))
			if err := flapsClient.Wait(stopCtx, machine, api.MachineStateDestroyed, stopTimeout); err != nil {
				fmt.Fprintf(io.Out, " %s!\n", colorize.Red("failed"))
				terminal.Warnf("Failed to wait for ephemeral runner machine to be destroyed: %v\n", err)
				terminal.Warn("You may need to destroy it manually (`fly machine destroy`).")
			} else {
				fmt.Fprintf(io.Out, " %s.\n", colorize.Green("done"))
			}
		}()
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

func selectMachine(ctx context.Context, app *api.AppCompact, appConfig *appconfig.Config) (*api.Machine, bool, error) {
	if flag.GetBool(ctx, "select") {
		return promptForMachine(ctx, app, appConfig)
	} else if flag.IsSpecified(ctx, "machine") {
		return getMachineByID(ctx)
	} else {
		return makeEphemeralRunnerMachine(ctx, app, appConfig)
	}
}

func promptForMachine(ctx context.Context, app *api.AppCompact, appConfig *appconfig.Config) (*api.Machine, bool, error) {
	if flag.IsSpecified(ctx, "machine") {
		return nil, false, errors.New("-m/--machine can't be used with -s/--select")
	}

	flapsClient := flaps.FromContext(ctx)
	machines, err := flapsClient.ListActive(ctx)
	if err != nil {
		return nil, false, err
	}
	machines = lo.Filter(machines, func(machine *api.Machine, _ int) bool {
		return machine.State == api.MachineStateStarted && !machine.IsFlyAppsReleaseCommand()
	})
	if len(machines) == 0 {
		return nil, false, errors.New("no machines are available")
	}

	options := []string{"create an ephemeral shared-cpu-1x machine"}
	for _, machine := range machines {
		options = append(options, fmt.Sprintf("%s: %s %s %s", machine.Region, machine.ID, machine.PrivateIP, machine.Name))
	}

	index := 0
	if err := prompt.Select(ctx, &index, "Select a machine:", "", options...); err != nil {
		return nil, false, fmt.Errorf("failed to prompt for a machine: %w", err)
	}
	if index == 0 {
		return makeEphemeralRunnerMachine(ctx, app, appConfig)
	} else {
		return machines[index-1], false, nil
	}
}

func getMachineByID(ctx context.Context) (*api.Machine, bool, error) {
	flapsClient := flaps.FromContext(ctx)
	machineID := flag.GetString(ctx, "machine")
	machine, err := flapsClient.Get(ctx, machineID)
	if err != nil {
		return nil, false, err
	}
	if machine.State != api.MachineStateStarted {
		return nil, false, fmt.Errorf("machine %s is not started", machineID)
	}
	if machine.IsFlyAppsReleaseCommand() {
		return nil, false, fmt.Errorf("machine %s is a release command machine", machineID)
	}

	return machine, false, nil
}

func makeEphemeralRunnerMachine(ctx context.Context, app *api.AppCompact, appConfig *appconfig.Config) (*api.Machine, bool, error) {
	var (
		io          = iostreams.FromContext(ctx)
		colorize    = io.ColorScheme()
		apiClient   = client.FromContext(ctx).API()
		flapsClient = flaps.FromContext(ctx)
	)

	currentRelease, err := apiClient.GetAppCurrentReleaseMachines(ctx, app.Name)
	if err != nil {
		return nil, false, err
	}
	if currentRelease == nil {
		return nil, false, errors.New("can't create an ephemeral runner machine since the app has not yet been released")
	}

	machConfig, err := appConfig.ToEphemeralRunnerMachineConfig()
	if err != nil {
		return nil, false, fmt.Errorf("failed to generate ephemeral runner machine configuration: %w", err)
	}
	machConfig.Image = currentRelease.ImageRef
	machConfig.Guest = api.MachinePresets["shared-cpu-1x"] // TODO: infer size like with release commands?

	launchInput := api.LaunchMachineInput{
		AppID:   app.ID,
		OrgSlug: app.Organization.ID,
		Config:  machConfig,
	}
	machine, err := flapsClient.Launch(ctx, launchInput)
	if err != nil {
		return nil, false, fmt.Errorf("failed to launch ephemeral runner machine: %w", err)
	}
	fmt.Fprintf(io.Out, "Created an ephemeral machine %s to run the command.\n", colorize.Bold(machine.ID))

	const waitTimeout = 15 * time.Second
	fmt.Fprintf(io.Out, "Waiting for %s to start ...", colorize.Bold(machine.ID))
	err = flapsClient.Wait(ctx, machine, api.MachineStateStarted, waitTimeout)
	if err == nil {
		fmt.Fprintf(io.Out, " %s.\n", colorize.Green("done"))
		return machine, true, nil
	}

	fmt.Fprintf(io.Out, " %s!\n", colorize.Red("failed"))
	var flapsErr *flaps.FlapsError
	destroyed := false
	if errors.As(err, &flapsErr) && flapsErr.ResponseStatusCode == 404 {
		destroyed, err = checkMachineDestruction(ctx, machine, err)
	}

	if !destroyed {
		terminal.Warn("You may need to destroy the machine manually (`fly machine destroy`).")
	}
	return nil, false, err
}

func checkMachineDestruction(ctx context.Context, machine *api.Machine, firstErr error) (bool, error) {
	flapsClient := flaps.FromContext(ctx)
	machine, err := flapsClient.Get(ctx, machine.ID)
	if err != nil {
		return false, fmt.Errorf("failed to check status of machine: %w", err)
	}

	if machine.State != api.MachineStateDestroyed && machine.State != api.MachineStateDestroying {
		return false, firstErr
	}

	var exitEvent *api.MachineEvent
	for _, event := range machine.Events {
		if event.Type == "exit" {
			exitEvent = event
			break
		}
	}

	if exitEvent == nil || exitEvent.Request == nil {
		return true, errors.New("machine was destroyed unexpectedly")
	}

	exitCode, err := exitEvent.Request.GetExitCode()
	if err != nil {
		return true, errors.New("machine exited unexpectedly")
	}

	return true, fmt.Errorf("machine exited unexpectedly with code %v", exitCode)
}
