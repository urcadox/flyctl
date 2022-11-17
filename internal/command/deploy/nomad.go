package deploy

import (
	"context"
	"fmt"
	"strings"

	"github.com/superfly/flyctl/api"
	"github.com/superfly/flyctl/client"
	"github.com/superfly/flyctl/internal/app"
	"github.com/superfly/flyctl/internal/build/imgsrc"
	"github.com/superfly/flyctl/internal/flag"
	"github.com/superfly/flyctl/internal/logger"
	"github.com/superfly/flyctl/internal/render"
	"github.com/superfly/flyctl/internal/watch"
)

func DeployNomadWithConfig(ctx context.Context, appConfig *app.Config) (err error) {
	apiClient := client.FromContext(ctx).API()

	// Fetch an image ref or build from source to get the final image reference to deploy
	img, err := determineImage(ctx, appConfig)
	if err != nil {
		return fmt.Errorf("failed to fetch an image or build from source: %w", err)
	}

	// Assign an empty map if nil so later assignments won't fail
	if appConfig.Env == nil {
		appConfig.Env = map[string]string{}
	}

	if flag.GetBuildOnly(ctx) {
		return nil
	}

	var release *api.Release
	var releaseCommand *api.ReleaseCommand

	if appConfig.PrimaryRegion != "" && appConfig.Env["PRIMARY_REGION"] == "" {
		appConfig.Env["PRIMARY_REGION"] = appConfig.PrimaryRegion
	}

	release, releaseCommand, err = createRelease(ctx, appConfig, img)
	if err != nil {
		return err
	}

	if flag.GetDetach(ctx) {
		return nil
	}

	// TODO: This is a single message that doesn't belong to any block output, so we should have helpers to allow that
	tb := render.NewTextBlock(ctx)
	tb.Done("You can detach the terminal anytime without stopping the deployment")

	// Run the pre-deployment release command if it's set
	if releaseCommand != nil {
		// TODO: don't use text block here
		tb := render.NewTextBlock(ctx, fmt.Sprintf("Release command detected: %s\n", releaseCommand.Command))
		tb.Done("This release will not be available until the release command succeeds.")

		if err := watch.ReleaseCommand(ctx, appConfig.AppName, releaseCommand.ID); err != nil {
			return err
		}

		release, err = apiClient.GetAppRelease(ctx, appConfig.AppName, release.ID)
		if err != nil {
			return err
		}
	}

	if release.DeploymentStrategy == "IMMEDIATE" {
		logger := logger.FromContext(ctx)
		logger.Debug("immediate deployment strategy, nothing to monitor")

		return nil
	}

	err = watch.Deployment(ctx, appConfig.AppName, release.EvaluationID)

	return err
}

func createRelease(ctx context.Context, appConfig *app.Config, img *imgsrc.DeploymentImage) (*api.Release, *api.ReleaseCommand, error) {
	tb := render.NewTextBlock(ctx, "Creating release")

	input := api.DeployImageInput{
		AppID: appConfig.AppName,
		Image: img.Tag,
	}

	// Set the deployment strategy
	if val := flag.GetString(ctx, "strategy"); val != "" {
		input.Strategy = api.StringPointer(strings.ReplaceAll(strings.ToUpper(val), "-", "_"))
	}

	if len(appConfig.Definition) > 0 {
		input.Definition = api.DefinitionPtr(appConfig.Definition)
	}

	// Start deployment of the determined image
	client := client.FromContext(ctx).API()

	release, releaseCommand, err := client.DeployImage(ctx, input)
	if err == nil {
		tb.Donef("release v%d created\n", release.Version)
	}

	return release, releaseCommand, err
}
