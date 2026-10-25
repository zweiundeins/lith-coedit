# Changelog

Until 1.0, a minor release may change the API; its entry says what to change in an app.

## v0.1.0 (2026-10-25)

The first release: tickit's `internal/collab` and the shared parts of its editor, extracted as a module of its own on prosemirror-go and lith v0.5.1. To move an app from `internal/collab`:

- Import `github.com/zweiundeins/lith-coedit/collab`. `Config` takes the schema, `Rules(tx, actor, id)`, `Epoch`, `NotFound` and `ValidID` in place of the app's packages; `Commit.By` is `Commit.Meta`, an `events.Meta`.
- `Evolve`, `CreateDecider` and `CommitDecider` are the Kind's. `Blank`, `IsBlank`, `Canonical`, `Marshal`, `Images` and `ImageExists` stay with the app.
- `CommitWhole` refuses with a `*writer.Refusal`; the app turns it into its own refusal of a field.
- `Readonly` answers 409, as `Answer.Status()` says: the frame tells the editor it is read-only.

- `collab`: a Kind per subject, the commit with its rebase, idempotent by ref, refusals in the editing tab's row (Stale when the tab has none), the document and commit caches, which take what a command made only once its batch commits, the editors' rows (`Open` starts presence again at seq 0), presence, `Pause`, the head projector, `Batches`, `Cut`, `Mark`, `Release`, `Hold` and `Fold` with `collab_uncut`, `ParseCommit`, `Answer.Status`.
- `editor/`: the browser side as ES modules an app bundles with its own editor: `Session` (the commit protocol, with `insertNode`), `collab.js`, `carets.js`, `comments.js` (text ranges and node anchors), `ids.js` (stable node ids), `schema.js` (`schemaSpec`), and the fake writer of `test/authority/` for an app's browser tests.
