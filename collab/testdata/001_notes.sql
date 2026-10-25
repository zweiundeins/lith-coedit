-- The tables of the tests' Kind, "notedoc", as the README gives them.
CREATE TABLE note_doc (
    note_id    TEXT PRIMARY KEY,
    epoch      TEXT NOT NULL,
    version    INTEGER NOT NULL,
    ref        TEXT NOT NULL DEFAULT '',
    checkpoint INTEGER NOT NULL,
    doc        TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    blocks     INTEGER NOT NULL DEFAULT 0,
    last_at    INTEGER NOT NULL
) STRICT;

CREATE TABLE note_editors (
    sid       TEXT NOT NULL,
    tab_id    TEXT NOT NULL,
    note_id   TEXT NOT NULL,
    person_id TEXT NOT NULL,
    client    TEXT NOT NULL,
    epoch     TEXT NOT NULL,
    ack       INTEGER NOT NULL,
    v         INTEGER NOT NULL DEFAULT 0,
    anchor    INTEGER NOT NULL DEFAULT 0,
    head      INTEGER NOT NULL DEFAULT 0,
    seq       INTEGER NOT NULL DEFAULT 0,
    refusal   TEXT NOT NULL DEFAULT '',
    moved_at  INTEGER NOT NULL,
    seen_at   INTEGER NOT NULL,
    PRIMARY KEY (sid, tab_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX note_editors_note ON note_editors (note_id);
CREATE INDEX note_editors_tab ON note_editors (tab_id);

CREATE TABLE collab_uncut (
    kind TEXT NOT NULL,
    id   TEXT NOT NULL,
    at   INTEGER NOT NULL,
    PRIMARY KEY (kind, id)
) STRICT, WITHOUT ROWID;

CREATE INDEX collab_uncut_at ON collab_uncut (kind, at);

CREATE TABLE photos (id TEXT PRIMARY KEY) STRICT;
