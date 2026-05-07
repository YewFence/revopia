package main

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

func TestIsManagedHelperSummary(t *testing.T) {
	summary := container.Summary{
		Labels: map[string]string{
			labelProject:      labelTrue,
			labelVolume:       "db-data",
			labelFriendlyName: "database",
		},
	}
	if !isManagedHelperSummary(summary) {
		t.Fatal("expected complete helper labels to be managed")
	}

	delete(summary.Labels, labelFriendlyName)
	if isManagedHelperSummary(summary) {
		t.Fatal("expected incomplete helper labels to be ignored")
	}
}
