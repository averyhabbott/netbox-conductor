package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/cron"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/filesync"
	"github.com/averyhabbott/netbox-conductor/internal/server/hub"
)

// FileSyncScheduler starts each cluster's media and extra-folder syncs on its
// file_sync_cron when its schedule is on (UTC, same format as the backup schedule). Backup folders
// aren't scheduled here: they sync after every backup.
type FileSyncScheduler struct {
	clusters *queries.ClusterQuerier
	nodes    *queries.NodeQuerier
	hub      *hub.Hub
	fileSync *filesync.Manager
}

func NewFileSyncScheduler(clusters *queries.ClusterQuerier, nodes *queries.NodeQuerier, h *hub.Hub, fs *filesync.Manager) *FileSyncScheduler {
	return &FileSyncScheduler{clusters: clusters, nodes: nodes, hub: h, fileSync: fs}
}

func (s *FileSyncScheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx, time.Now().UTC())
		}
	}
}

func (s *FileSyncScheduler) tick(ctx context.Context, now time.Time) {
	clusters, err := s.clusters.List(ctx)
	if err != nil {
		slog.Warn("filesyncscheduler: listing clusters", "error", err)
		return
	}
	for i := range clusters {
		c := &clusters[i]
		if !c.MediaSyncEnabled || !c.FileSyncScheduleEnabled || !cron.Matches(c.FileSyncCron, now) {
			continue
		}
		nodes, err := s.nodes.ListByCluster(ctx, c.ID)
		if err != nil {
			slog.Warn("filesyncscheduler: listing nodes", "cluster", c.Name, "error", err)
			continue
		}
		jobs, err := filesync.MediaJobs(c, nodes, s.hub.IsConnected)
		if err != nil {
			slog.Warn("filesyncscheduler: scheduled sync skipped", "cluster", c.Name, "reason", err)
			continue
		}
		for _, j := range jobs {
			if err := s.fileSync.Start(j, false); err != nil {
				slog.Info("filesyncscheduler: scheduled sync skipped", "cluster", c.Name, "folder", j.Name, "reason", err)
			}
		}
	}
}
