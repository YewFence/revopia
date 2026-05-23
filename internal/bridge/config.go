package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	dockerclient "github.com/moby/moby/client"
)

const (
	labelBackupEnable = "backup.enable"
	labelBackupName   = "backup.name"

	labelProject       = "revopia"
	labelVolume        = "revopia.volume"
	labelFriendlyName  = "revopia.name"
	labelMode          = "revopia.mode"
	labelSession       = "revopia.session"
	labelSourceVolume  = "revopia.source-volume"
	labelTargetVolume  = "revopia.target-volume"
	labelCreatedAt     = "revopia.created-at"
	labelCreatedBy     = "revopia.created-by"
	labelRestoreTarget = "revopia.restore-target"

	labelTrue = "true"

	modeBackup  = "backup"
	modeRestore = "restore"
	modeCleanup = "cleanup"

	helperNamePrefix         = "revopia-"
	restoreHelperNamePrefix  = "revopia-restore-bridge-"
	cleanupUnmountNamePrefix = "revopia-cleanup-umount-"
	helperTargetRoot         = "/bridge"
	restoreTargetSubdir      = "restore"
	defaultVerifyTimeout     = 5 * time.Second
)

var helperCommand = []string{"sleep", "infinity"}
var emptyCheckCommand = []string{"sh", "-c", "set -eu; test -d /target; if find /target -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then exit 1; fi"}
var statFile = os.Stat
var readFile = os.ReadFile

var appVersion = "dev"

type DockerAPI interface {
	VolumeList(context.Context, dockerclient.VolumeListOptions) (dockerclient.VolumeListResult, error)
	VolumeInspect(context.Context, string, dockerclient.VolumeInspectOptions) (dockerclient.VolumeInspectResult, error)
	VolumeCreate(context.Context, dockerclient.VolumeCreateOptions) (dockerclient.VolumeCreateResult, error)
	ImagePull(context.Context, string, dockerclient.ImagePullOptions) (dockerclient.ImagePullResponse, error)
	ContainerList(context.Context, dockerclient.ContainerListOptions) (dockerclient.ContainerListResult, error)
	ContainerInspect(context.Context, string, dockerclient.ContainerInspectOptions) (dockerclient.ContainerInspectResult, error)
	ContainerCreate(context.Context, dockerclient.ContainerCreateOptions) (dockerclient.ContainerCreateResult, error)
	ContainerStart(context.Context, string, dockerclient.ContainerStartOptions) (dockerclient.ContainerStartResult, error)
	ContainerWait(context.Context, string, dockerclient.ContainerWaitOptions) dockerclient.ContainerWaitResult
	ContainerLogs(context.Context, string, dockerclient.ContainerLogsOptions) (dockerclient.ContainerLogsResult, error)
	ContainerRemove(context.Context, string, dockerclient.ContainerRemoveOptions) (dockerclient.ContainerRemoveResult, error)
}

type Config struct {
	BridgeSource       string
	VisibleRoot        string
	RestoreVisibleRoot string
	HelperImage        string
	VerifyTimeout      time.Duration
}

type volumeSpec struct {
	VolumeName   string
	FriendlyName string
}

type RestoreOptions struct {
	SourceVolume        string
	TargetVolume        string
	SourceDirectoryID   string
	SnapshotTime        string
	SessionID           string
	AllowSourceTarget   bool
	AllowNonEmptyTarget bool
}

type CleanupOptions struct {
	LazyUnmount bool
}

type restoreSession struct {
	SourceVolume  string
	TargetVolume  string
	FriendlyName  string
	SessionID     string
	TargetPath    string
	TargetCreated bool
	HelperAction  string
}

func DefaultConfig() Config {
	runtime := defaultRuntime()
	bridgeSource := getenvDefault("REVOPIA_BRIDGE_SOURCE", "/mnt/revopia")
	visibleRoot := bridgeSource
	restoreRoot := filepath.Join(bridgeSource, "restore")
	if runtime == runtimeContainer {
		visibleRoot = "/volumes"
		restoreRoot = "/restore"
	}

	return Config{
		BridgeSource:       bridgeSource,
		VisibleRoot:        getenvDefault("REVOPIA_VISIBLE_ROOT", visibleRoot),
		RestoreVisibleRoot: getenvDefault("REVOPIA_RESTORE_ROOT", restoreRoot),
		HelperImage:        getenvDefault("REVOPIA_HELPER_IMAGE", "alpine"),
		VerifyTimeout:      defaultVerifyTimeout,
	}
}

func SetVersion(version string) {
	appVersion = version
}

func RunningInContainer() bool {
	return defaultRuntime() == runtimeContainer
}

func getenvDefault(key, fallback string) string {
	if value := strings.TrimSpace(getenv(key)); value != "" {
		return value
	}
	return fallback
}

var getenv = func(key string) string {
	return strings.TrimSpace(strings.Trim(os.Getenv(key), "\x00"))
}

type runtimeEnvironment string

const (
	runtimeHost      runtimeEnvironment = "host"
	runtimeContainer runtimeEnvironment = "container"
)

func defaultRuntime() runtimeEnvironment {
	switch strings.ToLower(getenv("REVOPIA_RUNTIME")) {
	case string(runtimeHost):
		return runtimeHost
	case string(runtimeContainer):
		return runtimeContainer
	}
	if detectedInContainer() {
		return runtimeContainer
	}
	return runtimeHost
}

func detectedInContainer() bool {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := statFile(marker); err == nil {
			return true
		}
	}

	content, err := readFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	text := strings.ToLower(string(content))
	return strings.Contains(text, "docker") ||
		strings.Contains(text, "kubepods") ||
		strings.Contains(text, "containerd") ||
		strings.Contains(text, "libpod")
}
