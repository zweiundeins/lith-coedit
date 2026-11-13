-- The tables of the tests' plain-text Kind, "rundoc".
CREATE TABLE run_doc (
    run_id     TEXT PRIMARY KEY,
    epoch      TEXT NOT NULL,
    version    INTEGER NOT NULL,
    ref        TEXT NOT NULL DEFAULT '',
    checkpoint INTEGER NOT NULL,
    doc        TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    blocks     INTEGER NOT NULL DEFAULT 0,
    last_at    INTEGER NOT NULL
) STRICT;

CREATE TABLE run_editors (
    sid       TEXT NOT NULL,
    tab_id    TEXT NOT NULL,
    run_id    TEXT NOT NULL,
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

CREATE INDEX run_editors_run ON run_editors (run_id);
CREATE INDEX run_editors_tab ON run_editors (tab_id);
