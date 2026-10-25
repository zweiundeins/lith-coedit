package collab

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/zweiundeins/lith/events"
	"github.com/zweiundeins/lith/writer"
	"github.com/zweiundeins/prosemirror-go"
)

// Answer is a commit's answer (README, "The protocol").
type Answer byte

const (
	Applied  Answer = 'a' // 204: applied, or applied before
	Stale    Answer = 's' // 409: another epoch, the subject is gone, or base is older than the fold
	Refused  Answer = 'r' // 422: a rule refused it; the editor's row says why
	Readonly Answer = 'o' // 403: the actor may not edit the subject
)

// Status is the HTTP status a commit's route answers with.
func (a Answer) Status() int {
	switch a {
	case Applied:
		return http.StatusNoContent
	case Stale:
		return http.StatusConflict
	case Refused:
		return http.StatusUnprocessableEntity
	case Readonly:
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}

// Commit applies an editor's commit to a subject's document: it maps its
// steps over the commits since Base (prosemirror.Rebase), checks the result
// (the Kind's Rules, then Check) and appends it with events.Exactly(head). A
// resent commit applies once: its ref and client among the commits since
// its base answer Applied, so it needs no op id. Its result is an
// Answer; a refusal by a rule goes into the editing tab's row (the
// editors table's refusal) for its view.
type Commit struct {
	Kind *Kind
	// Meta is the commit's event metadata; its Actor is who commits.
	Meta         events.Meta
	Session, Tab string
	ID           string
	Epoch        string
	Base         int64
	Ref          string
	Client       string
	Steps        json.RawMessage

	// Gate is the app's check, inside the writer before anything else,
	// that the actor may edit the subject now: ErrGone, ErrNotFound or
	// Config.NotFound answer Stale, an error with a Denied() bool method
	// that says true answers Readonly, any other fails the commit.
	Gate func(tx *writer.Tx) error
	// Check refuses a document beyond the rules, with a message for the
	// person; "" lets it through. Optional.
	Check func(head, next prosemirror.Doc) string

	// Took is how long the writer spent on it, for the measurements.
	Took time.Duration

	steps []prosemirror.Step
	bad   error
	ready bool
}

// Commit limits beyond the rules.
const (
	MaxCommitSteps = 500
	MaxCommitBytes = 256 << 10
)

// ParseCommit reads the steps of a commit: at most MaxCommitSteps steps
// in MaxCommitBytes; a larger one is a *prosemirror.RuleError.
func ParseCommit(s *prosemirror.Schema, data []byte) ([]prosemirror.Step, error) {
	if len(data) > MaxCommitBytes {
		return nil, &prosemirror.RuleError{Msg: "This change is too large to save at once."}
	}
	steps, err := s.ParseSteps(data)
	if err != nil {
		return nil, err
	}
	if len(steps) > MaxCommitSteps {
		return nil, &prosemirror.RuleError{Msg: "This change has too many steps to save at once."}
	}
	return steps, nil
}

// Validate parses the steps outside the writer: ParseCommit is the
// costly part that needs no transaction.
func (c *Commit) Validate() error {
	if c.Meta.Actor == "" {
		return errors.New("collab: a commit needs an actor")
	}
	if c.Kind == nil || !c.Kind.cfg.ValidID(c.ID) || c.Ref == "" || len(c.Ref) > 64 || c.Base < 0 || len(c.Client) > 64 {
		return errors.New("collab: a malformed commit")
	}
	steps, err := ParseCommit(c.Kind.cfg.Schema, c.Steps)
	var re *prosemirror.RuleError
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
	case errors.Is(err, ErrStale) || errors.Is(err, ErrGone) || errors.Is(err, ErrNotFound) || errors.Is(err, c.Kind.cfg.NotFound):
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
		return "", ErrStale // never across a reset
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
	var mapped []prosemirror.Step
	for _, s := range since {
		if s.Snapshot != nil {
			return "", ErrStale
		}
		if s.Ref == c.Ref && s.Client == c.Client {
			return "", nil // resent: it applied already, after its base (no op id is kept per commit)
		}
		mapped = append(mapped, s.Steps...)
	}
	head, err := k.docAt(ctx, tx, c.ID, ds.Epoch, ds.Version, ds.Ref, false)
	if err != nil {
		return "", err
	}
	next, applied, err := prosemirror.Rebase(head, mapped, c.steps)
	if err != nil {
		return "This change no longer fits the " + k.cfg.Noun + ".", nil
	}
	if msg := k.check(tx, c.Meta.Actor, c.ID, head, next, c.Check); msg != "" {
		return msg, nil
	}
	raw, err := prosemirror.MarshalSteps(applied)
	if err != nil {
		return "", err
	}
	if err := k.append(tx, c.Meta, c.ID, ds, Committed{Ref: c.Ref, Client: c.Client, Base: c.Base, Steps: raw}, applied, next); err != nil {
		return "", err
	}
	return "", nil
}

// check runs the Kind's rules and the subject's own check.
func (k *Kind) check(tx *writer.Tx, actor, id string, head, next prosemirror.Doc, own func(head, next prosemirror.Doc) string) string {
	if err := k.Rules(tx, actor, id).Check(head, next); err != nil {
		var re *prosemirror.RuleError
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
func (k *Kind) append(tx *writer.Tx, m events.Meta, id string, ds State, e Committed, applied []prosemirror.Step, next prosemirror.Doc) error {
	v := ds.Version + 1
	docs.put(docKey{k.cfg.Name, id, ds.Epoch, v, e.Ref}, next)
	if m.At.IsZero() {
		m.At = time.Now()
	}
	if err := events.Execute(k.store(), tx, k.CommitDecider, k.Stream.ID(id), events.Exactly(ds.Version), e, m); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO collab_uncut (kind, id, at) VALUES (?, ?, ?)`, k.cfg.Name, id, m.At.UnixMilli()); err != nil {
		return err
	}
	if k.cfg.OnCommit != nil {
		if err := k.cfg.OnCommit(tx, id, applied, next); err != nil {
			return err
		}
	}
	rec, key := commitRec{Ref: e.Ref, Client: e.Client, Base: e.Base, Steps: applied, Raw: e.Steps}, commitKey{k.cfg.Name, id, ds.Epoch, v}
	tx.AfterCommit(func() { commits.put(key, rec) })
	return nil
}

// CommitWhole replaces a subject's document with d in one commit made on
// the head: the one step of prosemirror.RestoreStep, so that open editors
// keep their places outside what changed. The client is "" (no tab); ref
// names it. A document the rules or check refuse is a *writer.Refusal
// with the message. It reports whether it committed: an equal document
// commits nothing.
func (k *Kind) CommitWhole(tx *writer.Tx, m events.Meta, id string, d prosemirror.Doc, ref string, check func(head, next prosemirror.Doc) string) (bool, error) {
	ctx := tx.Context()
	ds, err := k.State(tx, id)
	if err != nil {
		return false, err
	}
	head, err := k.docAt(ctx, tx, id, ds.Epoch, ds.Version, ds.Ref, false)
	if err != nil {
		return false, err
	}
	step := prosemirror.RestoreStep(head, d)
	if step == nil {
		return false, nil
	}
	next, err := step.Apply(head)
	if err != nil {
		return false, err
	}
	if msg := k.check(tx, m.Actor, id, head, next, check); msg != "" {
		return false, &writer.Refusal{Msg: msg}
	}
	raw, err := prosemirror.MarshalSteps([]prosemirror.Step{step})
	if err != nil {
		return false, err
	}
	return true, k.append(tx, m, id, ds, Committed{Ref: ref, Base: ds.Version, Steps: raw}, []prosemirror.Step{step}, next)
}

// StepsSince are the steps of a subject's commits after version base up
// to the state's head, in a command: what a position made at base maps
// through. ErrStale when base is newer than the head or the commits
// after it were folded away.
func (k *Kind) StepsSince(tx *writer.Tx, id string, s State, base int64) ([]prosemirror.Step, error) {
	if base > s.Version || base < 0 {
		return nil, ErrStale
	}
	since, first, err := k.loadCommits(tx.Context(), tx, id, s.Epoch, base, s.Version, false)
	if err != nil {
		return nil, err
	}
	if base < s.Version && first != base+1 {
		return nil, ErrStale
	}
	var out []prosemirror.Step
	for _, c := range since {
		if c.Snapshot != nil {
			return nil, ErrStale
		}
		out = append(out, c.Steps...)
	}
	return out, nil
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
