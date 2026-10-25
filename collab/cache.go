package collab

import (
	"container/list"
	"sync"
	"sync/atomic"

	"github.com/zweiundeins/lith/sqlite"
	"github.com/zweiundeins/lith/writer"

	"github.com/zweiundeins/prosemirror-go"
)

// lru is a cache bounded by the bytes of its values, the least recently
// used going first. It is safe for concurrent use.
type lru[K comparable, V any] struct {
	mu   sync.Mutex
	max  int64
	cur  int64
	size func(V) int64
	ll   list.List
	m    map[K]*list.Element

	hits, misses atomic.Int64
}

type lruEntry[K comparable, V any] struct {
	key  K
	val  V
	size int64
}

func newLRU[K comparable, V any](max int64, size func(V) int64) *lru[K, V] {
	return &lru[K, V]{max: max, size: size, m: map[K]*list.Element{}}
}

func (c *lru[K, V]) get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[k]; ok {
		c.ll.MoveToFront(e)
		c.hits.Add(1)
		return e.Value.(*lruEntry[K, V]).val, true
	}
	c.misses.Add(1)
	var zero V
	return zero, false
}

func (c *lru[K, V]) put(k K, v V) {
	n := c.size(v)
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[k]; ok {
		old := e.Value.(*lruEntry[K, V])
		c.cur += n - old.size
		old.val, old.size = v, n
		c.ll.MoveToFront(e)
	} else {
		c.m[k] = c.ll.PushFront(&lruEntry[K, V]{key: k, val: v, size: n})
		c.cur += n
	}
	for c.cur > c.max && c.ll.Len() > 1 {
		e := c.ll.Back()
		x := e.Value.(*lruEntry[K, V])
		c.ll.Remove(e)
		delete(c.m, x.key)
		c.cur -= x.size
	}
}

// docKey names one immutable document: a subject's of a kind at a
// version of an epoch, with the ref of the commit that made it. A command
// puts what it makes in the cache only once its batch has committed
// (stage); until then its batch's commands and projectors read it there.
type docKey struct {
	kind, id, epoch string
	version         int64
	ref             string
}

// commitKey names a committed commit of a subject's document.
type commitKey struct {
	kind, id, epoch string
	version         int64
}

// commitRec is a commit as the writer applied it: its steps parsed, for
// rebasing, and as stored, for the editors' frames.
type commitRec struct {
	Ref, Client string
	Base        int64
	Steps       []prosemirror.Step
	Raw         []byte // the steps' JSON
	Snapshot    prosemirror.Doc
}

// Cache sizes: the shared documents of the writer, the views and the jobs
// (README), and the commits since a recent base, of every Kind.
const (
	DocCacheBytes    = 96 << 20
	CommitCacheBytes = 16 << 20
)

var (
	docs    = newLRU[docKey, prosemirror.Doc](DocCacheBytes, func(d prosemirror.Doc) int64 { return 3*int64(d.JSONLen()) + 512 })
	commits = newLRU[commitKey, commitRec](CommitCacheBytes, func(c commitRec) int64 { return 2*int64(len(c.Raw)) + 256 })
)

// CacheStats are the document cache's hits and misses since start.
func CacheStats() (hits, misses int64) { return docs.hits.Load(), docs.misses.Load() }

// ResetCaches empties the document and commit caches, as a process that
// starts again has them: for tests of reads that miss them.
func ResetCaches() {
	docs.reset()
	commits.reset()
}

// reset empties the cache.
func (c *lru[K, V]) reset() {
	c.mu.Lock()
	c.ll.Init()
	c.m = map[K]*list.Element{}
	c.cur = 0
	c.mu.Unlock()
}

// pendingDocs is the writer-local key of the documents commands of the
// batch made: the cache takes them once the batch has committed, so a
// batch that fails leaves none there.
type pendingDocs struct{}

// stage keeps d for the commands and projectors of the batch, and puts
// it in the document cache after the commit.
func stage(tx *writer.Tx, k docKey, d prosemirror.Doc) {
	p, _ := tx.Local(pendingDocs{}).(map[docKey]prosemirror.Doc)
	if p == nil || len(p) > 1024 {
		p = map[docKey]prosemirror.Doc{}
		tx.SetLocal(pendingDocs{}, p)
	}
	p[k] = d
	tx.AfterCommit(func() {
		docs.put(k, d)
		delete(p, k)
	})
}

// cached returns a document from the cache or, in a command, from the
// documents its batch made.
func cached(q sqlite.Querier, k docKey) (prosemirror.Doc, bool) {
	if d, ok := docs.get(k); ok {
		return d, true
	}
	if tx, ok := q.(*writer.Tx); ok {
		p, _ := tx.Local(pendingDocs{}).(map[docKey]prosemirror.Doc)
		d, ok := p[k]
		return d, ok
	}
	return nil, false
}

// keep puts a document in the cache: at once for a read of committed
// state (fill), after the commit in a command.
func keep(q sqlite.Querier, k docKey, d prosemirror.Doc, fill bool) {
	if tx, ok := q.(*writer.Tx); ok {
		stage(tx, k, d)
		return
	}
	if fill {
		docs.put(k, d)
	}
}
