package collab

import (
	"context"

	"github.com/zweiundeins/lith/sqlite"
	"github.com/zweiundeins/prosemirror-go"
)

const textSchemaJSON = `{"nodes": [
	{"name": "doc", "content": "text*", "code": true},
	{"name": "text"}
]}`

// TextSchema is a document of plain text: one run of characters without
// marks, whose positions are the text's UTF-16 offsets. A Kind on it takes
// commits of whole texts (Commit.Text).
var TextSchema = prosemirror.MustSchema([]byte(textSchemaJSON))

// TextDoc is s as a document of TextSchema.
func TextDoc(s string) prosemirror.Doc {
	d, err := textDoc(TextSchema, s)
	if err != nil {
		panic(err) // TextSchema holds any text
	}
	return d
}

// textDoc is s as a document of a schema whose doc holds text.
func textDoc(s *prosemirror.Schema, text string) (prosemirror.Doc, error) {
	var content []*prosemirror.Node
	if text != "" {
		n, err := s.Text(text)
		if err != nil {
			return nil, err
		}
		content = append(content, n)
	}
	d, err := s.Node("doc").Create(nil, content)
	if err != nil {
		return nil, err
	}
	return d, d.Check()
}

// TextOf is the text of a document.
func TextOf(d prosemirror.Doc) string {
	return d.TextBetween(0, d.Content().Size(), "", nil)
}

// docBefore is the document at version v, older than the head, for the
// diff of a whole text made on it: from the cache, else from the head
// table's checkpoint, which ErrStale says is newer than v.
func (k *Kind) docBefore(ctx context.Context, q sqlite.Querier, id, epoch string, v int64) (prosemirror.Doc, error) {
	if v < 1 {
		return nil, ErrStale
	}
	at, first, err := k.loadCommits(ctx, q, id, epoch, v-1, v, false)
	if err != nil {
		return nil, err
	}
	if len(at) != 1 || first != v {
		return nil, ErrStale
	}
	return k.docAt(ctx, q, id, epoch, v, at[0].Ref, false)
}
