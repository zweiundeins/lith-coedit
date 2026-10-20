// Package collab is the server side of co-edited documents
// (docs/editor.md), for any subject that has one: a page's body and an
// issue's description are two Kinds of it. A Kind is a stream kind of
// its own, keyed by the subject's id ("pagedoc", "issuedoc"), whose
// events are created {epoch, doc}, committed {ref, client, base, steps}
// and snapshot {epoch, doc}; its stream version is the collab version
// the editor speaks. Each Kind has a head table (the head's version and
// ref, the document as of a checkpoint) and an editors table (the
// editing tabs' rows), both of the shape of migration 030's page_doc and
// page_editors, keyed by the subject's id column.
//
// The subject's package keeps what is its own: who may edit, when the
// subject is gone, versions and what they record (page.version_cut,
// issue.description_changed), and the views. This package keeps the
// rest: the commit (rebase, rules, events.Exactly), the reads through
// the shared document and commit caches, the editors' rows, the head
// projector and the fold (NFR-43).
package collab

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/google/uuid"
	"github.com/zweiundeins/lith/events"
	"github.com/zweiundeins/lith/writer"

	"github.com/zweiundeins/tickit/internal/access"
	"github.com/zweiundeins/tickit/internal/demo/seed"
	"github.com/zweiundeins/tickit/internal/doc"
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

// Snapshot is the document as of the commits the fold replaced (NFR-43):
// Fold writes it in place of the last of them.
type Snapshot struct {
	Epoch string          `json:"epoch"`
	Doc   json.RawMessage `json:"doc"`
}

func (Created) collabEvent()   {}
func (Committed) collabEvent() {}
func (Snapshot) collabEvent()  {}

// State is what a commit decides on: the stream's epoch, its version and
// the ref of its last commit. It never holds the document, which is up
// to 200 KB: the document cache and the head table do. Only a fold from
// the events, as Fold's (events.Compact), carries the document along.
type State struct {
	Exists  bool   `json:"exists"`
	Epoch   string `json:"epoch"`
	Version int64  `json:"version"`
	Ref     string `json:"ref,omitempty"`

	doc doc.Doc // in a fold from the events only
}

// Folded is the document a fold from the events carried along; nil in a
// state read from its row.
func (s State) Folded() doc.Doc { return s.doc }

const stateFold = 1

// Evolve folds an event into a document stream's state.
func Evolve(s State, e Event) State {
	s.Version++
	switch e := e.(type) {
	case Created:
		s.Exists, s.Epoch, s.Ref = true, e.Epoch, ""
		s.doc, _ = doc.ParseDoc(e.Doc)
	case Snapshot:
		s.Exists, s.Epoch, s.Ref = true, e.Epoch, ""
		s.doc, _ = doc.ParseDoc(e.Doc)
	case Committed:
		s.Ref = e.Ref
		if s.doc != nil {
			if steps, err := doc.ParseSteps(e.Steps); err == nil {
				s.doc, _ = doc.Apply(s.doc, steps)
			} else {
				s.doc = nil
			}
		}
	}
	return s
}

func decider[C any](decide func(C, State) ([]Event, error)) events.Decider[C, Event, State] {
	return events.Decider[C, Event, State]{Decide: decide, Evolve: Evolve, Version: stateFold}
}

// peek is a command that decides nothing: its decider hands out the
// stream's state.
type peek struct{ out *State }

var peekDecider = events.Decider[peek, Event, State]{
	Decide: func(c peek, s State) ([]Event, error) {
		*c.out = s
		return nil, nil
	},
	Evolve:  Evolve,
	Version: stateFold,
}

// CreateDecider starts a document stream.
var CreateDecider = decider(func(c Created, s State) ([]Event, error) {
	if s.Exists {
		return nil, errors.New("collab: the document exists")
	}
	return []Event{c}, nil
})

// CommitDecider appends a commit to an existing document.
var CommitDecider = decider(func(c Committed, s State) ([]Event, error) {
	if !s.Exists {
		return nil, ErrStale
	}
	return []Event{c}, nil
})

// ErrStale is a document state the reader or the editor has to start
// again from: its version was folded away, or the subject was reset.
var ErrStale = errors.New("collab: the document moved on")

// ErrGone is what a Kind's gate answers for a subject that is gone (a
// page in the trash, a deleted or hidden issue): its editors stop.
var ErrGone = errors.New("collab: the subject is gone")

// Config is what makes a Kind.
type Config struct {
	// Name is the stream kind, and the prefix of its event types'
	// names: "pagedoc" stores "pagedoc.created". Permanent once events
	// are stored.
	Name string
	// Noun names the document in refusals: "page", "description".
	Noun string
	// Store is the event store its commands append with.
	Store func() *events.Store
	// Head and Editors are its tables; Key is their column of the
	// subject's id.
	Head, Editors, Key string
	// EditorTopic is touched by every change to one editing tab's row,
	// CaretsTopic by every presence post of a subject's editors.
	EditorTopic func(tab string) string
	CaretsTopic func(id string) string
	// Rules are the server's rules beyond the schema; Image is the
	// library's when nil.
	Rules doc.Rules
}

// Kind is a kind of co-edited document. Make one per subject at package
// level with NewKind.
type Kind struct {
	cfg       Config
	Stream    *events.Stream[string, Event]
	Created   events.Type[string, Event, Created]
	Committed events.Type[string, Event, Committed]
	Snapshot  events.Type[string, Event, Snapshot]
	sql       queries
}

// NewKind defines a Kind's stream and event types.
func NewKind(cfg Config) *Kind {
	if cfg.Rules.Image == nil {
		cfg.Rules.Image = ImageExists
	}
	s := events.NewStream[string, Event](cfg.Name)
	return &Kind{
		cfg:       cfg,
		Stream:    s,
		Created:   events.Define(s, cfg.Name+".created", func(e Created) Event { return e }),
		Committed: events.Define(s, cfg.Name+".committed", func(e Committed) Event { return e }),
		Snapshot:  events.Define(s, cfg.Name+".snapshot", func(e Snapshot) Event { return e }),
		sql:       queriesOf(cfg),
	}
}

// Name is the Kind's stream kind.
func (k *Kind) Name() string { return k.cfg.Name }

// Topic is touched by every commit to a subject's document: its reader
// view and its editors' views.
func (k *Kind) Topic(id string) string { return k.cfg.Name + ":" + id }

// EditorTopic and CaretsTopic are the Config's.
func (k *Kind) EditorTopic(tab string) string { return k.cfg.EditorTopic(tab) }
func (k *Kind) CaretsTopic(id string) string  { return k.cfg.CaretsTopic(id) }

// Rules are the Kind's rules beyond the schema.
func (k *Kind) Rules() doc.Rules { return k.cfg.Rules }

func (k *Kind) store() *events.Store { return k.cfg.Store() }

// State reads a subject's document state inside a command;
// access.ErrNotFound when it has none.
func (k *Kind) State(tx *writer.Tx, id string) (State, error) {
	var s State
	if err := events.Execute(k.store(), tx, peekDecider, k.Stream.ID(id), events.Any, peek{&s}, events.Meta{}); err != nil {
		return s, err
	}
	if !s.Exists {
		return s, fmt.Errorf("document %s: %w", id, access.ErrNotFound)
	}
	return s, nil
}

// Create starts a subject's document with d, in the epoch now; it
// returns the document as stored.
func (k *Kind) Create(tx *writer.Tx, id string, d doc.Doc, m events.Meta) (json.RawMessage, error) {
	raw := Marshal(d)
	epoch, err := NewEpoch(tx)
	if err != nil {
		return nil, err
	}
	if err := events.Execute(k.store(), tx, CreateDecider, k.Stream.ID(id), events.NoStream, Created{Epoch: epoch, Doc: raw}, m); err != nil {
		return nil, err
	}
	return raw, nil
}

// NewEpoch is the epoch of a new document stream: the demo's epoch, which
// every reset bumps (demo.Epoch reads the same row; this package cannot
// import demo, which seeds), so that no edit made before a reset applies
// after it to the subject seeded again with its id (MP-13.2). The seed
// stays the same at every run (DM-02.4).
func NewEpoch(tx *writer.Tx) (string, error) {
	var n int64
	err := tx.QueryRow(`SELECT epoch FROM demo_state WHERE id = 1`).Scan(&n)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return "e" + strconv.FormatInt(n, 10), nil
}

// Blank is the document of a new blank page or an empty description.
const Blank = `{"type":"doc","content":[{"type":"paragraph"}]}`

// Marshal is a document's JSON as internal/doc writes it.
func Marshal(d doc.Doc) json.RawMessage {
	b, _ := doc.MarshalDoc(d)
	return b
}

// Canonical is a document's JSON as internal/doc writes it: the stored
// event's JSON escapes <, > and &, the event in a live command does not,
// and a rebuild must write what the live projection wrote.
func Canonical(raw json.RawMessage) string {
	d, err := doc.ParseDoc(raw)
	if err != nil {
		return string(raw)
	}
	return string(Marshal(d))
}

// IsBlank reports whether raw is empty, null or the blank document.
func IsBlank(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) == 0 || string(raw) == "null" || Canonical(raw) == Blank
}

// ValidID reports whether s is a UUID in its canonical form, as every
// subject's id is.
func ValidID(s string) bool {
	u, err := uuid.Parse(s)
	return err == nil && u.String() == s
}

// The image library (FR-84), from the seed's images.json.
var library = sync.OnceValue(func() map[string]seed.Image {
	m := map[string]seed.Image{}
	c, err := seed.Load()
	if err != nil {
		return m
	}
	for _, im := range c.Images {
		m[im.ID] = im
	}
	return m
})

// Images are the library's images in the seed's order.
func Images() []seed.Image {
	c, err := seed.Load()
	if err != nil {
		return nil
	}
	return c.Images
}

// ImageExists reports whether id is in the image library.
func ImageExists(id string) bool { _, ok := library()[id]; return ok }

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
