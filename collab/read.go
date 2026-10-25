package collab

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zweiundeins/lith/events"
	"github.com/zweiundeins/lith/sqlite"

	"github.com/zweiundeins/prosemirror-go"
)

// loadCommits reads a subject's commits after version after up to upto,
// the snapshot that a fold left among them included (its Snapshot set),
// through the commit cache. A render's or a GET's reads may fill the
// cache: they see committed events only. A command's may not, as its
// batch could still fail; it passes fill false.
func (k *Kind) loadCommits(ctx context.Context, q sqlite.Querier, id, epoch string, after, upto int64, fill bool) ([]commitRec, int64, error) {
	out := make([]commitRec, 0, upto-after)
	first := after + 1
	for v := after + 1; v <= upto; v++ {
		c, ok := commits.get(commitKey{k.cfg.Name, id, epoch, v})
		if !ok {
			break
		}
		out = append(out, c)
	}
	if int64(len(out)) == upto-after {
		return out, first, nil
	}
	from := after + int64(len(out))
	n := 0
	for r, err := range events.LoadUntil(ctx, k.store(), q, k.Stream.ID(id), from, 1<<62) {
		if err != nil {
			return nil, 0, err
		}
		if r.Version > upto {
			break
		}
		if n == 0 && len(out) == 0 {
			first = r.Version
		}
		n++
		var c commitRec
		switch e := r.Data.(type) {
		case Committed:
			steps, err := k.cfg.Schema.ParseSteps(e.Steps)
			if err != nil {
				return nil, 0, fmt.Errorf("collab: %s %s version %d: %w", k.cfg.Name, id, r.Version, err)
			}
			c = commitRec{Ref: e.Ref, Client: e.Client, Base: e.Base, Steps: steps, Raw: e.Steps}
		case Snapshot:
			d, err := k.cfg.Schema.ParseDoc(e.Doc)
			if err != nil {
				return nil, 0, err
			}
			c = commitRec{Snapshot: d}
		case Created:
			d, err := k.cfg.Schema.ParseDoc(e.Doc)
			if err != nil {
				return nil, 0, err
			}
			c = commitRec{Snapshot: d}
		}
		if fill && c.Snapshot == nil {
			commits.put(commitKey{k.cfg.Name, id, epoch, r.Version}, c)
		}
		out = append(out, c)
	}
	return out, first, nil
}

// docAt returns a subject's document at version, whose last commit has
// ref, through the cache: from the cache, or from the head table's
// checkpoint and the commits since.
func (k *Kind) docAt(ctx context.Context, q sqlite.Querier, id, epoch string, version int64, ref string, fill bool) (prosemirror.Doc, error) {
	if d, ok := docs.get(docKey{k.cfg.Name, id, epoch, version, ref}); ok {
		return d, nil
	}
	var rowEpoch, raw string
	var checkpoint int64
	err := q.QueryRowContext(ctx, k.sql.headCheckpoint, id).Scan(&rowEpoch, &checkpoint, &raw)
	if err != nil {
		return nil, err
	}
	if rowEpoch != epoch || checkpoint > version {
		return nil, ErrStale
	}
	d, err := k.cfg.Schema.ParseDoc([]byte(raw))
	if err != nil {
		return nil, err
	}
	if checkpoint < version {
		list, _, err := k.loadCommits(ctx, q, id, epoch, checkpoint, version, fill)
		if err != nil {
			return nil, err
		}
		for _, c := range list {
			if c.Snapshot != nil {
				d = c.Snapshot
				continue
			}
			if d, err = prosemirror.Apply(d, c.Steps); err != nil {
				return nil, err
			}
		}
	}
	docs.put(docKey{k.cfg.Name, id, epoch, version, ref}, d)
	return d, nil
}

// StateDoc is the document at a state's head, in a command (it fills no
// cache with what the command's batch could still take back).
func (k *Kind) StateDoc(ctx context.Context, q sqlite.Querier, id string, s State) (prosemirror.Doc, error) {
	return k.docAt(ctx, q, id, s.Epoch, s.Version, s.Ref, false)
}

// Head is a subject's document as it is now, from the cache: for the
// reader view, the versions and the jobs.
type Head struct {
	ID      string
	Epoch   string
	Version int64
	Ref     string
	Doc     prosemirror.Doc
	LastAt  int64
}

// Head reads a subject's head document; sql.ErrNoRows when it has none.
func (k *Kind) Head(ctx context.Context, q sqlite.Querier, id string) (Head, error) {
	h := Head{ID: id}
	err := q.QueryRowContext(ctx, k.sql.headRow, id).Scan(&h.Epoch, &h.Version, &h.Ref, &h.LastAt)
	if err != nil {
		return h, err
	}
	h.Doc, err = k.docAt(ctx, q, id, h.Epoch, h.Version, h.Ref, true)
	return h, err
}

// Batch is a commit as an editor's frame carries it (README).
type Batch struct {
	V      int64           `json:"v"`
	Ref    string          `json:"ref"`
	Client string          `json:"client"`
	Steps  json.RawMessage `json:"steps"`
}

// MaxBatches is how many commits a frame carries at most.
const MaxBatches = 500

// Batches are a subject's commits after version after, up to head and at
// most MaxBatches, for an editor's frame. ok is false when the commits
// right after it were folded away: the editor starts again ("gap").
func (k *Kind) Batches(ctx context.Context, q sqlite.Querier, id, epoch string, after, head int64) (list []Batch, ok bool, err error) {
	if after >= head {
		return nil, true, nil
	}
	upto := min(head, after+MaxBatches)
	recs, first, err := k.loadCommits(ctx, q, id, epoch, after, upto, true)
	if err != nil {
		return nil, false, err
	}
	if first != after+1 {
		return nil, false, nil
	}
	list = make([]Batch, 0, len(recs))
	for i, c := range recs {
		if c.Snapshot != nil {
			return nil, false, nil
		}
		raw := c.Raw
		if len(raw) == 0 {
			raw = json.RawMessage(`[]`)
		}
		list = append(list, Batch{V: after + 1 + int64(i), Ref: c.Ref, Client: c.Client, Steps: raw})
	}
	return list, true, nil
}
