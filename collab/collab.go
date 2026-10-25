// Package collab is the server side of co-edited ProseMirror documents on
// lith: the writer orders every edit, rebases a stale commit over the
// commits since its base and keeps the document; no CRDT. A Kind is one
// kind of co-edited document (a page's body, an issue's description), a
// stream kind of its own keyed by the subject's id, whose events are
// created {epoch, doc}, committed {ref, client, base, steps} and snapshot
// {epoch, doc}; its stream version is the collab version the editor
// speaks. Each Kind has a head table (the head's version and ref, the
// document as of a checkpoint) and an editors table (the editing tabs'
// rows), keyed by the subject's id column; the README gives their DDL.
//
// The app keeps what is its own: who may edit and whether the subject is
// still there (Commit.Gate), its rules (Config.Rules), versions and what
// they record, and the views. This package keeps the rest: the commit
// (rebase, rules, events.Exactly), the reads through the shared document
// and commit caches, the editors' rows, the head projector and the fold.
package collab

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zweiundeins/lith/events"
	"github.com/zweiundeins/lith/writer"
	"github.com/zweiundeins/prosemirror-go"
)

// Event is an event of a document stream.
type Event interface{ collabEvent() }

// Created starts a document: its epoch and first document.
type Created struct {
	Epoch string          `json:"epoch"`
	Doc   json.RawMessage `json:"doc"`
}

// Committed is a commit as the writer applied it: its random ref, the
// tab that made it (Client, as others see the tab), the version it was
// made on, and its steps after the rebase. Steps may be empty: a commit
// whose steps all dropped still confirms its ref.
type Committed struct {
	Ref    string          `json:"ref"`
	Client string          `json:"client,omitempty"`
	Base   int64           `json:"base"`
	Steps  json.RawMessage `json:"steps"`
}

// Snapshot is the document as of the commits the fold replaced:
// Fold writes it in place of the last of them.
type Snapshot struct {
	Epoch string          `json:"epoch"`
	Doc   json.RawMessage `json:"doc"`
}

func (Created) collabEvent()   {}
func (Committed) collabEvent() {}
func (Snapshot) collabEvent()  {}

// State is what a commit decides on: the stream's epoch, its version and
// the ref of its last commit. It never holds the document, which can be
// hundreds of kilobytes: the document cache and the head table do. Only
// a fold from the events, as Fold's (events.Compact), carries the
// document along.
type State struct {
	Exists  bool   `json:"exists"`
	Epoch   string `json:"epoch"`
	Version int64  `json:"version"`
	Ref     string `json:"ref,omitempty"`

	doc prosemirror.Doc // in a fold from the events only
}

// Folded is the document a fold from the events carried along; nil in a
// state read from its row.
func (s State) Folded() prosemirror.Doc { return s.doc }

const stateFold = 1

// Evolve folds an event into a document stream's state.
func (k *Kind) Evolve(s State, e Event) State {
	s.Version++
	switch e := e.(type) {
	case Created:
		s.Exists, s.Epoch, s.Ref = true, e.Epoch, ""
		s.doc, _ = k.cfg.Schema.ParseDoc(e.Doc)
	case Snapshot:
		s.Exists, s.Epoch, s.Ref = true, e.Epoch, ""
		s.doc, _ = k.cfg.Schema.ParseDoc(e.Doc)
	case Committed:
		s.Ref = e.Ref
		if s.doc != nil {
			if steps, err := k.cfg.Schema.ParseSteps(e.Steps); err == nil {
				s.doc, _ = prosemirror.Apply(s.doc, steps)
			} else {
				s.doc = nil
			}
		}
	}
	return s
}

// peek is a command that decides nothing: its decider hands out the
// stream's state.
type peek struct{ out *State }

// ErrStale is a document state the reader or the editor has to start
// again from: its version was folded away, or the subject was reset.
var ErrStale = errors.New("collab: the document moved on")

// ErrGone is what a commit's gate answers for a subject that is gone (a
// page in the trash, a deleted issue): its editors stop.
var ErrGone = errors.New("collab: the subject is gone")

// ErrNotFound is wrapped by State and Editor for a subject without a
// document and a tab without a row, unless Config.NotFound names the
// app's own error.
var ErrNotFound = errors.New("collab: not found")

// Config is what makes a Kind.
type Config struct {
	// Name is the stream kind, and the prefix of its event types'
	// names: "pagedoc" stores "pagedoc.created". Permanent once events
	// are stored.
	Name string
	// Noun names the document in refusals: "page", "description".
	Noun string
	// Schema is the documents' schema.
	Schema *prosemirror.Schema
	// Store is the event store its commands append with.
	Store func() *events.Store
	// Head and Editors are its tables; Key is their column of the
	// subject's id.
	Head, Editors, Key string
	// EditorTopic is touched by every change to one editing tab's row,
	// CaretsTopic by every presence post of a subject's editors.
	EditorTopic func(tab string) string
	CaretsTopic func(id string) string
	// Rules are the app's rules for a commit by actor to the subject id,
	// in tx: rules that look up the app's data (whether an image was
	// uploaded, whether the actor may name an id) do so in the commit's
	// transaction. Optional: the schema alone.
	Rules func(tx *writer.Tx, actor, id string) prosemirror.Rules
	// Epoch names the epoch of a new document stream. An app that resets
	// its data and seeds documents again with the same ids bumps it, so
	// that no edit made before a reset applies after it. Optional: "e0".
	Epoch func(tx *writer.Tx) (string, error)
	// NotFound is the app's error for what does not exist, wrapped by
	// State and Editor, and taken from a gate as ErrNotFound is.
	// Optional: ErrNotFound.
	NotFound error
	// ValidID reports whether s is a subject's id. Optional: 1 to 64
	// letters, digits, '-' and '_'.
	ValidID func(s string) bool
	// OnCommit runs in the command of every commit, after its event:
	// what the subject keeps beside the document follows its steps
	// (the anchors of inline comments). Optional.
	OnCommit func(tx *writer.Tx, id string, steps []prosemirror.Step, next prosemirror.Doc) error
}

// Kind is a kind of co-edited document. Make one per subject at package
// level with NewKind.
type Kind struct {
	cfg       Config
	Stream    *events.Stream[string, Event]
	Created   events.Type[string, Event, Created]
	Committed events.Type[string, Event, Committed]
	Snapshot  events.Type[string, Event, Snapshot]
	// CreateDecider starts a document stream; CommitDecider appends a
	// commit to an existing one.
	CreateDecider events.Decider[Created, Event, State]
	CommitDecider events.Decider[Committed, Event, State]
	peekDecider   events.Decider[peek, Event, State]
	sql           queries
}

// NewKind defines a Kind's stream and event types.
func NewKind(cfg Config) *Kind {
	if cfg.Schema == nil {
		panic("collab: a Kind needs a schema")
	}
	if cfg.NotFound == nil {
		cfg.NotFound = ErrNotFound
	}
	if cfg.ValidID == nil {
		cfg.ValidID = validID
	}
	s := events.NewStream[string, Event](cfg.Name)
	k := &Kind{
		cfg:       cfg,
		Stream:    s,
		Created:   events.Define(s, cfg.Name+".created", func(e Created) Event { return e }),
		Committed: events.Define(s, cfg.Name+".committed", func(e Committed) Event { return e }),
		Snapshot:  events.Define(s, cfg.Name+".snapshot", func(e Snapshot) Event { return e }),
		sql:       queriesOf(cfg),
	}
	k.CreateDecider = events.Decider[Created, Event, State]{Evolve: k.Evolve, Version: stateFold, Decide: func(c Created, s State) ([]Event, error) {
		if s.Exists {
			return nil, errors.New("collab: the document exists")
		}
		return []Event{c}, nil
	}}
	k.CommitDecider = events.Decider[Committed, Event, State]{Evolve: k.Evolve, Version: stateFold, Decide: func(c Committed, s State) ([]Event, error) {
		if !s.Exists {
			return nil, ErrStale
		}
		return []Event{c}, nil
	}}
	k.peekDecider = events.Decider[peek, Event, State]{Evolve: k.Evolve, Version: stateFold, Decide: func(c peek, s State) ([]Event, error) {
		*c.out = s
		return nil, nil
	}}
	return k
}

func validID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range []byte(s) {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Name is the Kind's stream kind.
func (k *Kind) Name() string { return k.cfg.Name }

// Schema is the documents' schema.
func (k *Kind) Schema() *prosemirror.Schema { return k.cfg.Schema }

// ValidID reports whether s is a subject's id, as Config.ValidID says.
func (k *Kind) ValidID(s string) bool { return k.cfg.ValidID(s) }

// Topic is touched by every commit to a subject's document: its reader
// view and its editors' views.
func (k *Kind) Topic(id string) string { return k.cfg.Name + ":" + id }

// EditorTopic and CaretsTopic are the Config's.
func (k *Kind) EditorTopic(tab string) string { return k.cfg.EditorTopic(tab) }
func (k *Kind) CaretsTopic(id string) string  { return k.cfg.CaretsTopic(id) }

// Rules are the Kind's rules for a commit by actor to id in tx.
func (k *Kind) Rules(tx *writer.Tx, actor, id string) prosemirror.Rules {
	if k.cfg.Rules == nil {
		return prosemirror.Rules{Noun: k.cfg.Noun}
	}
	return k.cfg.Rules(tx, actor, id)
}

func (k *Kind) store() *events.Store { return k.cfg.Store() }

// State reads a subject's document state inside a command; an error that
// is Config.NotFound when it has none.
func (k *Kind) State(tx *writer.Tx, id string) (State, error) {
	var s State
	if err := events.Execute(k.store(), tx, k.peekDecider, k.Stream.ID(id), events.Any, peek{&s}, events.Meta{}); err != nil {
		return s, err
	}
	if !s.Exists {
		return s, fmt.Errorf("document %s: %w", id, k.cfg.NotFound)
	}
	return s, nil
}

// Create starts a subject's document with d, in the epoch now; it
// returns the document as stored.
func (k *Kind) Create(tx *writer.Tx, id string, d prosemirror.Doc, m events.Meta) (json.RawMessage, error) {
	raw, _ := prosemirror.MarshalDoc(d)
	epoch := "e0"
	if k.cfg.Epoch != nil {
		var err error
		if epoch, err = k.cfg.Epoch(tx); err != nil {
			return nil, err
		}
	}
	if err := events.Execute(k.store(), tx, k.CreateDecider, k.Stream.ID(id), events.NoStream, Created{Epoch: epoch, Doc: raw}, m); err != nil {
		return nil, err
	}
	return raw, nil
}

func marshal(d prosemirror.Doc) json.RawMessage {
	b, _ := prosemirror.MarshalDoc(d)
	return b
}

// queries are a Kind's SQL texts, built once from its table names: each
// Kind runs a fixed set of texts, as constants would.
type queries struct {
	headRow, headRowCheckpoint, headCheckpoint, insertHead, setCheckpoint, setHead string
	openEditor, presence, closeEditor, editor, editors, sweep                      string
	refusal                                                                        string
}

func queriesOf(c Config) queries {
	h, e, k := c.Head, c.Editors, c.Key
	return queries{
		headRow:           `SELECT epoch, version, ref, last_at FROM ` + h + ` WHERE ` + k + ` = ?`,
		headRowCheckpoint: `SELECT epoch, version, ref, checkpoint FROM ` + h + ` WHERE ` + k + ` = ?`,
		headCheckpoint:    `SELECT epoch, checkpoint, doc FROM ` + h + ` WHERE ` + k + ` = ?`,
		insertHead: `INSERT INTO ` + h + ` (` + k + `, epoch, version, ref, checkpoint, doc, bytes, blocks, last_at) VALUES (?, ?, ?, '', ?, ?, ?, ?, ?)
			ON CONFLICT (` + k + `) DO UPDATE SET epoch = excluded.epoch, version = excluded.version, ref = '', checkpoint = excluded.checkpoint,
			doc = excluded.doc, bytes = excluded.bytes, blocks = excluded.blocks, last_at = excluded.last_at`,
		setCheckpoint: `UPDATE ` + h + ` SET checkpoint = ?, doc = ? WHERE ` + k + ` = ?`,
		setHead:       `UPDATE ` + h + ` SET version = ?, ref = ?, bytes = ?, blocks = ?, last_at = ? WHERE ` + k + ` = ?`,
		openEditor: `INSERT INTO ` + e + ` (sid, tab_id, ` + k + `, person_id, client, epoch, ack, v, moved_at, seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (sid, tab_id) DO UPDATE SET ` + k + ` = excluded.` + k + `, person_id = excluded.person_id, epoch = excluded.epoch,
			client = excluded.client, ack = CASE WHEN ` + e + `.` + k + ` = excluded.` + k + ` AND ` + e + `.epoch = excluded.epoch THEN max(` + e + `.ack, excluded.ack) ELSE excluded.ack END,
			v = excluded.v, refusal = '', seen_at = excluded.seen_at`,
		presence: `UPDATE ` + e + ` SET ack = max(ack, ?), v = ?, anchor = ?, head = ?, seq = ?, seen_at = ?,
		moved_at = CASE WHEN anchor != ? OR head != ? OR v != ? THEN ? ELSE moved_at END
		WHERE sid = ? AND tab_id = ? AND ` + k + ` = ? AND epoch = ? AND seq < ?`,
		closeEditor: `DELETE FROM ` + e + ` WHERE sid = ? AND tab_id = ? RETURNING ` + k,
		editor: `SELECT tab_id, ` + k + `, person_id, client, epoch, ack, v, anchor, head, refusal, moved_at
		FROM ` + e + ` WHERE tab_id = ? AND person_id = ?`,
		editors: `SELECT tab_id, ` + k + `, person_id, client, epoch, ack, v, anchor, head, moved_at
		FROM ` + e + ` WHERE ` + k + ` = ? ORDER BY moved_at`,
		sweep:   `DELETE FROM ` + e + ` WHERE seen_at < ?`,
		refusal: `UPDATE ` + e + ` SET refusal = ? WHERE sid = ? AND tab_id = ? AND ` + k + ` = ?`,
	}
}
