package executor

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// SetRedisRequirepass sets requirepass on the local Redis instance and persists it
// to redis.conf via CONFIG REWRITE. Compatible with Redis 4.0+.
//
// It first tries connecting unauthenticated (covers a fresh Redis install with no
// password set). If that is rejected, it retries with the new password (covers
// idempotent re-runs where requirepass is already set to the same value).
func SetRedisRequirepass(params protocol.RedisRequirepassParams) (string, error) {
	setArgs := []string{"CONFIG", "SET", "requirepass", params.Password}

	cmd := exec.Command("redis-cli", setArgs...)
	out, err := cmd.CombinedOutput()
	outStr := strings.TrimSpace(string(out))

	if err != nil || strings.Contains(outStr, "NOAUTH") || strings.Contains(outStr, "ERR") {
		// Redis already requires authentication — retry with the new password.
		cmd = exec.Command("redis-cli", append([]string{"-a", params.Password}, setArgs...)...)
		out, err = cmd.CombinedOutput()
		outStr = strings.TrimSpace(string(out))
		if err != nil {
			return outStr, fmt.Errorf("CONFIG SET requirepass: %w\n%s", err, outStr)
		}
		if strings.Contains(outStr, "ERR") || strings.Contains(outStr, "WRONGPASS") {
			return outStr, fmt.Errorf("CONFIG SET requirepass failed: %s", outStr)
		}
	}

	// CONFIG REWRITE tells the Redis server process to persist the running config
	// to redis.conf. The agent does not need write access to the file — the Redis
	// process (running as the redis OS user) performs the write itself.
	rw := exec.Command("redis-cli", "-a", params.Password, "CONFIG", "REWRITE")
	if rwOut, rwErr := rw.CombinedOutput(); rwErr != nil {
		return outStr, fmt.Errorf("CONFIG REWRITE: %w\n%s", rwErr, strings.TrimSpace(string(rwOut)))
	}

	return outStr, nil
}
