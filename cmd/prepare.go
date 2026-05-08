package cmd

import (
	"context"

	"github.com/spf13/cobra"
)

func newPrepareCommand() *cobra.Command {
	opts := newBridgeOptions(true)
	cmd := &cobra.Command{
		Use:   "prepare",
		Short: "Prepare labeled Docker volumes for Kopia",
		Args:  noArgs("prepare"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.cfg.VerifyTimeout = opts.verifyTimeout
			return withDockerClient(cmd, opts, "prepare", func(ctx context.Context, api dockerAPI, logger eventLogger) error {
				return prepare(ctx, api, opts.cfg, cmd.OutOrStdout(), logger)
			})
		},
	}
	addBridgeFlags(cmd, &opts, true)
	return cmd
}
