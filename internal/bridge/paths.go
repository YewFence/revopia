package bridge

import (
	"path/filepath"
	"strings"
)

func visiblePath(cfg Config, spec volumeSpec) string {
	return filepath.Join(cfg.VisibleRoot, spec.FriendlyName)
}

func restoreVisiblePath(cfg Config, friendlyName string) string {
	return filepath.Join(cfg.RestoreVisibleRoot, friendlyName)
}

func kopiaSnapshotPathForSource(cfg Config, specs []volumeSpec, sourcePath string) (string, bool) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return "", false
	}

	cleanSource := filepath.Clean(sourcePath)
	for _, spec := range specs {
		target := visiblePath(cfg, spec)
		if filepath.Clean(target) == cleanSource {
			return target, true
		}
		rawTarget := filepath.Join(cfg.VisibleRoot, sanitizePathName(spec.VolumeName))
		if filepath.Clean(rawTarget) == cleanSource {
			return target, true
		}
	}
	return "", false
}
