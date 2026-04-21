package handlers

import (
	"encoding/json"
	"time"
)

// pgbStanza is the top-level object in `pgbackrest info --output=json` output.
type pgbStanza struct {
	Name   string      `json:"name"`
	Backup []pgbBackup `json:"backup"`
}

type pgbBackup struct {
	Type      string `json:"type"` // "full" | "diff" | "incr"
	Label     string `json:"label"`
	Timestamp struct {
		Start int64 `json:"start"` // Unix seconds
		Stop  int64 `json:"stop"`  // Unix seconds
	} `json:"timestamp"`
}

// parsePGBackRestInfo parses the raw JSON from `pgbackrest info --output=json`
// and returns the oldest backup start time, the newest backup stop time, and
// the flat list of all backup entries across all stanzas.
// Returns nil times if there are no backup entries.
func parsePGBackRestInfo(raw string) (oldest, newest *time.Time, backups []pgbBackup) {
	var stanzas []pgbStanza
	if err := json.Unmarshal([]byte(raw), &stanzas); err != nil {
		return nil, nil, nil
	}
	for _, s := range stanzas {
		for _, b := range s.Backup {
			b := b // capture
			backups = append(backups, b)
			t := time.Unix(b.Timestamp.Start, 0).UTC()
			if oldest == nil || t.Before(*oldest) {
				oldest = &t
			}
			t2 := time.Unix(b.Timestamp.Stop, 0).UTC()
			if newest == nil || t2.After(*newest) {
				newest = &t2
			}
		}
	}
	return oldest, newest, backups
}
