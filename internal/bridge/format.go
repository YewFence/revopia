package bridge

import (
	"fmt"
	"sort"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

func describeInspect(found container.InspectResponse, cfg Config, spec volumeSpec) string {
	state := "<nil>"
	if found.State != nil {
		state = fmt.Sprintf("status=%s running=%t exit=%d error=%q", found.State.Status, found.State.Running, found.State.ExitCode, found.State.Error)
	}
	image := "<nil>"
	labels := ""
	cmd := ""
	if found.Config != nil {
		image = found.Config.Image
		labels = formatLabels(found.Config.Labels)
		cmd = strings.Join(found.Config.Cmd, " ")
	}
	networkMode := ""
	autoRemove := false
	mounts := ""
	if found.HostConfig != nil {
		networkMode = string(found.HostConfig.NetworkMode)
		autoRemove = found.HostConfig.AutoRemove
		mounts = formatCreateMounts(found.HostConfig.Mounts)
	}
	return fmt.Sprintf("image=%q cmd=%q %s network=%q autoremove=%t labels=%q mounts=%q expected_match=%t", image, cmd, state, networkMode, autoRemove, labels, mounts, helperMatches(found, cfg, spec))
}

func describeRestoreInspect(found container.InspectResponse, cfg Config, session restoreSession) string {
	state := "<nil>"
	if found.State != nil {
		state = fmt.Sprintf("status=%s running=%t exit=%d error=%q", found.State.Status, found.State.Running, found.State.ExitCode, found.State.Error)
	}
	image := "<nil>"
	labels := ""
	cmd := ""
	if found.Config != nil {
		image = found.Config.Image
		labels = formatLabels(found.Config.Labels)
		cmd = strings.Join(found.Config.Cmd, " ")
	}
	networkMode := ""
	autoRemove := false
	mounts := ""
	if found.HostConfig != nil {
		networkMode = string(found.HostConfig.NetworkMode)
		autoRemove = found.HostConfig.AutoRemove
		mounts = formatCreateMounts(found.HostConfig.Mounts)
	}
	return fmt.Sprintf("image=%q cmd=%q %s network=%q autoremove=%t labels=%q mounts=%q expected_match=%t", image, cmd, state, networkMode, autoRemove, labels, mounts, restoreHelperMatches(found, cfg, session))
}

func formatCreateMounts(mounts []mount.Mount) string {
	parts := make([]string, 0, len(mounts))
	for _, item := range mounts {
		piece := fmt.Sprintf("%s:%s->%s", item.Type, item.Source, item.Target)
		if item.ReadOnly {
			piece += ":ro"
		}
		if item.BindOptions != nil && item.BindOptions.Propagation != "" {
			piece += ":propagation=" + string(item.BindOptions.Propagation)
		}
		parts = append(parts, piece)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	return strings.Join(parts, ",")
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}
