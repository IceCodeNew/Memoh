package workspace

import (
	"strings"

	ctr "github.com/felinics/memoh/internal/container"
)

var knownContainerPrefixes = []string{ContainerPrefix, LegacyContainerPrefix}

// InstanceKeyFromContainerID is the part of a workspace container name after
// the known prefixes. It keys every host-side resource of the instance (socket
// directory, display socket, runtime dial target). Containers created before
// managed computers had their own ids carry the bot id here; newer ones carry
// the workspace id. Neither has to move for the other to work.
func InstanceKeyFromContainerID(containerID string) (string, bool) {
	containerID = strings.TrimSpace(containerID)
	for _, prefix := range knownContainerPrefixes {
		if !strings.HasPrefix(containerID, prefix) {
			continue
		}
		key := strings.TrimPrefix(containerID, prefix)
		if key == "" {
			return "", false
		}
		return key, true
	}
	return "", false
}

// instanceKey is InstanceKeyFromContainerID for ids that are trusted to be
// workspace containers; an id without a known prefix is its own key.
func instanceKey(containerID string) string {
	if key, ok := InstanceKeyFromContainerID(containerID); ok {
		return key
	}
	return strings.TrimSpace(containerID)
}

// WorkspaceIDFromContainerInfo reads the managed computer a container was
// created for. Only the label says so: a container name carries an instance
// key, which is a bot id on older containers.
func WorkspaceIDFromContainerInfo(info ctr.ContainerInfo) (string, bool) {
	workspaceID := strings.TrimSpace(info.Labels[WorkspaceIDLabelKey])
	return workspaceID, workspaceID != ""
}

// BotIDFromContainerID infers a bot ID from a known container naming scheme.
// Only containers created before managed computers had their own ids are named
// after a bot; it is a compatibility fallback when labels are missing.
func BotIDFromContainerID(containerID string) (string, bool) {
	return InstanceKeyFromContainerID(containerID)
}

// BotIDFromContainerInfo resolves the bot ID from container metadata.
// It prefers the bot label and only falls back to name inference.
func BotIDFromContainerInfo(info ctr.ContainerInfo) (string, bool) {
	if botID := strings.TrimSpace(info.Labels[BotLabelKey]); botID != "" {
		return botID, true
	}
	return BotIDFromContainerID(info.ID)
}
