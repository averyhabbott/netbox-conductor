package executor

import (
	"context"
	"fmt"
	"os"
	"regexp"

	"github.com/averyhabbott/netbox-conductor/internal/shared/protocol"
)

// hostLineRe matches the DATABASE HOST entry in NetBox's configuration.py.
// NetBox uses single-quoted Python dict literals; this handles any whitespace
// around the colon and any existing value.
var hostLineRe = regexp.MustCompile(`'HOST'\s*:\s*'[^']*'`)

// UpdateDBHost patches only the DATABASE.HOST line in configuration.py and
// optionally restarts NetBox services. All other settings are preserved.
//
// This is dispatched by the conductor when the Patroni primary changes on an
// app_tier_always_available cluster, so every app-tier node reconnects to the
// new writable primary without a full config rewrite.
func UpdateDBHost(ctx context.Context, params protocol.DBHostUpdateParams, configPath string) (string, error) {
	if configPath == "" {
		return "", ErrNoNetbox
	}
	if params.Host == "" {
		return "", fmt.Errorf("host must not be empty")
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", configPath, err)
	}

	original := string(raw)
	if !hostLineRe.MatchString(original) {
		return "", fmt.Errorf("DATABASE 'HOST' line not found in %s — cannot patch", configPath)
	}

	replacement := fmt.Sprintf("'HOST': '%s'", params.Host)
	updated := hostLineRe.ReplaceAllString(original, replacement)

	if updated == original {
		return fmt.Sprintf("DATABASE.HOST already set to %s — no change needed", params.Host), nil
	}

	if err := writeNetboxConfig(ctx, configPath, updated, false); err != nil {
		return "", err
	}

	output := fmt.Sprintf("updated DATABASE.HOST to %s in %s", params.Host, configPath)
	return withNetboxRestart(ctx, output, params.RestartAfter), nil
}
