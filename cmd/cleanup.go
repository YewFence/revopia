package cmd

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/yewfence/volume-backup/internal/bridge"
)

func newCleanupCommand() *cobra.Command {
	opts := newBridgeOptions(false)
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove managed backup helper containers",
		Args:  noArgs("cleanup"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withDockerClient(cmd, opts, "cleanup", func(ctx context.Context, api bridge.DockerAPI, logger bridge.Logger) error {
				return bridge.Cleanup(ctx, api, cmd.OutOrStdout(), logger)
			})
		},
	}
	addRuntimeFlags(cmd, &opts)
	return cmd
}
