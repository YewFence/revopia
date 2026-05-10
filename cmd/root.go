package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	dockerclient "github.com/moby/moby/client"
	"github.com/spf13/cobra"
	"github.com/yewfence/revopia/internal/bridge"
)

const (
	defaultTimeout = 30 * time.Second
)

var appVersion = "dev"

type bridgeOptions struct {
	cfg           bridge.Config
	timeout       time.Duration
	verifyTimeout time.Duration
	logFile       string
}

var rootCmd = newRootCommand()

func newRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "revopia",
		Short:         "Kopia Docker volume bridge",
		Long:          "Kopia Docker volume bridge exposes labeled Docker volumes through a propagation bridge for Kopia.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Help()
			return fmt.Errorf("缺少命令")
		},
	}

	cmd.AddCommand(
		newPrepareCommand(),
		newCleanupCommand(),
		newRestoreCommand(),
		newRestoreCleanupCommand(),
		newInspectCommand(),
		newVersionCommand(),
	)
	return cmd
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		writeErrorWithHints(os.Stderr, err)
		os.Exit(1)
	}
}

func SetVersion(version string) {
	appVersion = version
	bridge.SetVersion(version)
}

func addBridgeFlags(cmd *cobra.Command, opts *bridgeOptions, includeVerify bool) {
	flags := cmd.Flags()
	flags.StringVar(&opts.cfg.BridgeSource, "bridge-source", opts.cfg.BridgeSource, "Docker daemon 侧的宿主机 bridge bind mount 路径")
	flags.StringVar(&opts.cfg.VisibleRoot, "visible-root", opts.cfg.VisibleRoot, "当前 Kopia 进程可见的 volume 根路径")
	flags.StringVar(&opts.cfg.HelperImage, "helper-image", opts.cfg.HelperImage, "helper 容器镜像")
	flags.StringVar(&opts.logFile, "log-file", opts.logFile, "持久日志文件路径，留空则禁用文件日志")
	flags.DurationVar(&opts.timeout, "timeout", opts.timeout, "Docker API 调用超时时间")
	if includeVerify {
		flags.DurationVar(&opts.verifyTimeout, "verify-timeout", opts.verifyTimeout, "等待挂载传播到可见路径的时间")
	}
}

func noArgs(commandName string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != 0 {
			return fmt.Errorf("%s 不接受额外参数", commandName)
		}
		return nil
	}
}

func withDockerClient(cmd *cobra.Command, opts bridgeOptions, commandName string, run func(context.Context, bridge.DockerAPI, bridge.Logger) error) error {
	logCloser, logger, err := openCommandLog(opts.logFile)
	if err != nil {
		return err
	}
	defer func() {
		_ = logCloser.Close()
	}()

	logger.Printf("command=%s bridge_source=%q visible_root=%q restore_root=%q helper_image=%q timeout=%s verify_timeout=%s", commandName, opts.cfg.BridgeSource, opts.cfg.VisibleRoot, opts.cfg.RestoreVisibleRoot, opts.cfg.HelperImage, opts.timeout, opts.verifyTimeout)

	ctx, cancel := context.WithTimeout(cmd.Context(), opts.timeout)
	defer cancel()

	apiClient, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		logger.Printf("docker_client error=%q", err)
		return fmt.Errorf("创建 Docker 客户端失败: %w", err)
	}
	defer func() {
		_ = apiClient.Close()
	}()

	return run(ctx, apiClient, logger)
}

func defaultLogFile() string {
	if value := strings.TrimSpace(os.Getenv("REVOPIA_LOG_FILE")); value != "" {
		return value
	}
	if bridge.RunningInContainer() {
		return "/app/logs/revopia.log"
	}
	return "/var/log/revopia/revopia.log"
}

func newBridgeOptions(includeVerify bool) bridgeOptions {
	cfg := bridge.DefaultConfig()
	opts := bridgeOptions{
		cfg:           cfg,
		timeout:       defaultTimeout,
		verifyTimeout: cfg.VerifyTimeout,
		logFile:       defaultLogFile(),
	}
	if !includeVerify {
		opts.verifyTimeout = 0
	}
	return opts
}

func openCommandLog(path string) (io.Closer, bridge.Logger, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nopCloser{}, bridge.NewLogger(io.Discard), nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, bridge.Logger{}, fmt.Errorf("创建日志目录失败 %q: %w", filepath.Dir(path), err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, bridge.Logger{}, fmt.Errorf("打开日志文件失败 %q: %w", path, err)
	}
	return file, bridge.NewLogger(file), nil
}

type nopCloser struct{}

func (nopCloser) Close() error {
	return nil
}
