package collab_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zweiundeins/lith/events"
	"github.com/zweiundeins/lith/sqlite"
	"github.com/zweiundeins/lith/writer"
	"github.com/zweiundeins/prosemirror-go"

	"github.com/zweiundeins/coedit/collab"
)

var schema = prosemirror.MustSchema([]byte(`{"nodes": [
	{"name": "doc", "content": "block+"},
	{"name": "paragraph", "group": "block", "content": "inline*"},
	{"name": "photo", "group": "block", "atom": true, "attrs": {"id": {"validate": "string"}}},
	{"name": "text", "group": "inline"}
], "marks": [{"name": "strong"}]}`))

var store *events.Store

// notes is a Kind as an app defines one: its rules look up the photos of
// the app's own table in the commit's transaction.
var notes = collab.NewKind(collab.Config{
	Name: "notedoc", Noun: "note", Schema: schema, Store: func() *events.Store { return store },
	Head: "note_doc", Editors: "note_editors", Key: "note_id",
	EditorTopic: func(tab string) string { return "editor:" + tab },
	CaretsTopic: func(id string) string { return "carets:" + id },
	Rules: func(tx *writer.Tx, actor, id string) prosemirror.Rules {
		return prosemirror.Rules{Noun: "note", Nodes: map[string]func(n, parent *prosemirror.Node) error{
			"photo": func(n, _ *prosemirror.Node) error {
				var one int
				if tx == nil || tx.QueryRow(`SELECT 1 FROM photos WHERE id = ?`, n.AttrString("id")).Scan(&one) != nil {
					return prosemirror.Refuse("A note shows the trip's own photos.")
				}
				return nil
			},
		}}
	},
})

var projector = notes.Projector("notes", 1, nil)

type env struct {
	t  *testing.T
	db *sqlite.DB
	w  *writer.Writer
}

func open(t *testing.T) *env {
	t.Helper()
	ddl, err := os.ReadFile("testdata/001_notes.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), sqlite.Options{
		Migrations: fstest.MapFS{"001_notes.sql": {Data: ddl}},
	})
	if err != nil {
		t.Fatal(err)
	}
	w, err := writer.New(db, writer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	go w.Run(context.Background())
	store = events.Must(events.New(projector))
	r := events.NewRunner(store, db, w, events.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		w.Close()
		db.Close()
		collab.ResetCaches()
	})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		live := true
		for _, p := range r.Stats().Projectors {
			live = live && p.Status == "live"
		}
		if live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("projector not live")
		}
	}
	return &env{t: t, db: db, w: w}
}

func (e *env) do(fn func(tx *writer.Tx) error) error {
	return e.w.Do(context.Background(), writer.Func(fn))
}

func (e *env) must(fn func(tx *writer.Tx) error) {
	e.t.Helper()
	if err := e.do(fn); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) commit(c *collab.Commit) collab.Answer {
	e.t.Helper()
	if c.Meta.Actor == "" {
		c.Meta.Actor = "ada"
	}
	c.Kind, c.Session, c.Tab = notes, "s1", "t1"
	if c.Epoch == "" {
		c.Epoch = "e0"
	}
	if c.Client == "" {
		c.Client = "0123456789ab"
	}
	if err := c.Validate(); err != nil {
		e.t.Fatal(err)
	}
	var res []byte
	e.must(func(tx *writer.Tx) error {
		var err error
		res, err = c.ApplyResult(tx)
		return err
	})
	return collab.Answer(res[0])
}

func (e *env) head(id string) collab.Head {
	e.t.Helper()
	h, err := notes.Head(context.Background(), e.db.R, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}

func insert(at int, s string) string {
	return `[{"stepType":"replace","from":` + strconv.Itoa(at) + `,"to":` + strconv.Itoa(at) + `,"slice":{"content":[{"type":"text","text":"` + s + `"}]}}]`
}

type denial struct{}

func (denial) Error() string { return "may not edit" }
func (denial) Denied() bool  { return true }

func TestCommits(t *testing.T) {
	e := open(t)
	d, _ := schema.ParseDoc([]byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Hello"}]}]}`))
	e.must(func(tx *writer.Tx) error {
		_, err := notes.Create(tx, "n1", d, events.Meta{Actor: "ada"})
		return err
	})
	e.must(func(tx *writer.Tx) error {
		return notes.Open(tx, collab.Open{Session: "s1", Tab: "t1", ID: "n1", Person: "ada", Base: 1, Client: "0123456789ab", At: time.Now()})
	})
	if a := e.commit(&collab.Commit{ID: "n1", Base: 1, Ref: "a", Steps: []byte(insert(6, " world"))}); a != collab.Applied {
		t.Fatalf("first commit: %c", a)
	}
	// Made on version 1 like the first: rebased over it.
	if a := e.commit(&collab.Commit{ID: "n1", Base: 1, Ref: "b", Client: "ba9876543210", Steps: []byte(insert(6, "!"))}); a != collab.Applied {
		t.Fatalf("stale commit: %c", a)
	}
	h := e.head("n1")
	if got := h.Doc.TextBetween(0, h.Doc.Content().Size(), "", nil); got != "Hello world!" || h.Version != 3 {
		t.Fatalf("head %q at %d", got, h.Version)
	}
	// Resent: applied once.
	if a := e.commit(&collab.Commit{ID: "n1", Base: 1, Ref: "b", Client: "ba9876543210", Steps: []byte(insert(6, "!"))}); a != collab.Applied || e.head("n1").Version != 3 {
		t.Fatalf("resent: %c", a)
	}
	for name, c := range map[string]*collab.Commit{
		"another epoch":  {ID: "n1", Epoch: "e9", Base: 3, Ref: "c", Steps: []byte(insert(1, "x"))},
		"a future base":  {ID: "n1", Base: 9, Ref: "c", Steps: []byte(insert(1, "x"))},
		"a gone subject": {ID: "n1", Base: 3, Ref: "c", Steps: []byte(insert(1, "x")), Gate: func(*writer.Tx) error { return collab.ErrGone }},
		"no document":    {ID: "n2", Base: 1, Ref: "c", Steps: []byte(insert(1, "x"))},
	} {
		if a := e.commit(c); a != collab.Stale {
			t.Errorf("%s: %c", name, a)
		}
	}
	if a := e.commit(&collab.Commit{ID: "n1", Base: 3, Ref: "c", Steps: []byte(insert(1, "x")), Gate: func(*writer.Tx) error { return denial{} }}); a != collab.Readonly {
		t.Errorf("denied: %c", a)
	}
	// A rule with the app's data: the photo is not the app's, then it is.
	photo := []byte(`[{"stepType":"replace","from":14,"to":14,"slice":{"content":[{"type":"photo","attrs":{"id":"p1"}}]}}]`)
	if a := e.commit(&collab.Commit{ID: "n1", Base: 3, Ref: "d", Steps: photo}); a != collab.Refused {
		t.Fatalf("a photo of no trip: %c", a)
	}
	row, err := notes.Editor(context.Background(), e.db.R, "t1", "ada")
	if err != nil || !strings.Contains(row.Refusal, `"ref":"d"`) || !strings.Contains(row.Refusal, "the trip's own photos") {
		t.Fatalf("refusal %q %v", row.Refusal, err)
	}
	e.must(func(tx *writer.Tx) error { _, err := tx.Exec(`INSERT INTO photos (id) VALUES ('p1')`); return err })
	if a := e.commit(&collab.Commit{ID: "n1", Base: 3, Ref: "e", Steps: photo}); a != collab.Applied {
		t.Fatalf("the trip's photo: %c", a)
	}
	// The frame's batches after version 1.
	list, ok, err := notes.Batches(context.Background(), e.db.R, "n1", "e0", 1, 4)
	if err != nil || !ok || len(list) != 3 || list[0].Ref != "a" || list[1].Ref != "b" || list[2].V != 4 {
		t.Fatalf("batches %+v %v %v", list, ok, err)
	}
	if who, err := e.editorsSince(1); err != nil || strings.Join(who, ",") != "ada" {
		t.Errorf("editors since 1: %v %v", who, err)
	}
	// A whole document on the head, as one step; refused by a rule as a
	// *writer.Refusal.
	bye, _ := schema.ParseDoc([]byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Bye"}]}]}`))
	e.must(func(tx *writer.Tx) error {
		ok, err := notes.CommitWhole(tx, events.Meta{Actor: "ada"}, "n1", bye, "w1", nil)
		if !ok {
			t.Error("CommitWhole committed nothing")
		}
		return err
	})
	bad, _ := schema.ParseDoc([]byte(`{"type":"doc","content":[{"type":"photo","attrs":{"id":"p9"}}]}`))
	err = e.do(func(tx *writer.Tx) error {
		_, err := notes.CommitWhole(tx, events.Meta{Actor: "ada"}, "n1", bad, "w2", nil)
		return err
	})
	var refusal *writer.Refusal
	if !errors.As(err, &refusal) || refusal.Msg != "A note shows the trip's own photos." {
		t.Errorf("whole refused: %v", err)
	}
	collab.ResetCaches()
	if h := e.head("n1"); h.Version != 5 || h.Doc.TextBetween(0, h.Doc.Content().Size(), "", nil) != "Bye" {
		t.Errorf("head from the table %d %v", h.Version, h.Doc)
	}
}

func (e *env) editorsSince(v int64) (out []string, err error) {
	err = e.do(func(tx *writer.Tx) error {
		out, err = notes.EditorsSince(tx, "n1", v)
		return err
	})
	return out, err
}

// Presence moves a tab's caret; Close takes the row away.
func TestEditors(t *testing.T) {
	e := open(t)
	d, _ := schema.ParseDoc([]byte(`{"type":"doc","content":[{"type":"paragraph"}]}`))
	e.must(func(tx *writer.Tx) error { _, err := notes.Create(tx, "n1", d, events.Meta{Actor: "ada"}); return err })
	e.must(func(tx *writer.Tx) error {
		return notes.Open(tx, collab.Open{Session: "s1", Tab: "t1", ID: "n1", Person: "ada", Base: 1, Client: collab.Client("t1"), At: time.Now()})
	})
	for _, seq := range []int64{2, 1} { // the older post changes nothing
		p := collab.Presence{Kind: notes, Session: "s1", Tab: "t1", ID: "n1", Epoch: "e0", Ack: 1, V: 1, Anchor: int(seq), Head: int(seq), Seq: seq, At: time.Now()}
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
		e.must(p.Apply)
	}
	rows, err := notes.Editors(context.Background(), e.db.R, "n1")
	if err != nil || len(rows) != 1 || rows[0].Anchor != 2 || rows[0].Client != collab.Client("t1") {
		t.Fatalf("editors %+v %v", rows, err)
	}
	e.must(func(tx *writer.Tx) error {
		id, err := notes.Close(tx, "s1", "t1")
		if id != "n1" {
			t.Errorf("closed %q", id)
		}
		return err
	})
	if _, err := notes.Editor(context.Background(), e.db.R, "t1", "ada"); !errors.Is(err, collab.ErrNotFound) {
		t.Errorf("closed row: %v", err)
	}
	if !collab.ValidClient(collab.Client("t1")) || collab.ValidClient("xyz") {
		t.Error("client ids")
	}
}

// The fold replaces the commits up to a cut version by a snapshot: the
// head reads the same, a commit made before it starts again.
func TestFold(t *testing.T) {
	e := open(t)
	d, _ := schema.ParseDoc([]byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"ab"}]}]}`))
	e.must(func(tx *writer.Tx) error { _, err := notes.Create(tx, "n1", d, events.Meta{Actor: "ada"}); return err })
	for i, s := range []string{"x", "y", "z"} {
		if a := e.commit(&collab.Commit{ID: "n1", Base: int64(1 + i), Ref: "r" + s, Steps: []byte(insert(2, s))}); a != collab.Applied {
			t.Fatalf("commit %s: %c", s, a)
		}
	}
	e.must(func(tx *writer.Tx) error { return notes.Cut(tx, "n1", 4) })
	var n int64
	e.must(func(tx *writer.Tx) error {
		var err error
		n, err = notes.Fold(tx, time.Now().Add(time.Minute), 0)
		return err
	})
	if n == 0 {
		t.Fatal("the fold deleted nothing")
	}
	collab.ResetCaches()
	h := e.head("n1")
	if got := h.Doc.TextBetween(0, h.Doc.Content().Size(), "", nil); got != "azyxb" || h.Version != 4 {
		t.Fatalf("head %q at %d", got, h.Version)
	}
	if _, ok, err := notes.Batches(context.Background(), e.db.R, "n1", "e0", 1, 4); ok || err != nil {
		t.Errorf("batches across the fold: %v %v", ok, err)
	}
	if a := e.commit(&collab.Commit{ID: "n1", Base: 1, Ref: "late", Steps: []byte(insert(2, "q"))}); a != collab.Stale {
		t.Errorf("a commit made before the fold: %c", a)
	}
	if a := e.commit(&collab.Commit{ID: "n1", Base: 4, Ref: "now", Steps: []byte(insert(1, "q"))}); a != collab.Applied {
		t.Errorf("a commit on the snapshot: %c", a)
	}
	var cnt int
	e.must(func(tx *writer.Tx) error {
		return tx.QueryRow(`SELECT count(*) FROM lith_events WHERE stream = 'notedoc'`).Scan(&cnt)
	})
	if cnt != 2 {
		t.Errorf("%d events after the fold and a commit", cnt)
	}
}

func TestParseCommit(t *testing.T) {
	big := "[" + strings.Repeat(insert(1, "x")[1:len(insert(1, "x"))-1]+",", collab.MaxCommitSteps) + insert(1, "x")[1:len(insert(1, "x"))-1] + "]"
	var re *prosemirror.RuleError
	if _, err := collab.ParseCommit(schema, []byte(big)); !errors.As(err, &re) {
		t.Errorf("too many steps: %v", err)
	}
	if _, err := collab.ParseCommit(schema, []byte(`[{"stepType":"addNodeMark","pos":0,"mark":{"type":"strong"}}]`)); err == nil || errors.As(err, &re) {
		t.Errorf("a step type the server refuses: %v", err)
	}
}
