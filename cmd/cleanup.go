package cmd

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/yewfence/revopia/internal/bridge"
)

func newCleanupCommand() *cobra.Command {
	opts := newBridgeOptions(false)
	cleanupOpts := bridge.CleanupOptions{}
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove managed backup helper containers and propagated mounts",
		Args:  noArgs("cleanup"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withDockerClient(cmd, opts, "cleanup", func(ctx context.Context, api bridge.DockerAPI, logger bridge.Logger) error {
				return bridge.Cleanup(ctx, api, opts.cfg, cleanupOpts, cmd.OutOrStdout(), logger)
			})
		},
	}
	addCleanupFlags(cmd, &opts, &cleanupOpts)
	return cmd
}

func addCleanupFlags(cmd *cobra.Command, opts *bridgeOptions, cleanupOpts *bridge.CleanupOptions) {
	flags := cmd.Flags()
	flags.StringVar(&opts.cfg.BridgeSource, "bridge-source", opts.cfg.BridgeSource, "Docker daemon 侧的宿主机 bridge bind mount 路径，用于临时容器回收传播挂载")
	flags.StringVar(&opts.cfg.VisibleRoot, "visible-root", opts.cfg.VisibleRoot, "当前进程可见的 volume 根路径，用于回收传播挂载")
	flags.StringVar(&opts.cfg.HelperImage, "helper-image", opts.cfg.HelperImage, "cleanup 容器镜像")
	flags.StringVar(&opts.logFile, "log-file", opts.logFile, "持久日志文件路径，留空则禁用文件日志")
	flags.DurationVar(&opts.timeout, "timeout", opts.timeout, "Docker API 调用超时时间")
	flags.BoolVar(&cleanupOpts.LazyUnmount, "dangerously-lazy-umount", cleanupOpts.LazyUnmount, "普通 umount 失败后允许显式使用 lazy umount")
}
