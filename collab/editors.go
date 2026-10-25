package collab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/zweiundeins/lith/sqlite"
	"github.com/zweiundeins/lith/writer"

	"github.com/zweiundeins/tickit/internal/access"
)

// Client is a tab as other editors see it, made from its id: the first
// 12 hex digits of SHA-256 (docs/editor.md). The editor's page picks a
// random one instead (its GET knows no tab id); both keep tab ids private.
func Client(tab string) string {
	h := sha256.Sum256([]byte(tab))
	return hex.EncodeToString(h[:6])
}

// ValidClient reports whether s is a client id: 12 lower-case hex digits.
func ValidClient(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, c := range []byte(s) {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// Open is a tab that starts editing a subject: its row in the Kind's
// editors table, acknowledging version Base (the one its page rendered).
// The subject's command checks who may first.
type Open struct {
	Session, Tab string
	ID           string
	Person       string
	Base         int64
	Client       string
	At           time.Time
}

// Valid reports whether the row's fields are well formed.
func (o Open) Valid() bool {
	return o.Session != "" && o.Tab != "" && ValidID(o.ID) && o.Person != "" && o.Base >= 0 && ValidClient(o.Client)
}

// Open writes the editing tab's row.
func (k *Kind) Open(tx *writer.Tx, o Open) error {
	if !o.Valid() {
		return errors.New("collab: a malformed editor")
	}
	ds, err := k.State(tx, o.ID)
	if err != nil {
		return err
	}
	base := min(o.Base, ds.Version)
	now := o.At.UnixMilli()
	if _, err := tx.Exec(k.sql.openEditor, o.Session, o.Tab, o.ID, o.Person, o.Client, ds.Epoch, base, base, now, now); err != nil {
		return err
	}
	tx.Touch(k.EditorTopic(o.Tab), k.CaretsTopic(o.ID))
	return nil
}

// Close ends a tab's editing: its row goes, and with it its caret. It
// returns the subject the tab edited, "" when it had no row.
func (k *Kind) Close(tx *writer.Tx, session, tab string) (string, error) {
	var id string
	err := tx.QueryRow(k.sql.closeEditor, session, tab).Scan(&id)
	if err != nil {
		return "", nil // no row: nothing to close
	}
	tx.Touch(k.EditorTopic(tab), k.CaretsTopic(id))
	return id, nil
}

// Presence is an editing tab's selection and acknowledged version (MP-06):
// at most every 100 ms; a post that a newer one (Seq) overtook changes
// nothing. The handler sends it (writer.Send): the next one replaces it.
type Presence struct {
	Kind         *Kind
	Session, Tab string
	ID           string
	Epoch        string
	Ack, V       int64
	Anchor, Head int
	Seq          int64
	At           time.Time
}

func (c Presence) Validate() error {
	if c.Kind == nil || c.Session == "" || c.Tab == "" || !ValidID(c.ID) || c.Ack < 0 || c.V < 0 || c.Anchor < 0 || c.Head < 0 || c.Seq < 0 {
		return errors.New("collab: a malformed presence")
	}
	return nil
}

func (c Presence) Apply(tx *writer.Tx) error {
	now := c.At.UnixMilli()
	res, err := tx.Exec(c.Kind.sql.presence,
		c.Ack, c.V, c.Anchor, c.Head, c.Seq, now, c.Anchor, c.Head, c.V, now, c.Session, c.Tab, c.ID, c.Epoch, c.Seq)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		tx.Touch(c.Kind.CaretsTopic(c.ID))
	}
	return nil
}

// EditorRow is an editing tab's row; ID is the subject's.
type EditorRow struct {
	Tab, ID, Person, Client, Epoch string
	Ack, V                         int64
	Anchor, Head                   int
	Refusal                        string
	MovedAt                        time.Time
}

// Editor reads the row of an editing tab of a person; access.ErrNotFound
// when there is none.
func (k *Kind) Editor(ctx context.Context, q sqlite.Querier, tab, person string) (EditorRow, error) {
	var r EditorRow
	var moved int64
	err := q.QueryRowContext(ctx, k.sql.editor, tab, person).
		Scan(&r.Tab, &r.ID, &r.Person, &r.Client, &r.Epoch, &r.Ack, &r.V, &r.Anchor, &r.Head, &r.Refusal, &moved)
	if err != nil {
		return r, access.ErrNotFound
	}
	r.MovedAt = time.UnixMilli(moved)
	return r, nil
}

// Editors are the rows of a subject's editing tabs.
func (k *Kind) Editors(ctx context.Context, q sqlite.Querier, id string) ([]EditorRow, error) {
	rows, err := q.QueryContext(ctx, k.sql.editors, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EditorRow
	for rows.Next() {
		var r EditorRow
		var moved int64
		if err := rows.Scan(&r.Tab, &r.ID, &r.Person, &r.Client, &r.Epoch, &r.Ack, &r.V, &r.Anchor, &r.Head, &moved); err != nil {
			return nil, err
		}
		r.MovedAt = time.UnixMilli(moved)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SweepEditors deletes the rows of editing tabs not seen for a day.
func (k *Kind) SweepEditors(tx *writer.Tx, at time.Time) error {
	_, err := tx.Exec(k.sql.sweep, at.Add(-24*time.Hour).UnixMilli())
	return err
}
