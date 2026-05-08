package cmd

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/yewfence/volume-backup/internal/bridge"
)

func newRestoreCommand() *cobra.Command {
	opts := newBridgeOptions(true)
	restoreOpts := bridge.RestoreOptions{}
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Prepare a target Docker volume for Kopia restore",
		Args:  noArgs("restore"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.cfg.VerifyTimeout = opts.verifyTimeout
			return withDockerClient(cmd, opts, "restore", func(ctx context.Context, api bridge.DockerAPI, logger bridge.Logger) error {
				return bridge.Restore(ctx, api, opts.cfg, restoreOpts, cmd.OutOrStdout(), logger)
			})
		},
	}
	addRestoreFlags(cmd, &opts, &restoreOpts)
	return cmd
}

func newRestoreCleanupCommand() *cobra.Command {
	opts := newBridgeOptions(false)
	sessionID := ""
	cmd := &cobra.Command{
		Use:   "restore-cleanup",
		Short: "Remove restore helper containers for a session",
		Args:  noArgs("restore-cleanup"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withDockerClient(cmd, opts, "restore-cleanup", func(ctx context.Context, api bridge.DockerAPI, logger bridge.Logger) error {
				return bridge.RestoreCleanup(ctx, api, sessionID, cmd.OutOrStdout(), logger)
			})
		},
	}
	addRuntimeFlags(cmd, &opts)
	cmd.Flags().StringVar(&sessionID, "session", sessionID, "要清理的恢复 session id")
	return cmd
}

func addRestoreFlags(cmd *cobra.Command, opts *bridgeOptions, restoreOpts *bridge.RestoreOptions) {
	addBridgeFlags(cmd, opts, true)
	flags := cmd.Flags()
	flags.StringVar(&opts.cfg.RestoreVisibleRoot, "restore-root", opts.cfg.RestoreVisibleRoot, "Kopia 容器内可见的恢复根路径")
	flags.StringVar(&restoreOpts.SourceVolume, "source-volume", restoreOpts.SourceVolume, "源 Docker volume 名称")
	flags.StringVar(&restoreOpts.TargetVolume, "target-volume", restoreOpts.TargetVolume, "目标 Docker volume 名称，不存在时自动创建")
	flags.StringVar(&restoreOpts.SourceDirectoryID, "source-directory-id", restoreOpts.SourceDirectoryID, "可选的 Kopia source directory id，用于输出精确恢复命令")
	flags.StringVar(&restoreOpts.SnapshotTime, "snapshot-time", "latest", "用于输出路径恢复命令的 snapshot time")
	flags.StringVar(&restoreOpts.SessionID, "session", restoreOpts.SessionID, "可选的恢复 session id，留空时自动生成")
	flags.BoolVar(&restoreOpts.AllowSourceTarget, "dangerously-allow-source-target", restoreOpts.AllowSourceTarget, "允许源 volume 和目标 volume 相同")
	flags.BoolVar(&restoreOpts.AllowNonEmptyTarget, "dangerously-allow-non-empty-target", restoreOpts.AllowNonEmptyTarget, "允许复用非空目标 volume")
}
