package cmd

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yewfence/volume-backup/internal/bridge"
)

func newRestoreCommand() *cobra.Command {
	opts := newBridgeOptions(true)
	restoreOpts := bridge.RestoreOptions{}
	cmd := &cobra.Command{
		Use:   "restore SOURCE_VOLUME",
		Short: "Prepare a target Docker volume for Kopia restore",
		Args:  restoreSourceVolumeArg(),
		RunE: func(cmd *cobra.Command, args []string) error {
			restoreOpts.SourceVolume = args[0]
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
	assumeYes := false
	cmd := &cobra.Command{
		Use:   "restore-cleanup",
		Short: "Remove restore helper containers for a session",
		Args:  noArgs("restore-cleanup"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !assumeYes {
				ok, err := confirmRestoreCleanup(cmd, sessionID)
				if err != nil {
					return err
				}
				if !ok {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "已取消")
					return nil
				}
			}
			return withDockerClient(cmd, opts, "restore-cleanup", func(ctx context.Context, api bridge.DockerAPI, logger bridge.Logger) error {
				return bridge.RestoreCleanup(ctx, api, sessionID, cmd.OutOrStdout(), logger)
			})
		},
	}
	addRuntimeFlags(cmd, &opts)
	cmd.Flags().StringVar(&sessionID, "session", sessionID, "要清理的恢复 session id")
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", assumeYes, "跳过确认并清理恢复 helper 容器")
	return cmd
}

func addRestoreFlags(cmd *cobra.Command, opts *bridgeOptions, restoreOpts *bridge.RestoreOptions) {
	addBridgeFlags(cmd, opts, true)
	flags := cmd.Flags()
	flags.StringVar(&opts.cfg.RestoreVisibleRoot, "restore-root", opts.cfg.RestoreVisibleRoot, "Kopia 容器内可见的恢复根路径")
	flags.StringVarP(&restoreOpts.TargetVolume, "target-volume", "t", restoreOpts.TargetVolume, "目标 Docker volume 名称，不存在时自动创建")
	flags.StringVar(&restoreOpts.SourceDirectoryID, "source-directory-id", restoreOpts.SourceDirectoryID, "可选的 Kopia source directory id，用于输出精确恢复命令")
	flags.StringVar(&restoreOpts.SnapshotTime, "snapshot-time", "latest", "用于输出路径恢复命令的 snapshot time")
	flags.StringVar(&restoreOpts.SessionID, "session", restoreOpts.SessionID, "可选的恢复 session id，留空时自动生成")
	flags.BoolVarP(&restoreOpts.AllowSourceTarget, "dangerously-allow-source-target", "s", restoreOpts.AllowSourceTarget, "允许源 volume 和目标 volume 相同")
	flags.BoolVarP(&restoreOpts.AllowNonEmptyTarget, "dangerously-allow-non-empty-target", "n", restoreOpts.AllowNonEmptyTarget, "允许复用非空目标 volume")
}

func restoreSourceVolumeArg() cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != 1 {
			return withHints(
				fmt.Errorf("restore 需要一个 source-volume 位置参数"),
				"示例 `volume-backup restore app-data`",
				"用 `docker volume ls` 查看当前 Docker volume 名称",
			)
		}
		return nil
	}
}

func confirmRestoreCleanup(cmd *cobra.Command, sessionID string) (bool, error) {
	scope := "所有恢复 helper 容器"
	if strings.TrimSpace(sessionID) != "" {
		scope = fmt.Sprintf("session %s 的恢复 helper 容器", sessionID)
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "将清理%s，目标 volume 不会被删除，继续请输入 y 或 yes 并回车 ", scope); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return false, fmt.Errorf("读取确认输入失败: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
