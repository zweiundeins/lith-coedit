package collab

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zweiundeins/lith/events"
	"github.com/zweiundeins/lith/writer"

	"github.com/zweiundeins/tickit/internal/access"
	"github.com/zweiundeins/tickit/internal/doc"
	"github.com/zweiundeins/tickit/internal/tabs"
)

// Answer is a commit's answer (docs/editor.md, "Wire format").
type Answer byte

const (
	Applied  Answer = 'a' // 204: applied, or applied before
	Stale    Answer = 's' // 409: another epoch, the subject is gone, or base is older than the fold
	Refused  Answer = 'r' // 422: a rule refused it; the editor's row says why
	Readonly Answer = 'o' // 403: the actor may not edit the subject
)

// Commit applies an editor's commit to a subject's document: it maps its
// steps over the commits since Base (doc.Rebase), checks the result
// (doc.Rules, then Check) and appends it with events.Exactly(head). A
// resent commit applies once: its ref and client among the commits since
// its base answer Applied, so it needs no op id. Its result is an
// Answer; a refusal by a rule goes into the editing tab's row (the
// editors table's refusal) for its view.
type Commit struct {
	Kind         *Kind
	By           tabs.By
	Session, Tab string
	ID           string
	Epoch        string
	Base         int64
	Ref          string
	Client       string
	Steps        json.RawMessage

	// Gate checks the subject inside the writer before anything else:
	// ErrGone or access.ErrNotFound answer Stale, a denial Readonly.
	Gate func(tx *writer.Tx) error
	// Check refuses a document beyond the rules, with a message for the
	// person; "" lets it through. Optional.
	Check func(head, next doc.Doc) string

	// Took is how long the writer spent on it, for the measurements.
	Took time.Duration

	steps []doc.Step
	bad   error
	ready bool
}

// Validate parses the steps outside the writer: ParseCommit is the
// costly part that needs no transaction.
func (c *Commit) Validate() error {
	if c.By.Actor.IsZero() {
		return errors.New("collab: a commit needs an actor")
	}
	if c.Kind == nil || !ValidID(c.ID) || c.Ref == "" || len(c.Ref) > 64 || c.Base < 0 || len(c.Client) > 64 {
		return errors.New("collab: a malformed commit")
	}
	steps, err := doc.ParseCommit(c.Steps)
	var re *doc.RuleError
	switch {
	case errors.As(err, &re):
		c.bad = re
	case err != nil:
		return fmt.Errorf("collab: malformed steps: %w", err)
	}
	c.steps, c.ready = steps, true
	return nil
}

func (c *Commit) Apply(tx *writer.Tx) error {
	_, err := c.ApplyResult(tx)
	return err
}

func (c *Commit) ApplyResult(tx *writer.Tx) ([]byte, error) {
	if !c.ready {
		if err := c.Validate(); err != nil {
			return nil, err
		}
	}
	start := time.Now()
	defer func() { c.Took = time.Since(start) }()
	var refusal string
	err := tx.Savepoint(func(tx *writer.Tx) error {
		var err error
		refusal, err = c.apply(tx)
		return err
	})
	var denial interface{ Denied() bool }
	switch {
	case errors.Is(err, ErrStale) || errors.Is(err, ErrGone) || errors.Is(err, access.ErrNotFound):
		return []byte{byte(Stale)}, nil
	case errors.As(err, &denial) && denial.Denied():
		return []byte{byte(Readonly)}, nil
	case err != nil:
		return nil, err
	case refusal != "":
		b, _ := json.Marshal(map[string]string{"ref": c.Ref, "message": refusal})
		if _, err := tx.Exec(c.Kind.sql.refusal, string(b), c.Session, c.Tab, c.ID); err != nil {
			return nil, err
		}
		tx.Touch(c.Kind.EditorTopic(c.Tab))
		return []byte{byte(Refused)}, nil
	}
	return []byte{byte(Applied)}, nil
}

// apply runs the commit; a refusal by a rule comes back as its message,
// with nothing changed.
func (c *Commit) apply(tx *writer.Tx) (string, error) {
	ctx := tx.Context()
	k := c.Kind
	if c.Gate != nil {
		if err := c.Gate(tx); err != nil {
			return "", err
		}
	}
	ds, err := k.State(tx, c.ID)
	if err != nil {
		return "", err
	}
	if ds.Epoch != c.Epoch {
		return "", ErrStale // MP-13.2: never across a reset
	}
	if c.Base > ds.Version {
		return "", ErrStale // a version the document never had: the editor starts again
	}
	if c.bad != nil {
		return c.bad.Error(), nil
	}
	since, first, err := k.loadCommits(ctx, tx, c.ID, ds.Epoch, c.Base, ds.Version, false)
	if err != nil {
		return "", err
	}
	if c.Base < ds.Version && first != c.Base+1 {
		return "", ErrStale // folded away: the editor starts again
	}
	var mapped []doc.Step
	for _, s := range since {
		if s.Snapshot != nil {
			return "", ErrStale
		}
		if s.Ref == c.Ref && s.Client == c.Client {
			return "", nil // resent: it applied already, after its base (NFR-43: no op id kept per commit)
		}
		mapped = append(mapped, s.Steps...)
	}
	head, err := k.docAt(ctx, tx, c.ID, ds.Epoch, ds.Version, ds.Ref, false)
	if err != nil {
		return "", err
	}
	next, applied, err := doc.Rebase(head, mapped, c.steps)
	if err != nil {
		return "This change no longer fits the " + k.cfg.Noun + ".", nil
	}
	if msg := k.check(head, next, c.Check); msg != "" {
		return msg, nil
	}
	raw, err := doc.MarshalSteps(applied)
	if err != nil {
		return "", err
	}
	if err := k.append(tx, c.By, c.ID, ds, Committed{Ref: c.Ref, Client: c.Client, Base: c.Base, Steps: raw}, applied, next); err != nil {
		return "", err
	}
	return "", nil
}

// check runs the Kind's rules and the subject's own check.
func (k *Kind) check(head, next doc.Doc, own func(head, next doc.Doc) string) string {
	if err := k.cfg.Rules.Check(head, next); err != nil {
		var re *doc.RuleError
		if errors.As(err, &re) {
			return re.Msg
		}
		return "This change does not fit the " + k.cfg.Noun + "."
	}
	if own != nil {
		return own(head, next)
	}
	return ""
}

// append appends a commit at the state's head, with the document it
// makes for the cache.
func (k *Kind) append(tx *writer.Tx, by tabs.By, id string, ds State, e Committed, applied []doc.Step, next doc.Doc) error {
	v := ds.Version + 1
	docs.put(docKey{k.cfg.Name, id, ds.Epoch, v, e.Ref}, next)
	m := by.Meta()
	if m.At.IsZero() {
		m.At = time.Now()
	}
	if err := events.Execute(k.store(), tx, CommitDecider, k.Stream.ID(id), events.Exactly(ds.Version), e, m); err != nil {
		return err
	}
	rec, key := commitRec{Ref: e.Ref, Client: e.Client, Base: e.Base, Steps: applied, Raw: e.Steps}, commitKey{k.cfg.Name, id, ds.Epoch, v}
	tx.AfterCommit(func() { commits.put(key, rec) })
	return nil
}

// CommitWhole replaces a subject's document with d in one commit made on
// the head: the one step of doc.RestoreStep, so that open editors keep
// their places outside what changed. The client is "" (no tab); ref
// names it. A document the rules or check refuse is refused on field. It
// reports whether it committed: an equal document commits nothing.
func (k *Kind) CommitWhole(tx *writer.Tx, by tabs.By, id string, d doc.Doc, ref, field string, check func(head, next doc.Doc) string) (bool, error) {
	ctx := tx.Context()
	ds, err := k.State(tx, id)
	if err != nil {
		return false, err
	}
	head, err := k.docAt(ctx, tx, id, ds.Epoch, ds.Version, ds.Ref, false)
	if err != nil {
		return false, err
	}
	step := doc.RestoreStep(head, d)
	if step == nil {
		return false, nil
	}
	next, err := step.Apply(head)
	if err != nil {
		return false, err
	}
	if msg := k.check(head, next, check); msg != "" {
		return false, tabs.Refuse(field, msg)
	}
	raw, err := doc.MarshalSteps([]doc.Step{step})
	if err != nil {
		return false, err
	}
	return true, k.append(tx, by, id, ds, Committed{Ref: ref, Base: ds.Version, Steps: raw}, []doc.Step{step}, next)
}

// EditorsSince reports who committed to a subject's document after version
// after, in the order they first did.
func (k *Kind) EditorsSince(tx *writer.Tx, id string, after int64) ([]string, error) {
	rows, err := tx.Query(`SELECT actor FROM lith_events WHERE stream = ? AND stream_id = ? AND version > ? AND actor != ''
		GROUP BY actor ORDER BY min(position)`, k.cfg.Name, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
