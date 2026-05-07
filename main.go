package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	dockerclient "github.com/moby/moby/client"
)

const defaultTimeout = 30 * time.Second

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

	fs := flag.NewFlagSet("prepare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.BridgeSource, "bridge-source", cfg.BridgeSource, "Docker daemon 侧的宿主机 bridge bind mount 路径")
	fs.StringVar(&cfg.HelperImage, "helper-image", cfg.HelperImage, "helper 容器镜像")
	fs.DurationVar(&timeout, "timeout", timeout, "Docker API 调用超时时间")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("prepare 不接受额外参数")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	apiClient, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		return fmt.Errorf("创建 Docker 客户端失败: %w", err)
	}
	defer apiClient.Close()

	return prepare(ctx, apiClient, cfg, stdout)
}

func runCleanupCommand(args []string, stdout, stderr io.Writer) error {
	timeout := defaultTimeout

	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.DurationVar(&timeout, "timeout", timeout, "Docker API 调用超时时间")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("cleanup 不接受额外参数")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	apiClient, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		return fmt.Errorf("创建 Docker 客户端失败: %w", err)
	}
	defer apiClient.Close()

	return cleanup(ctx, apiClient, stdout)
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "用法")
	fmt.Fprintln(w, "  volume-backup prepare [--bridge-source /mnt/volumes-backup] [--helper-image alpine]")
	fmt.Fprintln(w, "  volume-backup cleanup")
}
