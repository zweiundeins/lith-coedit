# lith-coedit

Co-editing of ProseMirror documents for [lith](https://github.com/zweiundeins/lith) apps: several people edit one document at once, the server orders every edit and keeps the document, and no CRDT is involved. Two parts:

| Path | What it is |
| --- | --- |
| `collab/` | Go package `collab`, on [prosemirror-go](https://github.com/zweiundeins/prosemirror-go) and lith v0.5.1 (`events`, `sqlite`, `writer`): a kind of co-edited document per subject, the commit with its rebase, the document and commit caches, the editing tabs' rows, the head projector, the fold |
| `editor/` | the browser side as ES modules: the commit protocol (`session.js`, `collab.js`), remote carets (`carets.js`), inline-comment anchoring (`comments.js`), and the fake writer an app's browser tests run against (`test/authority/`) |

The app keeps its own: the schema and its node types (prosemirror-go's hooks), who may edit, versions and what they record, the views, and in the browser its editor component, node views, toolbar and notices.

Extracted from [tickit](https://github.com/zweiundeins/tickit) (`internal/collab` and `editor/`), which runs it with 20 editors at 5 characters a second; this repository carries `internal/collab`'s history.

## How it works

1. **The writer rebases.** A commit names the stream version it was made on (`base`). The writer maps its steps over the commits since `base`, applies what still applies, checks the result against the schema and the app's rules, and appends one event with `events.Exactly(head)`. This is the protocol of [prosemirror-collab-commit](https://github.com/stepwisehq/prosemirror-collab-commit). A commit whose steps all drop is still appended, with no steps, because the editor confirms its commit by finding its `ref`. The writer refuses a commit from another epoch (a reset) and one made on a version older than the last fold.
2. **A Go core.** prosemirror-go applies and rebases the steps in the writer, byte for byte as prosemirror-transform does; running ProseMirror in a JavaScript engine in the writer would cost a large share of it per busy document.
3. **One view per editing tab** whose frame sets the editor host's `batches` attribute to every commit after the version the tab acknowledged (kept in the tab's row), so a skipped frame loses nothing.
4. **Commits outside the tab's action.** A commit is its own command, confirmed by its ref in the next frame; a refusal goes into the editing tab's row, not into the tab's outcomes, so ten commits a second rewrite no tab row.
5. **Fold at version cuts.** The app cuts versions; the commits up to a version are folded into a snapshot with `events.Compact` some time later.

## The protocol

- **Step**: prosemirror-transform's `Step.toJSON()`. Positions count UTF-16 units.
- **Commit**: the app's route posts the payload `{tab, epoch, base, ref, steps}`; `ref` is the commit's random id. A commit whose ref and client are among the commits since its base applied already and answers 204 again, so a resent commit applies once without an op id per commit. Answers (`collab.Answer`): 204 applied, or applied before (`Applied`); 409 another epoch, the subject gone, or `base` older than the fold (`Stale`; the next frame says which); 422 refused by a rule, the reason in the tab's row (`Refused`); 409 too when the actor may not edit (`Readonly`), whose frame says `readonly`. `Answer.Status()` maps them. No person reads a status: the page's code acts on it, and what a person reads comes on the stream. The app adds 429 for its rate limit.
- **Presence**: `{tab, epoch, ack, v, anchor, head, seq}`, at most every 100 ms; `ack` and `v` are the confirmed version, `anchor` and `head` the selection mapped back through the unconfirmed steps to it; `seq` makes an overtaken post change nothing. The app sends it without waiting (`writer.Send`).
- **Open**: the tab's row (`Kind.Open`), acknowledging the version its page rendered, with `client`: 12 hex digits that name the tab to others (`collab.Client(tab)`, or random ones the page picks), so tab ids stay private.
- **Frame**: the tab's view patches the editor's host element. Every frame renders every attribute, because Datastar's morph removes attributes the new element lacks:

```html
<my-editor id="editor" epoch="e0" client="c3f9a1b2c4d5" base="41" head="57"
  batches='[{"v":56,"ref":"x81k","client":"c_77","steps":[…]}]'
  carets='[{"id":"c_77","name":"Sunny Otter","hue":7,"v":56,"anchor":128,"head":128,"idle":false}]'
  comments='{"v":57,"list":[{"id":"…","from":12,"to":20}]}'
  state="ok" refusal="">
  <script type="application/json" data-ignore-morph>{…the document at version base…}</script>
  <div class="pm-mount" data-ignore-morph data-ignore></div>
</my-editor>
```

| Attribute | Value |
| --- | --- |
| `epoch` | The document stream's epoch. Another one than the editor started with stops it as `reset`. |
| `client` | This tab as others see it; its own entry in `carets` is left out. |
| `tab` | Optional: when set, commits and presence carry it. |
| `base` | The version of the document in the script, read once at mount. |
| `head` | The version of the newest commit. |
| `batches` | `[{v, ref, client, steps}]`: every commit after the tab's acknowledged version, as the writer applied it, at most 500 (`Kind.Batches`). The editor applies them in order and waits at a gap for the next frame. |
| `carets` | `[{id, name, hue, v, anchor, head, idle}]` at version `v`; `hue` 0 to 15. One shared view per subject renders them for every editor. |
| `comments` | Optional: `{v, list: [{id, from, to} \| {id, node}]}`: inline comments on a range of text at version `v`, drawn as highlights and mapped like carets, or on a node by its stable id (prosemirror-go's `IDs`), drawn on the node's element; a node anchor needs no mapping and is gone with the node. Like `carets`, it may come from a shared view of the subject rather than the tab's frame (a signal the host's attribute follows, kept through the tab's frames by `data-preserve-attr`): one render then serves every editor, and `v` places the ranges whichever frame arrives first. |
| `state` | `ok`, `gap` (the tab's version was folded away), `reset` (another epoch, or the tab's row is gone), `gone` (the subject was removed) or `readonly`. |
| `refusal` | `{"ref", "message"}` for a commit the writer refused (the editor takes its steps out), or plain text to show. Each ref is handled once, so the row may keep it. |
| `readonly` | Present: read-only. The editor keeps applying `batches`, sends nothing and keeps the person's unsaved text. |

The page's GET puts the document at version `base` into the script element; later frames leave it empty and the morph keeps the old one, as it keeps `.pm-mount`, where ProseMirror edits in light DOM.

## Go: package collab

A `Kind` is one kind of co-edited document, defined at package level:

```go
var pageDocs = collab.NewKind(collab.Config{
	Name: "pagedoc", Noun: "page", Schema: schema, Store: func() *events.Store { return store },
	Head: "page_doc", Editors: "page_editors", Key: "page_id",
	EditorTopic: func(tab string) string { return "editor:" + tab },
	CaretsTopic: func(id string) string { return "carets:" + id },
	Rules: func(tx *writer.Tx, actor, id string) prosemirror.Rules { return pageRules(tx, actor) },
})
```

What the app provides:

| Hook | Says |
| --- | --- |
| `Config.Schema` | the documents' schema |
| `Config.Rules(tx, actor, id)` | the rules of a commit by `actor` to `id`, built in the commit's transaction, so a rule can ask the app's data (is this image uploaded, may this person name this id); a refusal's message goes into the tab's row |
| `Commit.Gate(tx)` | may this person edit this subject now: `ErrGone` or a not-found error answers `Stale`, an error with `Denied() bool` that says true `Readonly` |
| `Commit.Check(head, next)` | a refusal beyond the rules, such as a description's own limits |
| `Config.Epoch(tx)` | the epoch of a new document stream; an app that resets its data and seeds documents with the same ids bumps it, so no edit from before a reset applies after it (default `e0`) |
| `Config.NotFound` | the app's own not-found error, which `State` and `Editor` wrap and a gate may return (default `collab.ErrNotFound`) |
| `Config.ValidID` | the format of the subject's ids (default: 1 to 64 letters, digits, `-` and `_`) |
| `Config.OnCommit(tx, id, steps, next)` | what follows each commit's steps in its command, such as inline comments' anchors (`prosemirror.MapRange`) |
| `Commit.Meta` | the commit's event metadata, its `Actor` who commits |

The rest: `Kind.Create`, `Commit` (`Validate` parses outside the writer; `ApplyResult` answers one byte, an `Answer`, whose `Status()` is the route's HTTP status), `Kind.CommitWhole` (a whole document as one step on the head, for a restore or an import; a refusal is a `*writer.Refusal`), `StepsSince`, `EditorsSince`, `Open`, `Presence`, `Close`, `Editor`, `Editors`, `Pause` and `Paused` (an editor the app refused before the writer, shown read-only with the reason), `SweepEditors`, `Head` and `StateDoc` (through the caches), `Batches`, `Projector` (the head table, with a hook for the app's own tables), `Cut` and `Fold`, `Client`, `ParseCommit` (500 steps in 256 KB at most), `CacheStats`, `ResetCaches`. The document cache (96 MB) and the commit cache (16 MB) are shared by every Kind of the process.

### Tables

A Kind needs its head and editors tables, and every Kind shares `collab_uncut`, which holds per document the time of its oldest commit no version holds yet, so the fold finds its bound by index. Put them in a migration, with the Kind's names:

```sql
CREATE TABLE page_doc (
    page_id    TEXT PRIMARY KEY,
    epoch      TEXT NOT NULL,
    version    INTEGER NOT NULL,
    ref        TEXT NOT NULL DEFAULT '',
    checkpoint INTEGER NOT NULL,             -- the version doc holds
    doc        TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,   -- of the head document, as stored
    blocks     INTEGER NOT NULL DEFAULT 0,
    last_at    INTEGER NOT NULL              -- the last commit's time
) STRICT;

CREATE TABLE page_editors (
    sid       TEXT NOT NULL,
    tab_id    TEXT NOT NULL,
    page_id   TEXT NOT NULL,
    person_id TEXT NOT NULL,
    client    TEXT NOT NULL,
    epoch     TEXT NOT NULL,
    ack       INTEGER NOT NULL,
    v         INTEGER NOT NULL DEFAULT 0,
    anchor    INTEGER NOT NULL DEFAULT 0,
    head      INTEGER NOT NULL DEFAULT 0,
    seq       INTEGER NOT NULL DEFAULT 0,
    refusal   TEXT NOT NULL DEFAULT '',      -- {"ref", "message"} as JSON
    moved_at  INTEGER NOT NULL,
    seen_at   INTEGER NOT NULL,
    PRIMARY KEY (sid, tab_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX page_editors_page ON page_editors (page_id);
CREATE INDEX page_editors_tab ON page_editors (tab_id);

CREATE TABLE collab_uncut (
    kind TEXT NOT NULL,
    id   TEXT NOT NULL,
    at   INTEGER NOT NULL,
    PRIMARY KEY (kind, id)
) STRICT, WITHOUT ROWID;
CREATE INDEX collab_uncut_at ON collab_uncut (kind, at);
```

The head table is a projection (`Kind.Projector`, with a checkpoint of the document every 50 commits); the editors table and `collab_uncut` are written by the commands. The events are `<name>.created {epoch, doc}`, `.committed {ref, client, base, steps}` and `.snapshot {epoch, doc}`; the stream version is the version the editor speaks.

### Versions and the fold

The app cuts versions (when the last editor leaves, after a minute without commits, every few minutes of editing, on "Save version") and records them as it likes, calling `Kind.Cut(tx, id, upto)` with the version a cut holds. A commit marks its subject (`Kind.Mark`); a subject that is gone (in a trash, deleted, hidden) should hold no fold back, since no version is cut for it: `Kind.Release` deletes its row, and `Kind.Hold(tx, id, upto)` puts back the oldest of its events after its last version when it returns. A job calls `Kind.Fold(tx, since, limit)` with a time no later than the oldest `collab_uncut` row it may fold to, so no commit after a subject's last version is folded. While the commit cache holds the folded commits, an editor behind the fold still merges; past that its frame says `gap`.

## The browser: editor/

| Module | Exports |
| --- | --- |
| `session.js` | `Session`: the protocol of one editor, on the app's ProseMirror view |
| `collab.js` | the `collab` plugin (unconfirmed steps with sequence numbers), `makeCommit`, `batchTransaction`, `receiveTransaction`, `collabState`, `getVersion`, `REMOTE` (the meta of the module's own transactions) |
| `carets.js` | `caretPlugin`, `Carets` (remote carets and selections, mapped from the version they were sent at) |
| `comments.js` | `commentPlugin`, `drawComments`, `placeComments`, `commentRange` (a selected node with a stable id as `{v, node}`) |
| `ids.js` | `idPlugin({ids, make})`: gives nodes of the types in `ids` (`{photo: 'id'}`, as prosemirror-go's `IDs`) an id when this editor makes them, and a new one to a copy (a paste, a split); `findNode`, `randomId` |
| `schema.js` | `schemaSpec(json, extra)`: the schema JSON prosemirror-go reads (ordered arrays of nodes and marks) as the spec `new Schema` takes, with the browser's `toDOM` and `parseDOM` per name in `extra` |
| `index.js` | all of the above |

The app's editor makes the view with `collab({version: base})`, `caretPlugin()` and `commentPlugin()` among its plugins (and `idPlugin` when its schema has node types with stable ids, which the server's `Rules.IDs` checks), then a session:

```js
import { Session, collab, caretPlugin, commentPlugin } from 'lith-coedit'

const session = new Session({
	view, schema,
	read: () => host.props(),          // {epoch, client, tab, state, readonly, batches, carets, comments, refusal}
	emit: (type, detail) => {          // commit, presence, ack, refused, stopped, resumed, throttled
		if (type === 'commit') post('/commit', detail).then(() => session.posted(detail.ref, true), () => session.posted(detail.ref, false))
	},
	carets: { prefix: 'my' },          // .my-caret, .my-caret__tag, .my-remote-sel
	comments: { className: 'highlight', attr: 'data-highlight' },
	ids: { photo: 'id' },              // node types with stable ids, for comments on nodes
})
// in dispatchTransaction, after view.updateState: session.transaction(tr)
// when the host's attributes change (a frame): session.sync()
```

`posted(ref, ok, status)` tells the session that the post of a commit settled: `ok` false is a network failure, and the commit goes out again after 1, 2, 4, 8 and 15 s; with a `status`, 429 and 5xx back off the same way, and 409 and 422 wait 9 s for the frame that says why before the commit goes out again. An answered commit that no frame confirms within 3 s goes out again, and one that nobody reports with `posted` after 6 s: every resend has the same ref, so the writer applies the commit once. Pass the status, from Datastar's `datastar-fetch` event, or the 409, 422 and 429 branches never run. `insertNode(type, attrs, pos?)` puts a node at `pos` or in place of the selection as a local edit, so it goes out with the next commit (a photo dragged in from a side panel); with `idPlugin` among the plugins it gets its id. `editable()`, `unsaved()`, `stopped`, `commentRange()` and `stats` are for the app's toolbar, status line and tests. `emit` events: `commit {epoch, base, ref, steps, tab?}`, `presence {epoch, ack, v, anchor, head, seq, tab?}`, `ack {epoch, ack}`, `refused {ref, message, dropped}`, `stopped {state, unsaved}`, `resumed {}` and `throttled {}`.

### Using it from an app

The modules are plain ES modules that import `prosemirror-model`, `-state`, `-transform` and `-view` by bare name. The app bundles them into its own editor with esbuild, from the module's directory in the Go module cache, so the JavaScript always matches the Go version in `go.mod`, and resolves ProseMirror from its own `node_modules`, so the bundle holds one copy of each package:

```js
import { execFileSync } from 'node:child_process'
import { join } from 'node:path'

const dir = execFileSync('go', ['list', '-m', '-f', '{{.Dir}}', 'github.com/zweiundeins/lith-coedit'], { encoding: 'utf8' }).trim()
if (!dir) throw new Error("lith-coedit is not downloaded: run go mod download github.com/zweiundeins/lith-coedit")
await build({
	entryPoints: ['src/editor.js'], bundle: true, format: 'esm',
	alias: {
		'lith-coedit': join(dir, 'editor'), // 'lith-coedit' is src/index.js, 'lith-coedit/test/...' the fake writer
		'prosemirror-model': join(here, 'node_modules/prosemirror-model'),
		// the same for prosemirror-state, -transform and -view
	},
	nodePaths: [join(here, 'node_modules')],
})
```

The aliases of the ProseMirror packages matter where the lith-coedit checkout has a `node_modules` of its own: esbuild would take those next to the importing file, and two copies of prosemirror-model break `instanceof`. The app's `package.json` pins the ProseMirror versions of `editor/package.json`. With a `replace` directive to a local checkout, `go list` names that checkout. tickit's `editor/lith-coedit.mjs` is a complete example.

### Testing an app's editor

An app that tests its editor in Node bundles the test the way it bundles the editor: the same `lith-coedit` alias to the module's directory and the same ProseMirror aliases to its own `node_modules`, so that the test and lith-coedit's modules share one copy of each package. The module in Go's module cache has no `node_modules`; importing ProseMirror by file URL from lith-coedit's `editor/node_modules` works only in a checkout of lith-coedit itself, which is how its own tests run. `editor/test/authority/` holds a fake writer in JavaScript: `rebase.js` is the rebase prosemirror-go's oracle runs (a Go test compares them) and `authority.js` keeps versions, tabs' rows, refusals, folds and resets and renders each tab's frame, with the app's own attributes (`attrs`). An app's browser harness runs it in a SharedWorker, as tickit's does.

## Tests

    go test ./...                          # collab against a SQLite file; rebase.js against the oracle's
    cd editor && npm ci && npm test        # the protocol in Node: converging editors, refusals, resets, readonly

## Licence

MIT, © zwei und eins gmbh and contributors ([LICENSE](LICENSE)). `editor/src/collab.js` and `editor/test/authority/rebase.js` adapt [@stepwisehq/prosemirror-collab-commit](https://github.com/stepwisehq/prosemirror-collab-commit) 1.0.5 (MIT, © Greg Zapp), which builds on prosemirror-collab (MIT, © Marijn Haverbeke and others); [editor/LICENSES.txt](editor/LICENSES.txt) holds their licences.
