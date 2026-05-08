package cmd

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
	cfg := bridgeConfig{BridgeSource: "/mnt/volumes-backup", HelperImage: "alpine"}
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
	cfg := bridgeConfig{BridgeSource: "/mnt/volumes-backup", HelperImage: "alpine"}
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
	cfg := bridgeConfig{BridgeSource: "/mnt/volumes-backup", RestoreVisibleRoot: "/restore", HelperImage: "alpine"}
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
	cfg := bridgeConfig{BridgeSource: "/mnt/volumes-backup", RestoreVisibleRoot: "/restore", HelperImage: "alpine"}
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
	cfg := bridgeConfig{BridgeSource: "/mnt/volumes-backup", RestoreVisibleRoot: "/restore", HelperImage: "alpine"}
	err := validateRestoreInputs(cfg, restoreOptions{
		SourceVolume: "db-data",
		TargetVolume: "db-data",
	})
	if err == nil {
		t.Fatal("expected same source and target to be rejected")
	}

	err = validateRestoreInputs(cfg, restoreOptions{
		SourceVolume:      "db-data",
		TargetVolume:      "db-data",
		AllowSourceTarget: true,
	})
	if err != nil {
		t.Fatalf("same source and target with explicit dangerous flag should pass: %v", err)
	}
}

func TestKopiaSnapshotPathForSource(t *testing.T) {
	cfg := bridgeConfig{VisibleRoot: "/volumes"}
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
