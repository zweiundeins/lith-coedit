package collab

import (
	"cmp"
	"time"

	"github.com/zweiundeins/lith/events"
	"github.com/zweiundeins/lith/writer"
)

// The fold (NFR-43): the commits of a document up to a version are folded
// into a snapshot FoldAfter after the version is cut, at most FoldBatch
// events a command.
const (
	FoldAfter = 15 * time.Minute
	FoldBatch = 200
)

// Fold folds a Kind's commits stored before since into snapshots, with
// lith's events.Compact: each document stream that loses commits keeps
// the last of them, rewritten as a snapshot of the document then, at most
// limit events (FoldBatch when 0). The subject's package picks since so
// that no commit after its subject's last version is folded. It reports
// how many events it deleted.
func (k *Kind) Fold(tx *writer.Tx, since time.Time, limit int) (int64, error) {
	return events.Compact(k.store(), tx, k.Snapshot, events.Retention{Since: since, Limit: cmp.Or(limit, FoldBatch)}, peekDecider,
		func(s State) Snapshot { return Snapshot{Epoch: s.Epoch, Doc: Marshal(s.doc)} })
}
