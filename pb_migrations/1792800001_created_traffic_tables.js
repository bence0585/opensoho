/// <reference path="../pb_data/types.d.ts" />
// Plain tables (not collections) for the traffic history, see traffic.go:
// ~10^6 small rows that the UI never lists as records.
migrate((app) => {
  app.db().newQuery(`
    CREATE TABLE IF NOT EXISTS traffic_keys (
      id INTEGER PRIMARY KEY,
      key TEXT NOT NULL UNIQUE
    )
  `).execute()

  for (const table of ["traffic_minute", "traffic_hour"]) {
    app.db().newQuery(`
      CREATE TABLE IF NOT EXISTS ${table} (
        key_id INTEGER NOT NULL,
        ts INTEGER NOT NULL,
        rx INTEGER NOT NULL DEFAULT 0,
        tx INTEGER NOT NULL DEFAULT 0,
        secs REAL NOT NULL DEFAULT 0,
        PRIMARY KEY (key_id, ts)
      ) WITHOUT ROWID
    `).execute()
    app.db().newQuery(`CREATE INDEX IF NOT EXISTS idx_${table}_ts ON ${table} (ts)`).execute()
  }
}, (app) => {
  for (const table of ["traffic_hour", "traffic_minute", "traffic_keys"]) {
    app.db().newQuery(`DROP TABLE IF EXISTS ${table}`).execute()
  }
})
