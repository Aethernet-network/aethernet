package recognition

import (
	"log/slog"
	"time"

	"github.com/Aethernet-network/aethernet/internal/event"
)

// EmitCommit is a convenience function for call sites to emit a commit record
// to the bus. It constructs the CommitRecord and calls bus.Emit. If the bus
// is nil, this is a no-op (allows graceful degradation before wiring).
func EmitCommit(bus *Bus, ev *event.Event, source CommitSource, replay bool) {
	if bus == nil || ev == nil {
		return
	}
	record := CommitRecord{
		EventID:     ev.ID,
		EventType:   ev.Type,
		Source:      source,
		Replay:     replay,
		CommittedAt: time.Now(),
	}
	if err := bus.Emit(record, ev); err != nil {
		slog.Warn("recognition: emit failed",
			"event_id", ev.ID, "type", ev.Type, "err", err)
	} else {
		slog.Info("recognition: commit emitted",
			"event_id", ev.ID, "type", ev.Type, "source", source, "replay", replay)
	}
}
