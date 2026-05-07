package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	dockerclient "github.com/moby/moby/client"
)

const defaultTimeout = 30 * time.Second
const defaultVerifyTimeout = 5 * time.Second

func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCLI(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stderr)
		return fmt.Errorf("缺少命令")
	}

	switch args[0] {
	case "prepare":
		return runPrepareCommand(args[1:], stdout, stderr)
	case "cleanup":
		return runCleanupCommand(args[1:], stdout, stderr)
	case "inspect":
		return runInspectCommand(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("未知命令 %q", args[0])
	}
}

func runPrepareCommand(args []string, stdout, stderr io.Writer) error {
	cfg := defaultBridgeConfig()
	timeout := defaultTimeout
	verifyTimeout := defaultVerifyTimeout
	logFile := defaultLogFile()

	fs := flag.NewFlagSet("prepare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.BridgeSource, "bridge-source", cfg.BridgeSource, "Docker daemon 侧的宿主机 bridge bind mount 路径")
	fs.StringVar(&cfg.VisibleRoot, "visible-root", cfg.VisibleRoot, "Kopia 容器内可见的 volume 根路径")
	fs.StringVar(&cfg.HelperImage, "helper-image", cfg.HelperImage, "helper 容器镜像")
	fs.StringVar(&logFile, "log-file", logFile, "持久日志文件路径，留空则禁用文件日志")
	fs.DurationVar(&timeout, "timeout", timeout, "Docker API 调用超时时间")
	fs.DurationVar(&verifyTimeout, "verify-timeout", verifyTimeout, "等待挂载传播到可见路径的时间")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("prepare 不接受额外参数")
	}
	cfg.VerifyTimeout = verifyTimeout

	logCloser, logger, err := openCommandLog(logFile)
	if err != nil {
		return err
	}
	defer logCloser.Close()
	logger.Printf("command=prepare bridge_source=%q visible_root=%q helper_image=%q timeout=%s verify_timeout=%s", cfg.BridgeSource, cfg.VisibleRoot, cfg.HelperImage, timeout, verifyTimeout)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	apiClient, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		logger.Printf("docker_client error=%q", err)
		return fmt.Errorf("创建 Docker 客户端失败: %w", err)
	}
	defer apiClient.Close()

	return prepare(ctx, apiClient, cfg, stdout, logger)
}

func runCleanupCommand(args []string, stdout, stderr io.Writer) error {
	timeout := defaultTimeout
	logFile := defaultLogFile()

	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&logFile, "log-file", logFile, "持久日志文件路径，留空则禁用文件日志")
	fs.DurationVar(&timeout, "timeout", timeout, "Docker API 调用超时时间")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("cleanup 不接受额外参数")
	}

	logCloser, logger, err := openCommandLog(logFile)
	if err != nil {
		return err
	}
	defer logCloser.Close()
	logger.Printf("command=cleanup timeout=%s", timeout)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	apiClient, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		logger.Printf("docker_client error=%q", err)
		return fmt.Errorf("创建 Docker 客户端失败: %w", err)
	}
	defer apiClient.Close()

	return cleanup(ctx, apiClient, stdout, logger)
}

func runInspectCommand(args []string, stdout, stderr io.Writer) error {
	cfg := defaultBridgeConfig()
	timeout := defaultTimeout
	logFile := defaultLogFile()

	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.BridgeSource, "bridge-source", cfg.BridgeSource, "Docker daemon 侧的宿主机 bridge bind mount 路径")
	fs.StringVar(&cfg.VisibleRoot, "visible-root", cfg.VisibleRoot, "Kopia 容器内可见的 volume 根路径")
	fs.StringVar(&cfg.HelperImage, "helper-image", cfg.HelperImage, "helper 容器镜像")
	fs.StringVar(&logFile, "log-file", logFile, "持久日志文件路径，留空则禁用文件日志")
	fs.DurationVar(&timeout, "timeout", timeout, "Docker API 调用超时时间")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("inspect 不接受额外参数")
	}

	logCloser, logger, err := openCommandLog(logFile)
	if err != nil {
		return err
	}
	defer logCloser.Close()
	logger.Printf("command=inspect bridge_source=%q visible_root=%q helper_image=%q timeout=%s", cfg.BridgeSource, cfg.VisibleRoot, cfg.HelperImage, timeout)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	apiClient, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		logger.Printf("docker_client error=%q", err)
		return fmt.Errorf("创建 Docker 客户端失败: %w", err)
	}
	defer apiClient.Close()

	return inspectState(ctx, apiClient, cfg, stdout, logger)
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "用法")
	fmt.Fprintln(w, "  volume-backup prepare [--bridge-source /mnt/volumes-backup] [--visible-root /volumes] [--helper-image alpine]")
	fmt.Fprintln(w, "  volume-backup cleanup")
	fmt.Fprintln(w, "  volume-backup inspect [--visible-root /volumes]")
}

func defaultLogFile() string {
	if value := strings.TrimSpace(os.Getenv("KOPIA_VOLUME_BRIDGE_LOG_FILE")); value != "" {
		return value
	}
	return "/app/logs/volume-bridge.log"
}

func openCommandLog(path string) (io.Closer, eventLogger, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nopCloser{}, eventLogger{out: io.Discard}, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, eventLogger{}, fmt.Errorf("创建日志目录失败 %q: %w", filepath.Dir(path), err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, eventLogger{}, fmt.Errorf("打开日志文件失败 %q: %w", path, err)
	}
	return file, eventLogger{out: file}, nil
}

type nopCloser struct{}

func (nopCloser) Close() error {
	return nil
}
