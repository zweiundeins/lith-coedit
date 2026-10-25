package collab

import (
	"cmp"
	"time"

	"github.com/zweiundeins/lith/events"
	"github.com/zweiundeins/lith/writer"
)

// The fold: the commits of a document up to a version are folded
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
	return events.Compact(k.store(), tx, k.Snapshot, events.Retention{Since: since, Limit: cmp.Or(limit, FoldBatch)}, k.peekDecider,
		func(s State) Snapshot { return Snapshot{Epoch: s.Epoch, Doc: marshal(s.doc)} })
}

// Cut records that a version of the subject holds its document up to
// version upto: collab_uncut (README), which the subject's fold
// reads for its bound, then has the first commit after it, if any.
func (k *Kind) Cut(tx *writer.Tx, id string, upto int64) error {
	if _, err := tx.Exec(`DELETE FROM collab_uncut WHERE kind = ? AND id = ?`, k.cfg.Name, id); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO collab_uncut (kind, id, at) SELECT stream, stream_id, at FROM lith_events
		WHERE stream = ? AND stream_id = ? AND version = ?`, k.cfg.Name, id, upto+1)
	return err
}

// Mark records that the subject has a change no version holds yet, made
// at at, unless an older one holds the fold back already.
func (k *Kind) Mark(tx *writer.Tx, id string, at time.Time) error {
	_, err := tx.Exec(`INSERT OR IGNORE INTO collab_uncut (kind, id, at) VALUES (?, ?, ?)`, k.cfg.Name, id, at.UnixMilli())
	return err
}

// Release records that the subject is gone (trashed, hidden, deleted):
// its commits that no version holds hold the fold back no more.
func (k *Kind) Release(tx *writer.Tx, id string) error {
	_, err := tx.Exec(`DELETE FROM collab_uncut WHERE kind = ? AND id = ?`, k.cfg.Name, id)
	return err
}

// Hold undoes Release for a subject that is back, whose last version
// holds its document up to version upto: the oldest of its events after
// upto that the fold left, if any, holds the fold back again.
func (k *Kind) Hold(tx *writer.Tx, id string, upto int64) error {
	_, err := tx.Exec(`INSERT OR IGNORE INTO collab_uncut (kind, id, at) SELECT stream, stream_id, at FROM lith_events
		WHERE stream = ? AND stream_id = ? AND version > ? ORDER BY version LIMIT 1`, k.cfg.Name, id, upto)
	return err
}
