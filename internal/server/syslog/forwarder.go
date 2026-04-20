package syslog

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/events"
)

// Forwarder implements events.Sink by routing events to configured syslog destinations.
type Forwarder struct {
	destQ *queries.SyslogDestinationQuerier

	mu    sync.RWMutex
	dests []*Destination
}

// NewForwarder creates a Forwarder. Call Start to begin destination management.
func NewForwarder(destQ *queries.SyslogDestinationQuerier) *Forwarder {
	return &Forwarder{destQ: destQ}
}

// OnEvent implements events.Sink. The event is forwarded to all matching destinations.
func (f *Forwarder) OnEvent(ev events.Event) {
	f.mu.RLock()
	dests := f.dests
	f.mu.RUnlock()

	for _, d := range dests {
		if !matches(d.cfg, ev) {
			continue
		}
		msg := Format(ev)
		go d.Send(msg)
	}
}

// Start loads destinations and begins the periodic reload loop.
func (f *Forwarder) Start(ctx context.Context) {
	f.reload(ctx)
	go f.reloadLoop(ctx)
}

func (f *Forwarder) reloadLoop(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			f.closeAll()
			return
		case <-ticker.C:
			f.reload(ctx)
		}
	}
}

func (f *Forwarder) reload(ctx context.Context) {
	cfgs, err := f.destQ.ListEnabled(ctx)
	if err != nil {
		slog.Warn("syslog: failed to reload destinations", "error", err)
		return
	}

	// Build new destination set; reuse existing connections where the config matches.
	f.mu.RLock()
	old := f.dests
	f.mu.RUnlock()

	oldByID := make(map[string]*Destination, len(old))
	for _, d := range old {
		oldByID[d.cfg.ID.String()] = d
	}

	newDests := make([]*Destination, 0, len(cfgs))
	for _, cfg := range cfgs {
		if existing, ok := oldByID[cfg.ID.String()]; ok {
			existing.cfg = cfg // update config in place
			newDests = append(newDests, existing)
			delete(oldByID, cfg.ID.String())
		} else {
			newDests = append(newDests, NewDestination(cfg))
		}
	}
	// Close removed destinations
	for _, d := range oldByID {
		d.Close()
	}

	f.mu.Lock()
	f.dests = newDests
	f.mu.Unlock()
	slog.Debug("syslog: destinations reloaded", "count", len(newDests))
}

func (f *Forwarder) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.dests {
		d.Close()
	}
}

// matches returns true if the destination is configured to receive this event.
func matches(cfg queries.SyslogDestination, ev events.Event) bool {
	if len(cfg.Categories) > 0 {
		found := false
		for _, c := range cfg.Categories {
			if c == ev.Category {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if cfg.MinSeverity != "" {
		evRank := events.SeverityOrder[ev.Severity]
		minRank := events.SeverityOrder[cfg.MinSeverity]
		if evRank < minRank {
			return false
		}
	}
	return true
}
