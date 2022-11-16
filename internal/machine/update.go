package machine

import (
	"context"
	"fmt"
	"time"

	"github.com/superfly/flyctl/api"
	"github.com/superfly/flyctl/flaps"
	"github.com/superfly/flyctl/internal/watch"
	"github.com/superfly/flyctl/iostreams"
)

func RollingUpdate(ctx context.Context, input *api.LaunchMachineInput) error {
	var (
		flapsClient = flaps.FromContext(ctx)
	)

	machines, err := AcquireLeases(ctx)
	if err != nil {
		return err
	}
	// Defer lease release
	for _, m := range machines {
		defer flapsClient.ReleaseLease(ctx, m.ID, m.LeaseNonce)
	}

	// Verify all target machines are running the same image.
	// TODO - Once all scoped machines have the proper metadata tags, we can remove this.

	// Prompt user with diff confirmation

	for _, m := range machines {
		Update(ctx, m, input)
	}

	return nil
}

func Update(ctx context.Context, m *api.Machine, input *api.LaunchMachineInput) error {
	var (
		flapsClient = flaps.FromContext(ctx)
		io          = iostreams.FromContext(ctx)
		colorize    = io.ColorScheme()
	)

	fmt.Fprintf(io.Out, "Updating machine %s\n", colorize.Bold(m.ID))

	input.ID = m.ID
	if _, err := flapsClient.Update(ctx, *input, m.LeaseNonce); err != nil {
		return fmt.Errorf("could not stop machine %s: %w", input.ID, err)
	}

	waitForAction := "start"
	if m.Config.Schedule != "" {
		waitForAction = "stop"
	}

	if err := WaitForStartOrStop(ctx, &api.Machine{ID: input.ID}, waitForAction, time.Minute*5); err != nil {
		return err
	}

	if err := watch.MachinesChecks(ctx, []*api.Machine{m}); err != nil {
		return fmt.Errorf("failed to wait for health checks to pass: %w", err)
	}

	fmt.Fprintf(io.Out, "Machine %s updated successfully!\n", colorize.Bold(m.ID))

	return nil
}
