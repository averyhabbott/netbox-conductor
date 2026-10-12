package executor

import (
	"fmt"
	"os"
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
	// The password goes in via stdin (-x) and REDISCLI_AUTH, never argv, so it
	// doesn't show up in `ps` for other local users.
	out, err := redisCLI("", params.Password, "-x", "CONFIG", "SET", "requirepass").CombinedOutput()
	outStr := strings.TrimSpace(string(out))

	if err != nil || strings.Contains(outStr, "NOAUTH") || strings.Contains(outStr, "ERR") {
		// Redis already requires authentication — retry with the new password.
		out, err = redisCLI(params.Password, params.Password, "-x", "CONFIG", "SET", "requirepass").CombinedOutput()
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
	if rwOut, rwErr := redisCLI(params.Password, "", "CONFIG", "REWRITE").CombinedOutput(); rwErr != nil {
		return outStr, fmt.Errorf("CONFIG REWRITE: %w\n%s", rwErr, strings.TrimSpace(string(rwOut)))
	}

	return outStr, nil
}

// redisCLI builds a redis-cli command that authenticates via REDISCLI_AUTH
// (when auth is set) and feeds stdin (when set), keeping secrets out of argv.
func redisCLI(auth, stdin string, args ...string) *exec.Cmd {
	cmd := exec.Command("redis-cli", args...)
	if auth != "" {
		cmd.Env = append(os.Environ(), "REDISCLI_AUTH="+auth)
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd
}
