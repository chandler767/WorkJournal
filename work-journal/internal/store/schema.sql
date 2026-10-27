CREATE TABLE IF NOT EXISTS hours (
  day TEXT NOT NULL,
  hour INTEGER NOT NULL,
  status TEXT NOT NULL,
  summary_json TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  PRIMARY KEY (day, hour)
);
CREATE INDEX IF NOT EXISTS hours_status ON hours(status);

CREATE TABLE IF NOT EXISTS segments (
  day TEXT NOT NULL,
  hour INTEGER NOT NULL,
  start TEXT NOT NULL,
  end TEXT NOT NULL,
  app TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT '',
  category TEXT NOT NULL DEFAULT '',
  confidence REAL NOT NULL DEFAULT 0,
  source TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (day, hour, start)
);

CREATE TABLE IF NOT EXISTS meetings (
  id TEXT PRIMARY KEY,
  day TEXT NOT NULL,
  start TEXT NOT NULL,
  end TEXT NOT NULL,
  app TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  summary_json TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS meetings_day ON meetings(day);

CREATE TABLE IF NOT EXISTS participants (
  meeting_id TEXT NOT NULL,
  speaker TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  confidence REAL NOT NULL DEFAULT 0,
  confirmed INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (meeting_id, speaker)
);

CREATE TABLE IF NOT EXISTS kv (
  key TEXT PRIMARY KEY,
  val TEXT NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS docs_fts USING fts5(
  text, kind UNINDEXED, day UNINDEXED, hour UNINDEXED, ts UNINDEXED, ref UNINDEXED
);

-- An imported recording (journal import or the phone upload page): its id is
-- the id of the meeting it becomes; status is pending, done or needs_review,
-- and error is the last processing failure ('' when none).
CREATE TABLE IF NOT EXISTS imports (
  id TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);

-- A note the user wrote about a day (start and end ''), or a time range on
-- it (HH:MM on the recorded clock, end exclusive). kind is context or
-- correction; source is dashboard, mcp, cli or import. Notes are not
-- recordings: retention never removes them.
CREATE TABLE IF NOT EXISTS notes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,
  day TEXT NOT NULL,
  start TEXT NOT NULL DEFAULT '',
  end TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL,
  source TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS notes_day ON notes(day);

-- Work a note change queued, which serve runs on its next tick: hours to
-- summarize again (redo summary) or to reprocess with their privacy
-- decisions cleared (redo privacy), and with hour -1 the day's journal entry
-- and its week's rollup, rewritten when already written.
CREATE TABLE IF NOT EXISTS note_queue (
  day TEXT NOT NULL,
  hour INTEGER NOT NULL,
  redo TEXT NOT NULL DEFAULT '',
  seq INTEGER NOT NULL DEFAULT 0, -- bumped on every queueing
  PRIMARY KEY (day, hour)
);
