package collab

import (
	"github.com/zweiundeins/lith/events"
	"github.com/zweiundeins/lith/writer"

	"github.com/zweiundeins/tickit/internal/doc"
)

// Checkpoint is how many commits the head table's document may lag
// behind the head: a reader that misses the cache applies at most that
// many.
const Checkpoint = 50

// Projector keeps a Kind's head table: the head's version and ref, with
// the document as of a checkpoint. changed, when set, sees every new head
// document that may refer to something new (created, snapshot, a commit
// with steps), for the subject's own tables, which tables names.
func (k *Kind) Projector(name string, version int, changed func(tx *writer.Tx, id string, d doc.Doc) error, tables ...string) *events.Projector[string, Event] {
	hook := func(tx *writer.Tx, id string, d doc.Doc) error {
		if changed == nil {
			return nil
		}
		return changed(tx, id, d)
	}
	start := func(tx *writer.Tx, id, epoch string, version int64, raw []byte, at int64) (doc.Doc, error) {
		d, err := doc.ParseDoc(raw)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(k.sql.insertHead, id, epoch, version, version, string(Marshal(d)), d.JSONLen(), d.Blocks(), at); err != nil {
			return nil, err
		}
		docs.put(docKey{k.cfg.Name, id, epoch, version, ""}, d)
		return d, nil
	}
	return events.NewProjector(k.Stream, name, version,
		events.Handle(k.Created, func(tx *writer.Tx, r events.Record[string, Created]) error {
			d, err := start(tx, r.ID, r.Data.Epoch, r.Version, r.Data.Doc, r.At.UnixMilli())
			if err != nil {
				return err
			}
			tx.Touch(k.Topic(r.ID))
			return hook(tx, r.ID, d)
		}),
		events.Handle(k.Snapshot, func(tx *writer.Tx, r events.Record[string, Snapshot]) error {
			d, err := start(tx, r.ID, r.Data.Epoch, r.Version, r.Data.Doc, r.At.UnixMilli())
			if err != nil {
				return err
			}
			return hook(tx, r.ID, d)
		}),
		events.Handle(k.Committed, func(tx *writer.Tx, r events.Record[string, Committed]) error {
			var epoch, ref string
			var version, checkpoint int64
			if err := tx.QueryRow(k.sql.headRowCheckpoint, r.ID).Scan(&epoch, &version, &ref, &checkpoint); err != nil {
				return err
			}
			d, ok := docs.get(docKey{k.cfg.Name, r.ID, epoch, r.Version, r.Data.Ref})
			if !ok {
				// A rebuild, or a cache that let it go: the document before,
				// and this commit's steps.
				before, err := k.docAt(tx.Context(), tx, r.ID, epoch, version, ref, false)
				if err != nil {
					return err
				}
				steps, err := doc.ParseSteps(r.Data.Steps)
				if err != nil {
					return err
				}
				if d, err = doc.Apply(before, steps); err != nil {
					return err
				}
				docs.put(docKey{k.cfg.Name, r.ID, epoch, r.Version, r.Data.Ref}, d)
			}
			if r.Version-checkpoint >= Checkpoint {
				if _, err := tx.Exec(k.sql.setCheckpoint, r.Version, string(Marshal(d)), r.ID); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(k.sql.setHead, r.Version, r.Data.Ref, d.JSONLen(), d.Blocks(), r.At.UnixMilli(), r.ID); err != nil {
				return err
			}
			tx.Touch(k.Topic(r.ID))
			if len(r.Data.Steps) <= 2 { // [] : nothing changed
				return nil
			}
			return hook(tx, r.ID, d)
		}),
	).Tables(append([]string{k.cfg.Head}, tables...)...)
}
