# How it works

One container runs two processes as user `vdb`: `vdb-mysql` and the `mysqld` it supervises.

1. **Start.** `vdb-mysql` connects to the Source over verified TLS, discovers its databases, tables, views,
   routines and accounts, opens the plug-in socket `/run/vdb/vdb.sock`, copies the template data directory, starts
   `mysqld` without networking, rebuilds every object from the catalog of record, then restarts `mysqld` with its
   client port open. The port opens only when the environment is complete.
2. **Reads.** Every table uses the `vdb_mysql_storage` engine. A scan reads the Source in pages inside a consistent
   snapshot and merges them with the Delta: rows the environment updated or deleted are hidden, its own rows added.
   Ordered index scans interleave the two sides in MySQL's sort order; `COUNT(*)` is answered exactly with one Source
   count and a correction.
3. **Writes.** Inserts, updates and deletes go to the Delta, with uniqueness checked against the Delta and the
   Source, row and gap locks, foreign keys enforced as InnoDB enforces them, and savepoints. The Source is never
   written: the service account can only read, its session is read-only, and every statement is built from fixed
   read-only shapes.
4. **Logins.** The Source decides every login. `mysqld`'s VDB login plug-ins pass the Source's own challenge to the
   client and the answer back; the password reaches VDB only when the Source asks for it, and is then sent once over
   TLS and wiped. Accounts and privileges are mirrored from the Source and polled.
5. **Changes on the Source.** Schema changes are polled and applied; the Delta's rows are converted with MySQL's own
   conversion rules. Grants are polled; locked and dropped accounts lose their sessions.
6. **Restarts.** The Delta lives in memory. `mysqld`'s data directory is rebuilt at every start; a `mysqld` crash
   triggers the same rebuild, and committed work survives in `vdb-core`.
