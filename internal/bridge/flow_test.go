package bridge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/volume"
	dockerclient "github.com/moby/moby/client"
)

func TestPrepareCreatesHelperAndPrintsSnapshotPath(t *testing.T) {
	t.Setenv("KOPIA_SOURCE_PATH", "/volumes/db-data")
	restoreInspectVisiblePath(t, func(target string) visiblePathStatus {
		return visiblePathStatus{Path: target, Exists: true, IsDir: true, IsMount: true}
	})

	api := &fakeDockerAPI{
		volumeListResult: dockerclient.VolumeListResult{
			Items: []volume.Volume{{
				Name:   "db-data",
				Labels: map[string]string{labelBackupName: "database"},
			}},
		},
	}
	cfg := Config{
		BridgeSource:  "/mnt/revopia",
		VisibleRoot:   "/volumes",
		HelperImage:   "alpine",
		VerifyTimeout: 0,
	}
	var out bytes.Buffer

	if err := Prepare(context.Background(), api, cfg, &out, discardLogger()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "创建 db-data -> /volumes/database\n") {
		t.Fatalf("prepare output = %q, want created helper line", got)
	}
	if !strings.Contains(got, "KOPIA_SNAPSHOT_PATH=/volumes/database\n") {
		t.Fatalf("prepare output = %q, want remapped snapshot path", got)
	}
	if len(api.createdContainers) != 1 {
		t.Fatalf("created container count = %d, want 1", len(api.createdContainers))
	}
	created := api.createdContainers[0]
	if created.Name != helperContainerName("db-data") {
		t.Fatalf("created container name = %q", created.Name)
	}
	if created.Config.Labels[labelMode] != modeBackup || created.Config.Labels[labelFriendlyName] != "database" {
		t.Fatalf("created labels = %v", created.Config.Labels)
	}
	if len(api.startedContainers) != 1 || api.startedContainers[0] == "" {
		t.Fatalf("started containers = %v, want created helper start", api.startedContainers)
	}
}

func TestPrepareRecreatesMismatchedHelper(t *testing.T) {
	restoreInspectVisiblePath(t, func(target string) visiblePathStatus {
		return visiblePathStatus{Path: target, Exists: true, IsDir: true, IsMount: true}
	})

	cfg := Config{
		BridgeSource:  "/mnt/revopia",
		VisibleRoot:   "/volumes",
		HelperImage:   "alpine",
		VerifyTimeout: 0,
	}
	spec := volumeSpec{VolumeName: "db-data", FriendlyName: "database"}
	api := &fakeDockerAPI{
		volumeListResult: dockerclient.VolumeListResult{
			Items: []volume.Volume{{Name: spec.VolumeName, Labels: map[string]string{labelBackupName: spec.FriendlyName}}},
		},
		containerListFunc: func(context.Context, dockerclient.ContainerListOptions) (dockerclient.ContainerListResult, error) {
			return dockerclient.ContainerListResult{Items: []container.Summary{{
				ID:     "old-helper",
				Names:  []string{"/" + helperContainerName(spec.VolumeName)},
				Labels: helperLabels(spec),
			}}}, nil
		},
		containerInspectFunc: func(context.Context, string, dockerclient.ContainerInspectOptions) (dockerclient.ContainerInspectResult, error) {
			options := helperCreateOptions(cfg, spec)
			options.Config.Image = "busybox"
			return dockerclient.ContainerInspectResult{Container: container.InspectResponse{
				Config:     options.Config,
				HostConfig: options.HostConfig,
				State:      &container.State{Running: true},
			}}, nil
		},
	}
	var out bytes.Buffer

	if err := Prepare(context.Background(), api, cfg, &out, discardLogger()); err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}

	if !strings.Contains(out.String(), "重建 db-data -> /volumes/database\n") {
		t.Fatalf("prepare output = %q, want recreated helper line", out.String())
	}
	if len(api.removedContainers) != 1 || api.removedContainers[0] != "old-helper" {
		t.Fatalf("removed containers = %v, want old helper removed", api.removedContainers)
	}
	if len(api.createdContainers) != 1 {
		t.Fatalf("created container count = %d, want replacement helper", len(api.createdContainers))
	}
}

func TestRestoreCreatesTargetAndPrintsCommands(t *testing.T) {
	restoreInspectVisiblePath(t, func(target string) visiblePathStatus {
		return visiblePathStatus{Path: target, Exists: true, IsDir: true, IsMount: true}
	})

	api := &fakeDockerAPI{
		volumeInspectFunc: func(_ context.Context, name string, _ dockerclient.VolumeInspectOptions) (dockerclient.VolumeInspectResult, error) {
			switch name {
			case "db-data":
				return dockerclient.VolumeInspectResult{Volume: volume.Volume{
					Name:   "db-data",
					Labels: map[string]string{labelBackupName: "database"},
				}}, nil
			case "db-restored":
				return dockerclient.VolumeInspectResult{}, cerrdefs.ErrNotFound
			default:
				return dockerclient.VolumeInspectResult{}, fmt.Errorf("unexpected volume inspect %q", name)
			}
		},
	}
	cfg := Config{
		BridgeSource:       "/mnt/revopia",
		VisibleRoot:        "/volumes",
		RestoreVisibleRoot: "/restore",
		HelperImage:        "alpine",
		VerifyTimeout:      0,
	}
	opts := RestoreOptions{
		SourceVolume:        "db-data",
		TargetVolume:        "db-restored",
		SnapshotTime:        "latest",
		SessionID:           "session-1",
		AllowNonEmptyTarget: true,
	}
	var out bytes.Buffer

	if err := Restore(context.Background(), api, cfg, opts, &out, discardLogger()); err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}

	if len(api.createdVolumes) != 1 {
		t.Fatalf("created volume count = %d, want 1", len(api.createdVolumes))
	}
	labels := api.createdVolumes[0].Labels
	if labels[labelMode] != modeRestore || labels[labelSession] != "session-1" || labels[labelSourceVolume] != "db-data" || labels[labelTargetVolume] != "db-restored" {
		t.Fatalf("created volume labels = %v", labels)
	}
	if len(api.createdContainers) != 1 || api.createdContainers[0].Name != restoreHelperContainerName("session-1") {
		t.Fatalf("created containers = %v, want restore helper", containerNames(api.createdContainers))
	}

	got := out.String()
	for _, want := range []string{
		"创建 db-restored -> /restore/database\n",
		"RESTORE_SESSION_ID=session-1\n",
		"RESTORE_TARGET_PATH=/restore/database\n",
		"已创建目标 volume db-restored\n",
		"kopia snapshot list /volumes/database\n",
		"kopia snapshot restore /volumes/database /restore/database --snapshot-time latest\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("restore output = %q, want %q", got, want)
		}
	}
}

func TestRestoreRejectsNonEmptyTargetVolume(t *testing.T) {
	api := &fakeDockerAPI{
		volumeInspectFunc: func(_ context.Context, name string, _ dockerclient.VolumeInspectOptions) (dockerclient.VolumeInspectResult, error) {
			switch name {
			case "db-data":
				return dockerclient.VolumeInspectResult{Volume: volume.Volume{
					Name:   "db-data",
					Labels: map[string]string{labelBackupName: "database"},
				}}, nil
			case "db-restored":
				return dockerclient.VolumeInspectResult{Volume: volume.Volume{Name: "db-restored"}}, nil
			default:
				return dockerclient.VolumeInspectResult{}, fmt.Errorf("unexpected volume inspect %q", name)
			}
		},
		containerWaitFunc: func(context.Context, string, dockerclient.ContainerWaitOptions) dockerclient.ContainerWaitResult {
			result := make(chan container.WaitResponse, 1)
			result <- container.WaitResponse{StatusCode: 1}
			close(result)
			waitErr := make(chan error)
			return dockerclient.ContainerWaitResult{Result: result, Error: waitErr}
		},
	}
	cfg := Config{
		BridgeSource:       "/mnt/revopia",
		VisibleRoot:        "/volumes",
		RestoreVisibleRoot: "/restore",
		HelperImage:        "alpine",
		VerifyTimeout:      0,
	}
	opts := RestoreOptions{
		SourceVolume: "db-data",
		TargetVolume: "db-restored",
		SessionID:    "session-1",
	}

	err := Restore(context.Background(), api, cfg, opts, io.Discard, discardLogger())
	if err == nil {
		t.Fatal("expected non-empty target volume error")
	}
	if !strings.Contains(err.Error(), "目标 volume \"db-restored\" 已存在且不是空目录") {
		t.Fatalf("error = %q, want non-empty target error", err)
	}
	hints := strings.Join(HintsForError(err), "\n")
	if !strings.Contains(hints, "--dangerously-allow-non-empty-target") {
		t.Fatalf("hints = %q, want dangerous reuse flag", hints)
	}
	if len(api.removedContainers) != 1 {
		t.Fatalf("removed containers = %v, want empty check container cleanup", api.removedContainers)
	}
	if len(api.createdContainers) != 1 || !strings.Contains(api.createdContainers[0].Name, "empty-check") {
		t.Fatalf("created containers = %v, want empty check container", containerNames(api.createdContainers))
	}
}

func TestCleanupRemovesManagedHelpersAndSkipsMissingMount(t *testing.T) {
	visibleRoot := t.TempDir()
	api := &fakeDockerAPI{
		containerListFunc: func(context.Context, dockerclient.ContainerListOptions) (dockerclient.ContainerListResult, error) {
			return dockerclient.ContainerListResult{Items: []container.Summary{{
				ID: "helper-1",
				Labels: map[string]string{
					labelProject:      labelTrue,
					labelMode:         modeBackup,
					labelVolume:       "db-data",
					labelFriendlyName: "database",
				},
			}}}, nil
		},
	}
	cfg := Config{
		BridgeSource: "/mnt/revopia",
		VisibleRoot:  filepath.Join(visibleRoot, "volumes"),
		HelperImage:  "alpine",
	}
	var out bytes.Buffer

	if err := Cleanup(context.Background(), api, cfg, CleanupOptions{}, &out, discardLogger()); err != nil {
		t.Fatalf("Cleanup returned error: %v", err)
	}

	if len(api.removedContainers) != 1 || api.removedContainers[0] != "helper-1" {
		t.Fatalf("removed containers = %v, want helper removed", api.removedContainers)
	}
	want := "已清理 1 个 helper 容器，已回收 0 个传播挂载，跳过 1 个非挂载路径\n"
	if out.String() != want {
		t.Fatalf("cleanup output = %q, want %q", out.String(), want)
	}
}

func TestRestoreCleanupRemovesOnlyRequestedSession(t *testing.T) {
	restoreRoot := t.TempDir()
	api := &fakeDockerAPI{
		containerListFunc: func(context.Context, dockerclient.ContainerListOptions) (dockerclient.ContainerListResult, error) {
			return dockerclient.ContainerListResult{Items: []container.Summary{
				{
					ID: "restore-helper-1",
					Labels: map[string]string{
						labelProject:      labelTrue,
						labelMode:         modeRestore,
						labelSession:      "session-1",
						labelSourceVolume: "db-data",
						labelTargetVolume: "db-restored",
						labelFriendlyName: "database",
					},
				},
				{
					ID: "restore-helper-2",
					Labels: map[string]string{
						labelProject:      labelTrue,
						labelMode:         modeRestore,
						labelSession:      "session-2",
						labelSourceVolume: "cache-data",
						labelTargetVolume: "cache-restored",
						labelFriendlyName: "cache",
					},
				},
			}}, nil
		},
	}
	cfg := Config{
		BridgeSource:       "/mnt/revopia",
		RestoreVisibleRoot: restoreRoot,
		HelperImage:        "alpine",
	}
	var out bytes.Buffer

	if err := RestoreCleanup(context.Background(), api, cfg, CleanupOptions{}, "session-1", &out, discardLogger()); err != nil {
		t.Fatalf("RestoreCleanup returned error: %v", err)
	}

	if len(api.removedContainers) != 1 || api.removedContainers[0] != "restore-helper-1" {
		t.Fatalf("removed containers = %v, want only requested session helper", api.removedContainers)
	}
	want := "已清理 1 个恢复 helper 容器，session=session-1，已回收 0 个恢复挂载，跳过 1 个非挂载路径，目标 volume 不会被删除\n"
	if out.String() != want {
		t.Fatalf("restore cleanup output = %q, want %q", out.String(), want)
	}
}

func TestInspectStatePrintsVolumesAndHelpers(t *testing.T) {
	restoreInspectVisiblePath(t, func(target string) visiblePathStatus {
		return visiblePathStatus{Path: target, Exists: true, IsDir: true, IsMount: strings.HasPrefix(target, "/volumes/")}
	})

	cfg := Config{
		BridgeSource: "/mnt/revopia",
		VisibleRoot:  "/volumes",
		HelperImage:  "alpine",
	}
	spec := volumeSpec{VolumeName: "db-data", FriendlyName: "database"}
	api := &fakeDockerAPI{
		volumeListResult: dockerclient.VolumeListResult{
			Items: []volume.Volume{
				{Name: spec.VolumeName, Labels: map[string]string{labelBackupEnable: labelTrue, labelBackupName: spec.FriendlyName}},
				{Name: "cache-data"},
			},
		},
		containerListFunc: func(context.Context, dockerclient.ContainerListOptions) (dockerclient.ContainerListResult, error) {
			return dockerclient.ContainerListResult{Items: []container.Summary{{
				ID:     "helper-1",
				Names:  []string{"/" + helperContainerName(spec.VolumeName)},
				Labels: helperLabels(spec),
			}}}, nil
		},
		containerInspectFunc: func(context.Context, string, dockerclient.ContainerInspectOptions) (dockerclient.ContainerInspectResult, error) {
			options := helperCreateOptions(cfg, spec)
			return dockerclient.ContainerInspectResult{Container: container.InspectResponse{
				Config:     options.Config,
				HostConfig: options.HostConfig,
				State:      &container.State{Running: true},
			}}, nil
		},
	}
	var out bytes.Buffer

	if err := InspectState(context.Background(), api, cfg, &out, discardLogger()); err != nil {
		t.Fatalf("InspectState returned error: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"配置 bridge_source=/mnt/revopia visible_root=/volumes helper_image=alpine\n",
		"Docker volume 总数 2，启用备份的 volume 数 1\n",
		"volume db-data backup_name=\"database\" friendly=database",
		"helper 容器数 1\n",
		"known_volume=true config_match=true",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("inspect output = %q, want %q", got, want)
		}
	}
}

func restoreInspectVisiblePath(t *testing.T, fn func(string) visiblePathStatus) {
	t.Helper()

	original := inspectVisiblePathFunc
	inspectVisiblePathFunc = fn
	t.Cleanup(func() {
		inspectVisiblePathFunc = original
	})
}

func discardLogger() Logger {
	return NewLogger(io.Discard)
}

func containerNames(options []dockerclient.ContainerCreateOptions) []string {
	names := make([]string, 0, len(options))
	for _, option := range options {
		names = append(names, option.Name)
	}
	return names
}

type fakeDockerAPI struct {
	volumeListResult     dockerclient.VolumeListResult
	volumeListErr        error
	volumeInspectFunc    func(context.Context, string, dockerclient.VolumeInspectOptions) (dockerclient.VolumeInspectResult, error)
	volumeCreateFunc     func(context.Context, dockerclient.VolumeCreateOptions) (dockerclient.VolumeCreateResult, error)
	containerListFunc    func(context.Context, dockerclient.ContainerListOptions) (dockerclient.ContainerListResult, error)
	containerInspectFunc func(context.Context, string, dockerclient.ContainerInspectOptions) (dockerclient.ContainerInspectResult, error)
	containerCreateFunc  func(context.Context, dockerclient.ContainerCreateOptions) (dockerclient.ContainerCreateResult, error)
	containerStartFunc   func(context.Context, string, dockerclient.ContainerStartOptions) (dockerclient.ContainerStartResult, error)
	containerWaitFunc    func(context.Context, string, dockerclient.ContainerWaitOptions) dockerclient.ContainerWaitResult
	containerLogsFunc    func(context.Context, string, dockerclient.ContainerLogsOptions) (dockerclient.ContainerLogsResult, error)
	containerRemoveFunc  func(context.Context, string, dockerclient.ContainerRemoveOptions) (dockerclient.ContainerRemoveResult, error)
	createdVolumes       []dockerclient.VolumeCreateOptions
	createdContainers    []dockerclient.ContainerCreateOptions
	startedContainers    []string
	removedContainers    []string
}

func (api *fakeDockerAPI) VolumeList(ctx context.Context, opts dockerclient.VolumeListOptions) (dockerclient.VolumeListResult, error) {
	if api.volumeListErr != nil {
		return dockerclient.VolumeListResult{}, api.volumeListErr
	}
	return api.volumeListResult, nil
}

func (api *fakeDockerAPI) VolumeInspect(ctx context.Context, name string, opts dockerclient.VolumeInspectOptions) (dockerclient.VolumeInspectResult, error) {
	if api.volumeInspectFunc != nil {
		return api.volumeInspectFunc(ctx, name, opts)
	}
	return dockerclient.VolumeInspectResult{}, cerrdefs.ErrNotFound
}

func (api *fakeDockerAPI) VolumeCreate(ctx context.Context, opts dockerclient.VolumeCreateOptions) (dockerclient.VolumeCreateResult, error) {
	api.createdVolumes = append(api.createdVolumes, opts)
	if api.volumeCreateFunc != nil {
		return api.volumeCreateFunc(ctx, opts)
	}
	return dockerclient.VolumeCreateResult{Volume: volume.Volume{Name: opts.Name, Labels: opts.Labels}}, nil
}

func (api *fakeDockerAPI) ContainerList(ctx context.Context, opts dockerclient.ContainerListOptions) (dockerclient.ContainerListResult, error) {
	if api.containerListFunc != nil {
		return api.containerListFunc(ctx, opts)
	}
	return dockerclient.ContainerListResult{}, nil
}

func (api *fakeDockerAPI) ContainerInspect(ctx context.Context, id string, opts dockerclient.ContainerInspectOptions) (dockerclient.ContainerInspectResult, error) {
	if api.containerInspectFunc != nil {
		return api.containerInspectFunc(ctx, id, opts)
	}
	return dockerclient.ContainerInspectResult{}, cerrdefs.ErrNotFound
}

func (api *fakeDockerAPI) ContainerCreate(ctx context.Context, opts dockerclient.ContainerCreateOptions) (dockerclient.ContainerCreateResult, error) {
	api.createdContainers = append(api.createdContainers, opts)
	if api.containerCreateFunc != nil {
		return api.containerCreateFunc(ctx, opts)
	}
	id := opts.Name
	if id == "" {
		id = fmt.Sprintf("container-%d", len(api.createdContainers))
	}
	return dockerclient.ContainerCreateResult{ID: id}, nil
}

func (api *fakeDockerAPI) ContainerStart(ctx context.Context, id string, opts dockerclient.ContainerStartOptions) (dockerclient.ContainerStartResult, error) {
	api.startedContainers = append(api.startedContainers, id)
	if api.containerStartFunc != nil {
		return api.containerStartFunc(ctx, id, opts)
	}
	return dockerclient.ContainerStartResult{}, nil
}

func (api *fakeDockerAPI) ContainerWait(ctx context.Context, id string, opts dockerclient.ContainerWaitOptions) dockerclient.ContainerWaitResult {
	if api.containerWaitFunc != nil {
		return api.containerWaitFunc(ctx, id, opts)
	}
	result := make(chan container.WaitResponse, 1)
	result <- container.WaitResponse{StatusCode: 0}
	close(result)
	waitErr := make(chan error)
	return dockerclient.ContainerWaitResult{Result: result, Error: waitErr}
}

func (api *fakeDockerAPI) ContainerLogs(ctx context.Context, id string, opts dockerclient.ContainerLogsOptions) (dockerclient.ContainerLogsResult, error) {
	if api.containerLogsFunc != nil {
		return api.containerLogsFunc(ctx, id, opts)
	}
	return io.NopCloser(strings.NewReader("")), nil
}

func (api *fakeDockerAPI) ContainerRemove(ctx context.Context, id string, opts dockerclient.ContainerRemoveOptions) (dockerclient.ContainerRemoveResult, error) {
	api.removedContainers = append(api.removedContainers, id)
	if api.containerRemoveFunc != nil {
		return api.containerRemoveFunc(ctx, id, opts)
	}
	return dockerclient.ContainerRemoveResult{}, nil
}
