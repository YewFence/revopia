package bridge

import (
	"path"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/volume"
)

func TestSanitizePathName(t *testing.T) {
	tests := map[string]string{
		"db":             "db",
		"  app data  ":   "app-data",
		"../secret":      "secret",
		"app/../../data": "app-..-..-data",
		"prod_db.01":     "prod_db.01",
		"***":            "",
		".":              "",
	}

	for input, want := range tests {
		if got := sanitizePathName(input); got != want {
			t.Fatalf("sanitizePathName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFriendlyNameForVolume(t *testing.T) {
	got, err := friendlyNameForVolume(volume.Volume{
		Name: "raw-volume",
		Labels: map[string]string{
			labelBackupName: "../friendly data",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "friendly-data" {
		t.Fatalf("friendly name = %q, want friendly-data", got)
	}

	got, err = friendlyNameForVolume(volume.Volume{
		Name: "raw-volume",
		Labels: map[string]string{
			labelBackupName: "***",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "raw-volume" {
		t.Fatalf("fallback friendly name = %q, want raw-volume", got)
	}
}

func TestBuildVolumeSpecsDetectsDuplicateNames(t *testing.T) {
	_, err := buildVolumeSpecs([]volume.Volume{
		{Name: "one", Labels: map[string]string{labelBackupName: "same"}},
		{Name: "two", Labels: map[string]string{labelBackupName: "same"}},
	})
	if err == nil {
		t.Fatal("expected duplicate friendly name error")
	}
	if !strings.Contains(err.Error(), "same") {
		t.Fatalf("error %q does not mention duplicate name", err)
	}
}

func TestDefaultConfigUsesHostRuntimeDefaults(t *testing.T) {
	t.Setenv("REVOPIA_RUNTIME", "host")
	t.Setenv("REVOPIA_BRIDGE_SOURCE", "")
	t.Setenv("REVOPIA_VISIBLE_ROOT", "")
	t.Setenv("REVOPIA_RESTORE_ROOT", "")
	t.Setenv("REVOPIA_HELPER_IMAGE", "")

	cfg := DefaultConfig()
	if cfg.BridgeSource != "/mnt/revopia" {
		t.Fatalf("bridge source = %q, want /mnt/revopia", cfg.BridgeSource)
	}
	if cfg.VisibleRoot != "/mnt/revopia" {
		t.Fatalf("visible root = %q, want /mnt/revopia", cfg.VisibleRoot)
	}
	if cfg.RestoreVisibleRoot != "/mnt/revopia/restore" {
		t.Fatalf("restore root = %q, want /mnt/revopia/restore", cfg.RestoreVisibleRoot)
	}
	if cfg.HelperImage != "alpine" {
		t.Fatalf("helper image = %q, want alpine", cfg.HelperImage)
	}
}

func TestDefaultConfigUsesContainerRuntimeDefaults(t *testing.T) {
	t.Setenv("REVOPIA_RUNTIME", "container")
	t.Setenv("REVOPIA_BRIDGE_SOURCE", "")
	t.Setenv("REVOPIA_VISIBLE_ROOT", "")
	t.Setenv("REVOPIA_RESTORE_ROOT", "")

	cfg := DefaultConfig()
	if cfg.BridgeSource != "/mnt/revopia" {
		t.Fatalf("bridge source = %q, want /mnt/revopia", cfg.BridgeSource)
	}
	if cfg.VisibleRoot != "/volumes" {
		t.Fatalf("visible root = %q, want /volumes", cfg.VisibleRoot)
	}
	if cfg.RestoreVisibleRoot != "/restore" {
		t.Fatalf("restore root = %q, want /restore", cfg.RestoreVisibleRoot)
	}
}

func TestDefaultConfigUsesCustomHostBridgeAsVisibleRoot(t *testing.T) {
	t.Setenv("REVOPIA_RUNTIME", "host")
	t.Setenv("REVOPIA_BRIDGE_SOURCE", "/srv/revopia")
	t.Setenv("REVOPIA_VISIBLE_ROOT", "")
	t.Setenv("REVOPIA_RESTORE_ROOT", "")

	cfg := DefaultConfig()
	if cfg.BridgeSource != "/srv/revopia" {
		t.Fatalf("bridge source = %q, want /srv/revopia", cfg.BridgeSource)
	}
	if cfg.VisibleRoot != "/srv/revopia" {
		t.Fatalf("visible root = %q, want /srv/revopia", cfg.VisibleRoot)
	}
	if cfg.RestoreVisibleRoot != "/srv/revopia/restore" {
		t.Fatalf("restore root = %q, want /srv/revopia/restore", cfg.RestoreVisibleRoot)
	}
}

func TestDefaultConfigExplicitPathsOverrideRuntimeDefaults(t *testing.T) {
	t.Setenv("REVOPIA_RUNTIME", "host")
	t.Setenv("REVOPIA_VISIBLE_ROOT", "/custom/visible")
	t.Setenv("REVOPIA_RESTORE_ROOT", "/custom/restore")
	t.Setenv("REVOPIA_HELPER_IMAGE", "busybox")

	cfg := DefaultConfig()
	if cfg.VisibleRoot != "/custom/visible" {
		t.Fatalf("visible root = %q, want /custom/visible", cfg.VisibleRoot)
	}
	if cfg.RestoreVisibleRoot != "/custom/restore" {
		t.Fatalf("restore root = %q, want /custom/restore", cfg.RestoreVisibleRoot)
	}
	if cfg.HelperImage != "busybox" {
		t.Fatalf("helper image = %q, want busybox", cfg.HelperImage)
	}
}

func TestHelperContainerNameIsStableAndOpaque(t *testing.T) {
	got := helperContainerName("db-volume")
	if got != helperContainerName("db-volume") {
		t.Fatal("helper container name is not stable")
	}
	if !strings.HasPrefix(got, helperNamePrefix) {
		t.Fatalf("helper container name %q does not have expected prefix", got)
	}
	if strings.Contains(got, "db-volume") {
		t.Fatalf("helper container name %q should not include raw volume name", got)
	}
}

func TestHelperCreateOptions(t *testing.T) {
	cfg := Config{BridgeSource: "/mnt/revopia", HelperImage: "alpine"}
	spec := volumeSpec{VolumeName: "db-data", FriendlyName: "database"}

	options := helperCreateOptions(cfg, spec)
	if options.Name != helperContainerName(spec.VolumeName) {
		t.Fatalf("container name = %q", options.Name)
	}
	if options.Config.Image != "alpine" {
		t.Fatalf("image = %q", options.Config.Image)
	}
	if got := options.Config.Labels[labelVolume]; got != spec.VolumeName {
		t.Fatalf("volume label = %q", got)
	}
	if got := options.Config.Labels[labelMode]; got != modeBackup {
		t.Fatalf("mode label = %q", got)
	}
	if got := options.HostConfig.Mounts[1].Target; got != path.Join(helperTargetRoot, spec.FriendlyName) {
		t.Fatalf("volume mount target = %q", got)
	}
	if !options.HostConfig.Mounts[1].ReadOnly {
		t.Fatal("volume mount should be read only")
	}
}

func TestHelperMatches(t *testing.T) {
	cfg := Config{BridgeSource: "/mnt/revopia", HelperImage: "alpine"}
	spec := volumeSpec{VolumeName: "db-data", FriendlyName: "database"}
	options := helperCreateOptions(cfg, spec)

	found := container.InspectResponse{
		Config:     options.Config,
		HostConfig: options.HostConfig,
	}
	if !helperMatches(found, cfg, spec) {
		t.Fatal("expected helper to match")
	}

	found.HostConfig = &container.HostConfig{
		AutoRemove:  true,
		NetworkMode: container.NetworkMode("none"),
		Mounts: []mount.Mount{
			options.HostConfig.Mounts[0],
			{
				Type:     mount.TypeVolume,
				Source:   spec.VolumeName,
				Target:   path.Join(helperTargetRoot, spec.FriendlyName),
				ReadOnly: false,
			},
		},
	}
	if helperMatches(found, cfg, spec) {
		t.Fatal("expected writable volume mount to be rejected")
	}
}

func TestRestoreHelperCreateOptions(t *testing.T) {
	cfg := Config{BridgeSource: "/mnt/revopia", RestoreVisibleRoot: "/restore", HelperImage: "alpine"}
	session := restoreSession{
		SourceVolume: "db-data",
		TargetVolume: "db-data-restore",
		FriendlyName: "database",
		SessionID:    "restore-session",
	}

	options := restoreHelperCreateOptions(cfg, session)
	if options.Name != restoreHelperContainerName(session.SessionID) {
		t.Fatalf("container name = %q", options.Name)
	}
	if got := options.Config.Labels[labelMode]; got != modeRestore {
		t.Fatalf("mode label = %q", got)
	}
	if got := options.Config.Labels[labelSession]; got != session.SessionID {
		t.Fatalf("session label = %q", got)
	}
	if got := options.HostConfig.Mounts[1].Target; got != path.Join(helperTargetRoot, restoreTargetSubdir, session.FriendlyName) {
		t.Fatalf("restore mount target = %q", got)
	}
	if options.HostConfig.Mounts[1].ReadOnly {
		t.Fatal("restore target volume mount should be writable")
	}
}

func TestRestoreHelperMatches(t *testing.T) {
	cfg := Config{BridgeSource: "/mnt/revopia", RestoreVisibleRoot: "/restore", HelperImage: "alpine"}
	session := restoreSession{
		SourceVolume: "db-data",
		TargetVolume: "db-data-restore",
		FriendlyName: "database",
		SessionID:    "restore-session",
	}
	options := restoreHelperCreateOptions(cfg, session)

	found := container.InspectResponse{
		Config:     options.Config,
		HostConfig: options.HostConfig,
	}
	if !restoreHelperMatches(found, cfg, session) {
		t.Fatal("expected restore helper to match")
	}

	found.HostConfig = &container.HostConfig{
		AutoRemove:  true,
		NetworkMode: container.NetworkMode("none"),
		Mounts: []mount.Mount{
			options.HostConfig.Mounts[0],
			{
				Type:     mount.TypeVolume,
				Source:   session.TargetVolume,
				Target:   path.Join(helperTargetRoot, restoreTargetSubdir, session.FriendlyName),
				ReadOnly: true,
			},
		},
	}
	if restoreHelperMatches(found, cfg, session) {
		t.Fatal("expected read only restore mount to be rejected")
	}
}

func TestValidateRestoreInputsRejectsUnsafeDefaults(t *testing.T) {
	cfg := Config{BridgeSource: "/mnt/revopia", RestoreVisibleRoot: "/restore", HelperImage: "alpine"}
	err := validateRestoreInputs(cfg, RestoreOptions{
		SourceVolume: "db-data",
		TargetVolume: "db-data",
	})
	if err == nil {
		t.Fatal("expected same source and target to be rejected")
	}

	err = validateRestoreInputs(cfg, RestoreOptions{
		SourceVolume:      "db-data",
		TargetVolume:      "db-data",
		AllowSourceTarget: true,
	})
	if err != nil {
		t.Fatalf("same source and target with explicit dangerous flag should pass: %v", err)
	}
}

func TestRestoreOptionsWithDefaultsGeneratesTargetVolume(t *testing.T) {
	opts := restoreOptionsWithDefaults(RestoreOptions{SourceVolume: "app data"})
	if !strings.HasPrefix(opts.TargetVolume, "app data-restore-") {
		t.Fatalf("target volume = %q, want source based restore name", opts.TargetVolume)
	}
	if len(strings.TrimPrefix(opts.TargetVolume, "app data-restore-")) != len("20060102-150405") {
		t.Fatalf("target volume = %q, want timestamp suffix", opts.TargetVolume)
	}
}

func TestIsManagedRestoreHelperSummaryAcceptsAllSessions(t *testing.T) {
	summary := container.Summary{
		Labels: map[string]string{
			labelProject:      labelTrue,
			labelMode:         modeRestore,
			labelSession:      "restore-session",
			labelSourceVolume: "db-data",
			labelTargetVolume: "db-restore",
			labelFriendlyName: "database",
		},
	}

	if !isManagedRestoreHelperSummary(summary, "") {
		t.Fatal("expected restore helper from any session to be managed")
	}
	if !isManagedRestoreHelperSummary(summary, "restore-session") {
		t.Fatal("expected matching session restore helper to be managed")
	}
	if isManagedRestoreHelperSummary(summary, "other-session") {
		t.Fatal("expected different session restore helper to be skipped")
	}
}

func TestKopiaSnapshotPathForSource(t *testing.T) {
	cfg := Config{VisibleRoot: "/volumes"}
	specs := []volumeSpec{
		{VolumeName: "db-data", FriendlyName: "database"},
		{VolumeName: "cache-data", FriendlyName: "cache"},
	}

	got, ok := kopiaSnapshotPathForSource(cfg, specs, "/volumes/database")
	if !ok {
		t.Fatal("expected single volume source to match")
	}
	if got != "/volumes/database" {
		t.Fatalf("snapshot path = %q, want /volumes/database", got)
	}

	got, ok = kopiaSnapshotPathForSource(cfg, specs, "/volumes/db-data")
	if !ok {
		t.Fatal("expected raw volume source to match")
	}
	if got != "/volumes/database" {
		t.Fatalf("snapshot path for raw volume = %q, want /volumes/database", got)
	}

	if _, ok := kopiaSnapshotPathForSource(cfg, specs, "/volumes"); ok {
		t.Fatal("did not expect visible root source to match a single volume")
	}

	if _, ok := kopiaSnapshotPathForSource(cfg, specs, "/volumes/missing"); ok {
		t.Fatal("did not expect unknown source to match")
	}
}

func TestIsManagedHelperSummary(t *testing.T) {
	summary := container.Summary{
		Labels: map[string]string{
			labelProject:      labelTrue,
			labelMode:         modeBackup,
			labelVolume:       "db-data",
			labelFriendlyName: "database",
		},
	}
	if !isManagedHelperSummary(summary) {
		t.Fatal("expected complete helper labels to be managed")
	}

	delete(summary.Labels, labelMode)
	if !isManagedHelperSummary(summary) {
		t.Fatal("expected legacy helper labels without mode to be managed")
	}

	delete(summary.Labels, labelFriendlyName)
	if isManagedHelperSummary(summary) {
		t.Fatal("expected incomplete helper labels to be ignored")
	}
}

func TestValidateCleanupFriendlyName(t *testing.T) {
	for _, name := range []string{"database", "prod_db.01"} {
		if err := validateCleanupFriendlyName(name); err != nil {
			t.Fatalf("validateCleanupFriendlyName(%q): %v", name, err)
		}
	}

	for _, name := range []string{"", ".", "..", "../secret", "app/secret", "app..data", "bad name"} {
		if err := validateCleanupFriendlyName(name); err == nil {
			t.Fatalf("validateCleanupFriendlyName(%q) should reject unsafe name", name)
		}
	}
}

func TestCleanupUnmountCommandArgs(t *testing.T) {
	targetPath := "/volumes/database"

	got := cleanupUnmountCommandArgs(CleanupOptions{}, targetPath)
	if len(got) != 1 || got[0] != targetPath {
		t.Fatalf("cleanup umount args = %v", got)
	}

	got = cleanupUnmountCommandArgs(CleanupOptions{LazyUnmount: true}, targetPath)
	if len(got) != 2 || got[0] != "-l" || got[1] != targetPath {
		t.Fatalf("lazy cleanup umount args = %v", got)
	}
}

func TestCleanupUnmountContainerCreateOptions(t *testing.T) {
	cfg := Config{BridgeSource: "/mnt/revopia", HelperImage: "alpine"}
	spec := cleanupUnmountSpec{
		FriendlyName:    "database",
		VisibleRoot:     "/volumes",
		ContainerTarget: cleanupUnmountContainerTargetPath("database"),
	}

	options := cleanupUnmountContainerCreateOptions(cfg, CleanupOptions{}, spec)
	if options.Name != cleanupUnmountContainerName(spec.FriendlyName) {
		t.Fatalf("container name = %q", options.Name)
	}
	if options.Config.Image != "alpine" {
		t.Fatalf("image = %q", options.Config.Image)
	}
	if got := options.Config.Labels[labelMode]; got != modeCleanup {
		t.Fatalf("mode label = %q", got)
	}
	if len(options.Config.Cmd) != 2 || options.Config.Cmd[0] != "umount" || options.Config.Cmd[1] != path.Join(helperTargetRoot, spec.FriendlyName) {
		t.Fatalf("cleanup command = %v", options.Config.Cmd)
	}
	if options.HostConfig.NetworkMode != container.NetworkMode("none") {
		t.Fatalf("network mode = %q", options.HostConfig.NetworkMode)
	}
	if !options.HostConfig.ReadonlyRootfs {
		t.Fatal("cleanup container rootfs should be read only")
	}
	if got := strings.Join(options.HostConfig.CapDrop, ","); got != "ALL" {
		t.Fatalf("cap drop = %q", got)
	}
	if got := strings.Join(options.HostConfig.CapAdd, ","); got != "SYS_ADMIN" {
		t.Fatalf("cap add = %q", got)
	}
	if got := strings.Join(options.HostConfig.SecurityOpt, ","); got != "no-new-privileges:true" {
		t.Fatalf("security options = %q", got)
	}
	if options.HostConfig.AutoRemove {
		t.Fatal("cleanup container should be removed explicitly after logs are collected")
	}

	if len(options.HostConfig.Mounts) != 1 {
		t.Fatalf("mount count = %d", len(options.HostConfig.Mounts))
	}
	bridgeMount := options.HostConfig.Mounts[0]
	if bridgeMount.Type != mount.TypeBind || bridgeMount.Source != cfg.BridgeSource || bridgeMount.Target != helperTargetRoot {
		t.Fatalf("bridge mount = %#v", bridgeMount)
	}
	if bridgeMount.BindOptions == nil || bridgeMount.BindOptions.Propagation != mount.PropagationRShared {
		t.Fatalf("bridge mount propagation = %#v", bridgeMount.BindOptions)
	}
}

func TestCleanupUnmountContainerCreateOptionsLazy(t *testing.T) {
	cfg := Config{BridgeSource: "/mnt/revopia", HelperImage: "alpine"}
	spec := cleanupUnmountSpec{
		FriendlyName:    "database",
		VisibleRoot:     "/volumes",
		ContainerTarget: cleanupUnmountContainerTargetPath("database"),
	}

	options := cleanupUnmountContainerCreateOptions(cfg, CleanupOptions{LazyUnmount: true}, spec)
	wantTarget := path.Join(helperTargetRoot, spec.FriendlyName)
	if len(options.Config.Cmd) != 3 || options.Config.Cmd[0] != "umount" || options.Config.Cmd[1] != "-l" || options.Config.Cmd[2] != wantTarget {
		t.Fatalf("lazy cleanup command = %v", options.Config.Cmd)
	}
}

func TestRestoreCleanupUnmountContainerTargetPath(t *testing.T) {
	cfg := Config{BridgeSource: "/mnt/revopia", HelperImage: "alpine"}
	target := restoreCleanupTarget{FriendlyName: "database"}
	spec := cleanupUnmountSpec{
		FriendlyName:    target.FriendlyName,
		VisibleRoot:     "/restore",
		ContainerTarget: restoreCleanupContainerTargetPath(target.FriendlyName),
	}

	options := cleanupUnmountContainerCreateOptions(cfg, CleanupOptions{}, spec)
	wantTarget := path.Join(helperTargetRoot, restoreTargetSubdir, target.FriendlyName)
	if len(options.Config.Cmd) != 2 || options.Config.Cmd[0] != "umount" || options.Config.Cmd[1] != wantTarget {
		t.Fatalf("restore cleanup command = %v", options.Config.Cmd)
	}
}

func TestCleanupTargetPathChecked(t *testing.T) {
	got, err := cleanupTargetPathChecked("/volumes", "database")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/volumes/database" {
		t.Fatalf("cleanup target path = %q", got)
	}

	if _, err := cleanupTargetPathChecked("/volumes", "../database"); err == nil {
		t.Fatal("expected escaped target path to be rejected")
	}
}
