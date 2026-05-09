package bridge

import (
	"context"
	"os"
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

var appVersion = "dev"

type DockerAPI interface {
	VolumeList(context.Context, dockerclient.VolumeListOptions) (dockerclient.VolumeListResult, error)
	VolumeInspect(context.Context, string, dockerclient.VolumeInspectOptions) (dockerclient.VolumeInspectResult, error)
	VolumeCreate(context.Context, dockerclient.VolumeCreateOptions) (dockerclient.VolumeCreateResult, error)
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
	return Config{
		BridgeSource:       getenvDefault("REVOPIA_BRIDGE_SOURCE", "/mnt/revopia"),
		VisibleRoot:        getenvDefault("REVOPIA_VISIBLE_ROOT", "/volumes"),
		RestoreVisibleRoot: getenvDefault("REVOPIA_RESTORE_ROOT", "/restore"),
		HelperImage:        getenvDefault("REVOPIA_HELPER_IMAGE", "alpine"),
		VerifyTimeout:      defaultVerifyTimeout,
	}
}

func SetVersion(version string) {
	appVersion = version
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
