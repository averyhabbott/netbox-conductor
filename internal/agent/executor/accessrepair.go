package executor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrNoNetbox is returned by NetBox tasks on a node without NetBox.
var ErrNoNetbox = errors.New("NetBox is not on this node (NETBOX_ROOT is empty)")

// accessFixes holds the sudo commands that restore the agent's access to the
// NetBox config and media folders, e.g. after a NetBox upgrade switches the
// root symlink to a new version. install.sh grants exactly these commands in
// sudoers, generated from the same NETBOX_ROOT.
var accessFixes map[string][][]string

// SetNetboxPaths registers the NetBox folders whose access the agent repairs.
// Called once at startup; empty paths (no NetBox on this node) register none.
func SetNetboxPaths(configPath, mediaRoot string) {
	if configPath == "" {
		return
	}
	configDir := filepath.Dir(configPath)
	accessFixes = map[string][][]string{
		configDir: {
			{"/usr/bin/chgrp", "netbox", configDir},
			{"/usr/bin/chmod", "g+ws", configDir},
		},
		mediaRoot: {
			{"/usr/bin/setfacl", "-R", "-m", "u:netbox-agent:rwX,d:u:netbox-agent:rwX,u:netbox:rwX,d:u:netbox:rwX", mediaRoot},
		},
	}
}

var (
	repairMu sync.Mutex
	repaired []string
)

// withAccessRepair runs fn. If it fails with permission denied and dir is one
// of the registered NetBox folders, it restores the folder's access and runs fn
// once more. tried (nil: always) limits callers that write many files to one
// repair attempt.
func withAccessRepair(ctx context.Context, dir string, tried *bool, fn func() error) error {
	err := fn()
	fixes := accessFixes[filepath.Clean(dir)]
	if !errors.Is(err, fs.ErrPermission) || fixes == nil || (tried != nil && *tried) {
		return err
	}
	if tried != nil {
		*tried = true
	}
	for _, args := range fixes {
		if out, ferr := Command(ctx, "sudo", args...).CombinedOutput(); ferr != nil {
			return fmt.Errorf("%w (repairing access with `sudo %s` failed: %s — rerun install.sh)",
				err, strings.Join(args, " "), strings.TrimSpace(string(out)))
		}
	}
	repairMu.Lock()
	repaired = append(repaired, dir)
	repairMu.Unlock()
	if err := fn(); err != nil {
		return fmt.Errorf("still failing after repairing access to %s: %w", dir, err)
	}
	return nil
}

// TakeRepaired returns the folders whose access was repaired since the last
// call, for the next task result to report.
func TakeRepaired() []string {
	repairMu.Lock()
	defer repairMu.Unlock()
	out := repaired
	repaired = nil
	return out
}

// writeNetboxConfig replaces configuration.py (netbox-agent:netbox 0640 via the
// folder's setgid group), optionally keeping the old one as .bak, and repairs
// the folder's access if needed.
func writeNetboxConfig(ctx context.Context, path, content string, backup bool) error {
	return withAccessRepair(ctx, filepath.Dir(path), nil, func() error {
		if backup {
			if _, err := os.Stat(path); err == nil {
				_ = os.Rename(path, path+".bak")
			}
		}
		return writeFileAtomic(path, content, 0640, -1)
	})
}
