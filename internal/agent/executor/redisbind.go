package executor

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var bindLineRe = regexp.MustCompile(`(?m)^bind\s+.*$`)

// SetRedisBindAll opens /etc/redis/redis.conf, replaces the bind directive
// to listen on all interfaces, and restarts the redis service. Compatible
// with Redis 4.0+; does not rely on runtime CONFIG SET support for bind.
//
// The install script sets redis.conf to 660 (redis:redis) and adds the
// netbox-agent user to the redis group, so no sudo is needed for the file write.
// The systemctl restart uses the existing sudoers entry for 'redis'.
func SetRedisBindAll() (string, error) {
	const confPath = "/etc/redis/redis.conf"

	data, err := os.ReadFile(confPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", confPath, err)
	}

	content := string(data)
	if !bindLineRe.MatchString(content) {
		return "", fmt.Errorf("no bind directive found in %s", confPath)
	}

	updated := bindLineRe.ReplaceAllString(content, "bind 0.0.0.0 -::*")
	if updated == content {
		// Already set to the target value — still restart to ensure it's applied.
	}

	if err := os.WriteFile(confPath, []byte(updated), 0660); err != nil {
		return "", fmt.Errorf("write %s: %w", confPath, err)
	}

	cmd := exec.Command("sudo", "systemctl", "restart", "redis")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("restart redis: %w", err)
	}

	return "redis.conf bind updated to 0.0.0.0 and service restarted", nil
}
