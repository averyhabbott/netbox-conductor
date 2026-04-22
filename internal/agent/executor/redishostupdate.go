package executor

import (
	"fmt"
	"os"
	"regexp"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// redisHostRe matches a 'HOST': '...' entry inside the REDIS dict. It is
// intentionally broad — callers are responsible for only applying it within
// the REDIS block, not the DATABASE block.
var redisHostRe = regexp.MustCompile(`('HOST'\s*:\s*)'[^']*'`)

// UpdateRedisHost patches the Redis HOST entries in the REDIS dict of
// configuration.py and optionally restarts NetBox services. All other
// settings are preserved.
//
// This is dispatched by the conductor when the Patroni primary changes on an
// active/standby cluster with app_tier_always_available=true, so every
// app-tier node redirects its Redis connection to the new primary without a
// full config rewrite.
func UpdateRedisHost(params protocol.RedisHostUpdateParams, configPath string) (string, error) {
	if configPath == "" {
		return "", fmt.Errorf("NETBOX_CONFIG_PATH is not configured")
	}
	if params.Host == "" {
		return "", fmt.Errorf("host must not be empty")
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", configPath, err)
	}
	content := string(raw)

	start, end := findRedisBlockBounds(content)
	if start == -1 {
		return "", fmt.Errorf("REDIS block not found in %s — re-run Configure Failover to regenerate the config", configPath)
	}

	redisBlock := content[start:end]
	if !redisHostRe.MatchString(redisBlock) {
		return "", fmt.Errorf("no HOST entries found in REDIS block in %s — re-run Configure Failover to regenerate the config", configPath)
	}

	replacement := fmt.Sprintf("${1}'%s'", params.Host)
	newBlock := redisHostRe.ReplaceAllString(redisBlock, replacement)

	if newBlock == redisBlock {
		return fmt.Sprintf("Redis HOST already set to %s — no change needed", params.Host), nil
	}

	updated := content[:start] + newBlock + content[end:]
	if err := os.WriteFile(configPath, []byte(updated), 0640); err != nil {
		return "", fmt.Errorf("writing %s: %w", configPath, err)
	}

	output := fmt.Sprintf("updated Redis HOST to %s in %s", params.Host, configPath)

	if params.RestartAfter {
		restartOut, err := restartNetbox()
		output += "\n" + restartOut
		if err != nil {
			output += "\nwarn: restart failed: " + err.Error()
		}
	}

	return output, nil
}

// findRedisBlockBounds returns the [start, end) byte range of "REDIS = { ... }"
// using brace counting. Returns -1, -1 if not found.
func findRedisBlockBounds(src string) (start, end int) {
	const marker = "REDIS = {"
	idx := -1
	for i := 0; i+len(marker) <= len(src); i++ {
		if src[i:i+len(marker)] == marker {
			idx = i
			break
		}
	}
	if idx == -1 {
		return -1, -1
	}
	depth := 0
	for i := idx; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return idx, i + 1
			}
		}
	}
	return -1, -1
}
