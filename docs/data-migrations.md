# Data migrations and recovery

Kern stores local state in `kern.db` beneath the selected data directory. The
database uses SQLite WAL mode and forward-only schema migrations.

## Automatic upgrade behavior

On startup Kern determines the database schema version before applying any
pending migration. If an existing database contains application tables and is
older than the bundled schema, Kern first creates a transactionally consistent
SQLite backup using `VACUUM INTO`.

The backup is stored beside the database with a name like:

```text
kern.db.pre-migration-v12-to-v14-123456789.db
```

Its permissions are restricted to the current user. The exact path is written
to the local structured log. A new empty database is not backed up, and opening
an already-current database does not create another backup. Kern refuses to
open a database whose schema is newer than the running binary supports.

Backups are not deleted automatically because they are the operator's rollback
boundary. Remove obsolete copies only after the upgraded version has been
verified and an independent backup policy exists.

## Recovery procedure

Do not modify database files while Kern is running.

1. Stop every Kern process using the data directory.
2. Preserve the current `kern.db`, `kern.db-wal`, and `kern.db-shm` files as a
   separate incident copy.
3. Choose the pre-migration backup named in the startup log.
4. Copy that backup to `kern.db`; do not reuse WAL or SHM files from the newer
   database.
5. Start the older Kern binary whose schema matches the backup.
6. Run `kern doctor` and inspect several historical tasks before resuming work.

Restoring an old database discards tasks and approvals created after that
backup. Kern therefore never performs this rollback automatically.

## Release verification

Every new migration must include an automated upgrade test from the preceding
schema. The test must prove that a consistent, permission-restricted backup was
created before migration and that reopening the current schema does not create
duplicate backups.
