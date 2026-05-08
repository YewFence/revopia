package cmd

import (
	"context"

	"github.com/spf13/cobra"
)

func newInspectCommand() *cobra.Command {
	opts := newBridgeOptions(false)
	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "Inspect volumes and helper containers",
		Args:  noArgs("inspect"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withDockerClient(cmd, opts, "inspect", func(ctx context.Context, api dockerAPI, logger eventLogger) error {
				return inspectState(ctx, api, opts.cfg, cmd.OutOrStdout(), logger)
			})
		},
	}
	addBridgeFlags(cmd, &opts, false)
	return cmd
}
