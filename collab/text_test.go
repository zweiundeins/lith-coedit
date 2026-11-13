package collab_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zweiundeins/lith/events"
	"github.com/zweiundeins/lith/writer"

	"github.com/zweiundeins/lith-coedit/collab"
)

// runs is a Kind of plain-text documents, edited by whole texts.
var runs = collab.NewKind(collab.Config{
	Name: "rundoc", Noun: "text", Schema: collab.TextSchema, Store: func() *events.Store { return store },
	Head: "run_doc", Editors: "run_editors", Key: "run_id",
	EditorTopic: func(tab string) string { return "editor:" + tab },
	CaretsTopic: func(id string) string { return "carets:" + id },
})

var runProjector = runs.Projector("runs", 1, nil)

func (e *env) text(id, client string, base int64, ref, text string) collab.Answer {
	e.t.Helper()
	return e.commit(&collab.Commit{Kind: runs, ID: id, Base: base, Ref: ref, Client: client, Text: &text})
}

func (e *env) runHead(id string) (string, int64) {
	e.t.Helper()
	h, err := runs.Head(context.Background(), e.db.R, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return collab.TextOf(h.Doc), h.Version
}

// Whole texts made on the same version combine: each is diffed against
// the text at its base and rebased over the commits since, in UTF-16
// positions.
func TestTextCommits(t *testing.T) {
	e := open(t)
	e.must(func(tx *writer.Tx) error {
		_, err := runs.Create(tx, "r1", collab.TextDoc("Grüezi mitenand"), events.Meta{Actor: "ada"})
		return err
	})
	e.must(func(tx *writer.Tx) error {
		return runs.Open(tx, collab.Open{Session: "s1", Tab: "t1", ID: "r1", Person: "ada", Base: 1, Client: "0123456789ab", At: time.Now()})
	})
	for _, c := range []struct {
		client, ref, text string
		base              int64
		want              string
	}{
		{"aaaaaaaaaaaa", "a", "Grüezi alle mitenand", 1, "Grüezi alle mitenand"},
		{"bbbbbbbbbbbb", "b", "Grüezi mitenand 😀", 1, "Grüezi alle mitenand 😀"},
		{"cccccccccccc", "c", "Grüessech mitenand", 1, "Grüessech alle mitenand 😀"},
		{"bbbbbbbbbbbb", "b", "Grüezi mitenand 😀", 1, "Grüessech alle mitenand 😀"}, // resent
		{"dddddddddddd", "d", "Grüessech alle mitenand!", 4, "Grüessech alle mitenand!"},
		{"eeeeeeeeeeee", "e", "😀Grüessech alle mitenand 😀", 4, "😀Grüessech alle mitenand!"},
	} {
		if a := e.text("r1", c.client, c.base, c.ref, c.text); a != collab.Applied {
			t.Fatalf("%s: %c", c.ref, a)
		}
		if got, _ := e.runHead("r1"); got != c.want {
			t.Fatalf("after %s: %q, want %q", c.ref, got, c.want)
		}
	}
	// The same text again changes nothing but is appended, so that its ref
	// confirms it.
	if a := e.text("r1", "aaaaaaaaaaaa", 6, "f", "😀Grüessech alle mitenand!"); a != collab.Applied {
		t.Fatalf("unchanged: %c", a)
	}
	if got, v := e.runHead("r1"); got != "😀Grüessech alle mitenand!" || v != 7 {
		t.Fatalf("unchanged: %q at %d", got, v)
	}
	if a := e.text("r1", "aaaaaaaaaaaa", 7, "g", strings.Repeat("x", collab.MaxCommitBytes+1)); a != collab.Refused {
		t.Fatalf("too large: %c", a)
	}
	// A whole text on the head, as the app writes one.
	e.must(func(tx *writer.Tx) error {
		_, err := runs.CommitWhole(tx, events.Meta{Actor: "agent"}, "r1", collab.TextDoc("Grüezi"), "w1", nil)
		return err
	})
	// From the events and the checkpoint once the caches let go.
	collab.ResetCaches()
	if a := e.text("r1", "aaaaaaaaaaaa", 7, "h", "😀Grüessech alle mitenand!!"); a != collab.Applied {
		t.Fatalf("after the caches: %c", a)
	}
	if got, v := e.runHead("r1"); got != "Grüezi!" || v != 9 {
		t.Fatalf("after the caches: %q at %d", got, v)
	}
}

func TestTextMalformed(t *testing.T) {
	text := "x"
	for name, c := range map[string]*collab.Commit{
		"steps and a text":      {Kind: runs, Steps: []byte(insert(0, "y")), Text: &text},
		"a schema of no text":   {Kind: notes, Text: &text},
		"a text of only blocks": {Kind: notes, Text: new(string)},
	} {
		c.Meta.Actor, c.ID, c.Ref = "ada", "r1", "a"
		if err := c.Validate(); err == nil {
			t.Errorf("%s: valid", name)
		}
	}
}

// A text made on a version before the head table's checkpoint is stale
// once the caches let go: the editor starts again from the head.
func TestTextBeforeCheckpoint(t *testing.T) {
	e := open(t)
	e.must(func(tx *writer.Tx) error {
		_, err := runs.Create(tx, "r1", collab.TextDoc(""), events.Meta{Actor: "ada"})
		return err
	})
	// At version v the text is v-1 letters.
	last := int64(collab.Checkpoint + 6)
	for v := int64(1); v < last; v++ {
		if a := e.text("r1", "aaaaaaaaaaaa", v, "r"+strconv.FormatInt(v, 10), strings.Repeat("a", int(v))); a != collab.Applied {
			t.Fatalf("version %d: %c", v, a)
		}
	}
	collab.ResetCaches()
	if a := e.text("r1", "bbbbbbbbbbbb", 3, "old", "ba"); a != collab.Stale {
		t.Errorf("before the checkpoint: %c", a)
	}
	base := last - 2
	if a := e.text("r1", "bbbbbbbbbbbb", base, "new", "b"+strings.Repeat("a", int(base)-2)); a != collab.Applied {
		t.Errorf("after the checkpoint: %c", a)
	}
	if got, _ := e.runHead("r1"); got != "b"+strings.Repeat("a", int(last)-2) {
		t.Errorf("head %q", got)
	}
}
