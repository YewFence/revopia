package cmd

import (
	"context"

	"github.com/spf13/cobra"
)

func newCleanupCommand() *cobra.Command {
	opts := newBridgeOptions(false)
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove managed backup helper containers",
		Args:  noArgs("cleanup"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withDockerClient(cmd, opts, "cleanup", func(ctx context.Context, api dockerAPI, logger eventLogger) error {
				return cleanup(ctx, api, cmd.OutOrStdout(), logger)
			})
		},
	}
	addRuntimeFlags(cmd, &opts)
	return cmd
}
