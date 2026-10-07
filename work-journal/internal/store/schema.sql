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
