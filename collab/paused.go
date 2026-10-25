package collab

import (
	"encoding/json"

	"github.com/zweiundeins/lith/writer"
)

// An editing tab whose commits the app refuses before they reach the
// writer (a blocked guest) is paused: its row's refusal says so, and its
// view then renders the editor read-only with the reason, so the editor
// stops sending, says why at the editor and keeps the person's text,
// unsaved, in their browser only. Opening the editor again clears it.

type pausedRefusal struct {
	Paused  bool   `json:"paused"`
	Message string `json:"message"`
}

// Pause marks the tab's editor of the subject as paused with msg, and
// touches its view the first time.
func (k *Kind) Pause(tx *writer.Tx, session, tab, id, msg string) error {
	b, err := json.Marshal(pausedRefusal{Paused: true, Message: msg})
	if err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE `+k.cfg.Editors+` SET refusal = ? WHERE sid = ? AND tab_id = ? AND `+k.cfg.Key+` = ? AND refusal != ?`, string(b), session, tab, id, string(b))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		tx.Touch(k.EditorTopic(tab))
	}
	return nil
}

// Paused is the message of a paused editor's refusal, and whether it is
// one.
func Paused(refusal string) (string, bool) {
	var r pausedRefusal
	if refusal == "" || refusal[0] != '{' || json.Unmarshal([]byte(refusal), &r) != nil || !r.Paused {
		return "", false
	}
	return r.Message, true
}
