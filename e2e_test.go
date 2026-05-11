//go:build e2e

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockerclient "github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	e2eKopiaImage   = "kopia/kopia:unstable"
	e2eHelperImage  = "alpine:latest"
	e2eBeforeAction = "revopia prepare"
	e2eAfterAction  = "revopia cleanup"
	e2eSeedReadme   = "cache volume ready\n"
)

func TestRevopiaE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	docker, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		t.Fatalf("创建 Docker 客户端失败: %v", err)
	}
	t.Cleanup(func() {
		_ = docker.Close()
	})
	if _, err := docker.Ping(ctx, dockerclient.PingOptions{NegotiateAPIVersion: true}); err != nil {
		t.Skipf("Docker daemon 不可用，跳过 e2e 测试: %v", err)
	}

	project := "revopia-e2e-" + randomHex(t, 6)
	bridgeSource := filepath.Join(os.TempDir(), project, "bridge")
	logDir := filepath.Join(os.TempDir(), project, "logs")
	binPath := filepath.Join(os.TempDir(), project, "revopia")
	mustMkdir(t, bridgeSource)
	mustMkdir(t, logDir)
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Join(os.TempDir(), project))
	})

	buildRevopia(t, ctx, binPath)

	cacheVolume := project + "-test-cache-data"
	kopiaConfigVolume := project + "-kopia-config"
	kopiaCacheVolume := project + "-kopia-cache"
	kopiaDataVolume := project + "-kopia-data"
	friendlyVolumeName := "cache-tmp-" + project
	for _, volumeName := range []string{cacheVolume, kopiaConfigVolume, kopiaCacheVolume, kopiaDataVolume} {
		removeDockerVolume(t, ctx, docker, volumeName)
	}
	t.Cleanup(func() {
		removeProjectBridgeHelpers(t, context.Background(), docker, cacheVolume)
		for _, volumeName := range []string{cacheVolume, kopiaConfigVolume, kopiaCacheVolume, kopiaDataVolume} {
			cleanupDockerVolume(t, context.Background(), docker, volumeName)
		}
	})

	createDockerVolume(t, ctx, docker, cacheVolume, map[string]string{
		"backup.enable": "true",
		"backup.name":   friendlyVolumeName,
	})
	createDockerVolume(t, ctx, docker, kopiaConfigVolume, nil)
	createDockerVolume(t, ctx, docker, kopiaCacheVolume, nil)
	createDockerVolume(t, ctx, docker, kopiaDataVolume, nil)
	seedDockerVolume(t, ctx, cacheVolume)

	kopia, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Started: true,
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        e2eKopiaImage,
			Hostname:     "kopia-e2e",
			ExposedPorts: []string{"51515/tcp"},
			Env: map[string]string{
				"KOPIA_PASSWORD":        "kopia",
				"USER":                  "kopia",
				"REVOPIA_LOG_FILE":      "/app/logs/revopia.log",
				"REVOPIA_BRIDGE_SOURCE": bridgeSource,
				"REVOPIA_VISIBLE_ROOT":  "/volumes",
				"REVOPIA_HELPER_IMAGE":  e2eHelperImage,
			},
			Cmd: []string{
				"server",
				"start",
				"--disable-csrf-token-checks",
				"--enable-actions",
				"--insecure",
				"--address=0.0.0.0:51515",
				"--server-username=kopia",
				"--server-password=kopia",
			},
			WaitingFor: wait.ForListeningPort("51515/tcp").WithStartupTimeout(90 * time.Second),
			HostConfigModifier: func(hostConfig *container.HostConfig) {
				hostConfig.Mounts = append(hostConfig.Mounts,
					mount.Mount{Type: mount.TypeVolume, Source: kopiaConfigVolume, Target: "/app/config"},
					mount.Mount{Type: mount.TypeVolume, Source: kopiaCacheVolume, Target: "/app/cache"},
					mount.Mount{Type: mount.TypeVolume, Source: kopiaDataVolume, Target: "/app/data"},
					mount.Mount{Type: mount.TypeBind, Source: logDir, Target: "/app/logs"},
					mount.Mount{Type: mount.TypeBind, Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
					mount.Mount{Type: mount.TypeBind, Source: binPath, Target: "/bin/revopia", ReadOnly: true},
					mount.Mount{
						Type:   mount.TypeBind,
						Source: bridgeSource,
						Target: "/volumes",
						BindOptions: &mount.BindOptions{
							Propagation: mount.PropagationRShared,
						},
					},
				)
			},
		},
	})
	testcontainers.CleanupContainer(t, kopia)
	if err != nil {
		t.Fatalf("启动 Kopia 容器失败: %v", err)
	}
	t.Cleanup(func() {
		runFinalRevopiaCleanup(t, kopia)
	})

	kopiaExec(t, ctx, kopia, "repository", "create", "filesystem", "--path=/app/data", "--enable-actions")
	kopiaExec(t, ctx, kopia, "repository", "connect", "filesystem", "--path=/app/data", "--enable-actions")
	kopiaExec(
		t,
		ctx,
		kopia,
		"policy",
		"set",
		"/volumes",
		// 这里直接走 argv，不经过 shell，所以不要把引号字符传进去。
		// 这个字符串本身就等价于 shell 中的
		// --before-snapshot-root-action="revopia prepare"
		"--before-snapshot-root-action="+e2eBeforeAction,
		"--after-snapshot-root-action="+e2eAfterAction,
		"--one-file-system=false",
	)
	policy := kopiaExec(t, ctx, kopia, "policy", "show", "/volumes")
	assertPolicyActions(t, policy, e2eBeforeAction, e2eAfterAction)

	fullSnapshotCreate := kopiaExec(t, ctx, kopia, "snapshot", "create", "/volumes")
	containerExec(t, ctx, kopia, "mkdir", "-p", path.Join("/volumes", friendlyVolumeName))
	singleSnapshotCreate := kopiaExec(t, ctx, kopia, "snapshot", "create", path.Join("/volumes", friendlyVolumeName))

	fullSnapshotList := kopiaExec(t, ctx, kopia, "snapshot", "list", "/volumes")
	if !strings.Contains(fullSnapshotList, "/volumes") {
		t.Fatalf("全量快照列表没有包含 /volumes\n%s", fullSnapshotList)
	}
	singleSnapshotList := kopiaExec(t, ctx, kopia, "snapshot", "list", path.Join("/volumes", friendlyVolumeName))
	if !strings.Contains(singleSnapshotList, path.Join("/volumes", friendlyVolumeName)) {
		t.Fatalf("单卷快照列表没有包含 %s\n%s", path.Join("/volumes", friendlyVolumeName), singleSnapshotList)
	}
	debug := e2eDebug{
		Policy:               policy,
		FullSnapshotCreate:   fullSnapshotCreate,
		SingleSnapshotCreate: singleSnapshotCreate,
		BridgeLog:            readOptionalContainerFile(t, ctx, kopia, "/app/logs/revopia.log"),
	}
	assertRestoredFile(t, ctx, kopia, path.Join("/volumes", friendlyVolumeName), "/tmp/restore-single-"+project, "README.txt", e2eSeedReadme, singleSnapshotList, debug)
	assertRestoredFile(t, ctx, kopia, "/volumes", "/tmp/restore-full-"+project, path.Join(friendlyVolumeName, "README.txt"), e2eSeedReadme, fullSnapshotList, debug)
	assertNoBridgeHelpers(t, ctx, docker)
}

func TestRevopiaHostE2E(t *testing.T) {
	if _, err := exec.LookPath("kopia"); err != nil {
		t.Skipf("宿主机 kopia 命令不可用，跳过 host e2e 测试: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	docker, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		t.Fatalf("创建 Docker 客户端失败: %v", err)
	}
	t.Cleanup(func() {
		_ = docker.Close()
	})
	if _, err := docker.Ping(ctx, dockerclient.PingOptions{NegotiateAPIVersion: true}); err != nil {
		t.Skipf("Docker daemon 不可用，跳过 host e2e 测试: %v", err)
	}

	project := "revopia-host-e2e-" + randomHex(t, 6)
	projectRoot := filepath.Join(e2eTempDir(t), project)
	bridgeSource := filepath.Join(projectRoot, "bridge")
	repositoryPath := filepath.Join(projectRoot, "repository")
	configPath := filepath.Join(projectRoot, "repository.config")
	logPath := filepath.Join(projectRoot, "revopia.log")
	binPath := filepath.Join(projectRoot, "bin", "revopia")
	mustMkdir(t, bridgeSource)
	mustMkdir(t, filepath.Join(bridgeSource, "restore"))
	mustMkdir(t, repositoryPath)
	mustMkdir(t, filepath.Dir(binPath))
	t.Cleanup(func() {
		_ = os.RemoveAll(projectRoot)
	})

	buildRevopia(t, ctx, binPath)
	env := hostE2EEnv(binPath, bridgeSource, logPath)

	cacheVolume := project + "-test-cache-data"
	friendlyVolumeName := "cache-tmp-" + project
	removeDockerVolume(t, ctx, docker, cacheVolume)
	t.Cleanup(func() {
		runFinalHostRevopiaCleanup(t, env, binPath)
		removeProjectBridgeHelpers(t, context.Background(), docker, cacheVolume)
		cleanupDockerVolume(t, context.Background(), docker, cacheVolume)
	})

	createDockerVolume(t, ctx, docker, cacheVolume, map[string]string{
		"backup.enable": "true",
		"backup.name":   friendlyVolumeName,
	})
	seedDockerVolume(t, ctx, cacheVolume)

	hostKopiaExec(t, ctx, env, configPath, "repository", "create", "filesystem", "--path="+repositoryPath, "--enable-actions")
	hostKopiaExec(
		t,
		ctx,
		env,
		configPath,
		"policy",
		"set",
		bridgeSource,
		"--before-snapshot-root-action="+binPath+" prepare",
		"--after-snapshot-root-action="+binPath+" cleanup",
		"--one-file-system=false",
	)
	policy := hostKopiaExec(t, ctx, env, configPath, "policy", "show", bridgeSource)
	assertPolicyActions(t, policy, binPath+" prepare", binPath+" cleanup")

	fullSnapshotCreate := hostKopiaExec(t, ctx, env, configPath, "snapshot", "create", bridgeSource)
	singleSourcePath := filepath.Join(bridgeSource, friendlyVolumeName)
	mustMkdir(t, singleSourcePath)
	singleSnapshotCreate := hostKopiaExec(t, ctx, env, configPath, "snapshot", "create", singleSourcePath)

	fullSnapshotList := hostKopiaExec(t, ctx, env, configPath, "snapshot", "list", bridgeSource)
	if !strings.Contains(fullSnapshotList, bridgeSource) {
		t.Fatalf("宿主机全量快照列表没有包含 %s\n%s", bridgeSource, fullSnapshotList)
	}
	singleSnapshotList := hostKopiaExec(t, ctx, env, configPath, "snapshot", "list", singleSourcePath)
	if !strings.Contains(singleSnapshotList, singleSourcePath) {
		t.Fatalf("宿主机单卷快照列表没有包含 %s\n%s", singleSourcePath, singleSnapshotList)
	}

	debug := e2eDebug{
		Policy:               policy,
		FullSnapshotCreate:   fullSnapshotCreate,
		SingleSnapshotCreate: singleSnapshotCreate,
		BridgeLog:            readOptionalHostFile(logPath),
	}
	assertRestoredHostFile(t, ctx, env, configPath, singleSourcePath, filepath.Join(projectRoot, "restore-single"), "README.txt", e2eSeedReadme, singleSnapshotList, debug)
	assertRestoredHostFile(t, ctx, env, configPath, bridgeSource, filepath.Join(projectRoot, "restore-full"), filepath.Join(friendlyVolumeName, "README.txt"), e2eSeedReadme, fullSnapshotList, debug)
	assertNoProjectBridgeHelpers(t, ctx, docker, cacheVolume)
}

type e2eDebug struct {
	Policy               string
	FullSnapshotCreate   string
	SingleSnapshotCreate string
	BridgeLog            string
}

func (d e2eDebug) String() string {
	return "policy show:\n" + d.Policy +
		"\nfull snapshot create:\n" + d.FullSnapshotCreate +
		"\nsingle snapshot create:\n" + d.SingleSnapshotCreate +
		"\nbridge log:\n" + d.BridgeLog
}

func assertPolicyActions(t *testing.T, policy string, beforeAction string, afterAction string) {
	t.Helper()

	for _, want := range []string{
		"Run command before snapshot root:",
		"Run command after snapshot root:",
		beforeAction,
		afterAction,
	} {
		if !strings.Contains(policy, want) {
			t.Fatalf("Kopia policy action 设置不正确，缺少 %q\n%s", want, policy)
		}
	}
}

func runFinalRevopiaCleanup(t *testing.T, ctr testcontainers.Container) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	exitCode, output, err := containerExecResultNoFatal(ctx, ctr, "revopia", "cleanup")
	if err != nil {
		t.Logf("最终 revopia cleanup 执行失败: %v", err)
		return
	}
	if exitCode != 0 {
		t.Logf("最终 revopia cleanup 退出码为 %d\n%s", exitCode, output)
		return
	}
	if strings.TrimSpace(output) != "" {
		t.Logf("最终 revopia cleanup 输出:\n%s", output)
	}
}

func buildRevopia(t *testing.T, ctx context.Context, binPath string) {
	t.Helper()

	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags=-s -w -X main.version=e2e", "-o", binPath, ".")
	cmd.Dir = "."
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("构建 revopia 失败\n%s\n%v", output, err)
	}
}

func createDockerVolume(t *testing.T, ctx context.Context, docker *dockerclient.Client, name string, labels map[string]string) {
	t.Helper()

	if _, err := docker.VolumeCreate(ctx, dockerclient.VolumeCreateOptions{Name: name, Labels: labels}); err != nil {
		t.Fatalf("创建 Docker volume %q 失败: %v", name, err)
	}
}

func removeDockerVolume(t *testing.T, ctx context.Context, docker *dockerclient.Client, name string) {
	t.Helper()

	if _, err := docker.VolumeRemove(ctx, name, dockerclient.VolumeRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		t.Fatalf("删除 Docker volume %q 失败: %v", name, err)
	}
}

func cleanupDockerVolume(t *testing.T, ctx context.Context, docker *dockerclient.Client, name string) {
	t.Helper()

	if _, err := docker.VolumeRemove(ctx, name, dockerclient.VolumeRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		t.Logf("清理 Docker volume %q 失败: %v", name, err)
	}
}

func removeProjectBridgeHelpers(t *testing.T, ctx context.Context, docker *dockerclient.Client, volumeName string) {
	t.Helper()

	result, err := docker.ContainerList(ctx, dockerclient.ContainerListOptions{
		All: true,
		Filters: make(dockerclient.Filters).
			Add("label", "revopia=true").
			Add("label", "revopia.volume="+volumeName),
	})
	if err != nil {
		t.Logf("查询测试 helper 容器失败: %v", err)
		return
	}
	for _, item := range result.Items {
		if _, err := docker.ContainerRemove(ctx, item.ID, dockerclient.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			t.Logf("清理测试 helper 容器 %q 失败: %v", item.ID, err)
		}
	}
}

func seedDockerVolume(t *testing.T, ctx context.Context, volumeName string) {
	t.Helper()

	seed, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Started: true,
		ContainerRequest: testcontainers.ContainerRequest{
			Image:      e2eHelperImage,
			Entrypoint: []string{"sleep"},
			Cmd:        []string{"infinity"},
			WaitingFor: wait.ForExec([]string{"test", "-d", "/seed/cache"}).WithStartupTimeout(30 * time.Second),
			HostConfigModifier: func(hostConfig *container.HostConfig) {
				hostConfig.Mounts = append(hostConfig.Mounts, mount.Mount{
					Type:   mount.TypeVolume,
					Source: volumeName,
					Target: "/seed/cache",
				})
			},
		},
	})
	testcontainers.CleanupContainer(t, seed)
	if err != nil {
		t.Fatalf("启动 seed 容器失败: %v", err)
	}
	if err := seed.CopyToContainer(ctx, []byte(e2eSeedReadme), "/seed/cache/README.txt", 0o644); err != nil {
		t.Fatalf("写入 seed 文件失败: %v", err)
	}
}

func assertRestoredFile(t *testing.T, ctx context.Context, ctr testcontainers.Container, snapshotPath string, restorePath string, relativeFile string, want string, snapshotList string, debug e2eDebug) {
	t.Helper()

	kopiaExec(t, ctx, ctr, "snapshot", "restore", snapshotPath, restorePath)
	catPath := path.Join(restorePath, relativeFile)
	exitCode, got := containerExecResult(t, ctx, ctr, "cat", catPath)
	if exitCode != 0 {
		listExitCode, listing := containerExecResult(t, ctx, ctr, "find", restorePath, "-maxdepth", "5", "-print")
		t.Fatalf("恢复文件不存在 source=%q file=%q exit=%d\nsnapshot list:\n%s\nfind exit=%d\n%s\ncat output:\n%s\n%s", snapshotPath, catPath, exitCode, snapshotList, listExitCode, listing, got, debug.String())
	}
	if got != want {
		t.Fatalf("恢复文件内容不一致 source=%q file=%q got=%q want=%q\n%s", snapshotPath, relativeFile, got, want, debug.String())
	}
}

func assertRestoredHostFile(t *testing.T, ctx context.Context, env []string, configPath string, snapshotPath string, restorePath string, relativeFile string, want string, snapshotList string, debug e2eDebug) {
	t.Helper()

	hostKopiaExec(t, ctx, env, configPath, "snapshot", "restore", snapshotPath, restorePath)
	filePath := filepath.Join(restorePath, relativeFile)
	content, err := os.ReadFile(filePath)
	if err != nil {
		listingExitCode, listing := hostCommandResult(t, ctx, env, "find", restorePath, "-maxdepth", "5", "-print")
		t.Fatalf("宿主机恢复文件不存在 source=%q file=%q error=%v\nsnapshot list:\n%s\nfind exit=%d\n%s\n%s", snapshotPath, filePath, err, snapshotList, listingExitCode, listing, debug.String())
	}
	if string(content) != want {
		t.Fatalf("宿主机恢复文件内容不一致 source=%q file=%q got=%q want=%q\n%s", snapshotPath, relativeFile, string(content), want, debug.String())
	}
}

func readOptionalContainerFile(t *testing.T, ctx context.Context, ctr testcontainers.Container, path string) string {
	t.Helper()

	exitCode, output := containerExecResult(t, ctx, ctr, "cat", path)
	if exitCode != 0 {
		return output
	}
	return output
}

func readOptionalHostFile(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	return string(content)
}

func kopiaExec(t *testing.T, ctx context.Context, ctr testcontainers.Container, args ...string) string {
	t.Helper()

	cmd := append([]string{"kopia"}, args...)
	return containerExec(t, ctx, ctr, cmd...)
}

func hostKopiaExec(t *testing.T, ctx context.Context, env []string, configPath string, args ...string) string {
	t.Helper()

	cmd := append([]string{"kopia", "--config-file=" + configPath}, args...)
	return hostCommand(t, ctx, env, cmd...)
}

func hostCommand(t *testing.T, ctx context.Context, env []string, cmd ...string) string {
	t.Helper()

	exitCode, output := hostCommandResult(t, ctx, env, cmd...)
	if exitCode != 0 {
		t.Fatalf("执行宿主机命令 %q 退出码为 %d\n%s", strings.Join(cmd, " "), exitCode, output)
	}
	return output
}

func hostCommandResult(t *testing.T, ctx context.Context, env []string, cmd ...string) (int, string) {
	t.Helper()

	if len(cmd) == 0 {
		t.Fatal("宿主机命令不能为空")
	}
	command := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	command.Env = env
	output, err := command.CombinedOutput()
	if err == nil {
		return 0, string(output)
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), string(output)
	}
	t.Fatalf("执行宿主机命令 %q 失败: %v\n%s", strings.Join(cmd, " "), err, output)
	return 1, string(output)
}

func containerExec(t *testing.T, ctx context.Context, ctr testcontainers.Container, cmd ...string) string {
	t.Helper()

	exitCode, output := containerExecResult(t, ctx, ctr, cmd...)
	if exitCode != 0 {
		t.Fatalf("执行 %q 退出码为 %d\n%s", strings.Join(cmd, " "), exitCode, output)
	}
	return output
}

func containerExecResult(t *testing.T, ctx context.Context, ctr testcontainers.Container, cmd ...string) (int, string) {
	t.Helper()

	exitCode, output, err := containerExecResultNoFatal(ctx, ctr, cmd...)
	if err != nil {
		t.Fatalf("执行 %q 失败: %v", strings.Join(cmd, " "), err)
	}
	return exitCode, output
}

func containerExecResultNoFatal(ctx context.Context, ctr testcontainers.Container, cmd ...string) (int, string, error) {
	exitCode, outputReader, err := ctr.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		return 0, "", err
	}
	output, err := io.ReadAll(outputReader)
	if err != nil {
		return 0, "", err
	}
	return exitCode, string(output), nil
}

func assertNoBridgeHelpers(t *testing.T, ctx context.Context, docker *dockerclient.Client) {
	t.Helper()

	result, err := docker.ContainerList(ctx, dockerclient.ContainerListOptions{
		All:     true,
		Filters: make(dockerclient.Filters).Add("label", "revopia=true"),
	})
	if err != nil {
		t.Fatalf("查询 bridge helper 容器失败: %v", err)
	}
	if len(result.Items) != 0 {
		names := make([]string, 0, len(result.Items))
		for _, item := range result.Items {
			names = append(names, strings.Join(item.Names, ","))
		}
		t.Fatalf("bridge helper 容器没有清理干净: %s", strings.Join(names, "; "))
	}
}

func assertNoProjectBridgeHelpers(t *testing.T, ctx context.Context, docker *dockerclient.Client, volumeName string) {
	t.Helper()

	result, err := docker.ContainerList(ctx, dockerclient.ContainerListOptions{
		All: true,
		Filters: make(dockerclient.Filters).
			Add("label", "revopia=true").
			Add("label", "revopia.volume="+volumeName),
	})
	if err != nil {
		t.Fatalf("查询测试 bridge helper 容器失败: %v", err)
	}
	if len(result.Items) != 0 {
		names := make([]string, 0, len(result.Items))
		for _, item := range result.Items {
			names = append(names, strings.Join(item.Names, ","))
		}
		t.Fatalf("测试 bridge helper 容器没有清理干净: %s", strings.Join(names, "; "))
	}
}

func hostE2EEnv(binPath string, bridgeSource string, logPath string) []string {
	env := os.Environ()
	pathValue := filepath.Dir(binPath)
	if currentPath := os.Getenv("PATH"); currentPath != "" {
		pathValue += string(os.PathListSeparator) + currentPath
	}
	env = append(env,
		"PATH="+pathValue,
		"KOPIA_PASSWORD=kopia",
		"REVOPIA_RUNTIME=host",
		"REVOPIA_BRIDGE_SOURCE="+bridgeSource,
		"REVOPIA_VISIBLE_ROOT="+bridgeSource,
		"REVOPIA_RESTORE_ROOT="+filepath.Join(bridgeSource, "restore"),
		"REVOPIA_LOG_FILE="+logPath,
		"REVOPIA_HELPER_IMAGE="+e2eHelperImage,
	)
	return env
}

func runFinalHostRevopiaCleanup(t *testing.T, env []string, binPath string) {
	t.Helper()

	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	exitCode, output := hostCommandResult(t, cleanupCtx, env, binPath, "cleanup")
	if exitCode != 0 {
		t.Logf("最终宿主机 revopia cleanup 退出码为 %d\n%s", exitCode, output)
		return
	}
	if strings.TrimSpace(output) != "" {
		t.Logf("最终宿主机 revopia cleanup 输出:\n%s", output)
	}
}

func e2eTempDir(t *testing.T) string {
	t.Helper()

	dir := os.TempDir()
	if strings.HasPrefix(dir, "~/") || dir == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("展开临时目录 %q 失败: %v", dir, err)
		}
		if dir == "~" {
			dir = home
		} else {
			dir = filepath.Join(home, strings.TrimPrefix(dir, "~/"))
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("解析临时目录 %q 失败: %v", dir, err)
	}
	return abs
}

func readAll(t *testing.T, reader io.Reader) string {
	t.Helper()

	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("读取命令输出失败: %v", err)
	}
	return string(content)
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("创建目录 %q 失败: %v", path, err)
	}
}

func randomHex(t *testing.T, n int) string {
	t.Helper()

	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("生成随机后缀失败: %v", err)
	}
	return hex.EncodeToString(buf)
}
