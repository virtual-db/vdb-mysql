# Known limitations

| Behaviour | Why |
|---|---|
| A Source row updated in VDB is frozen: later Source changes to it are not seen | The Delta holds the whole row (D-53) |
| A Source insert of a key the Delta already holds stays hidden, with a warning | The Delta wins on equal keys (D-33) |
| The Source's own later writes are not locked or checked against VDB's foreign keys | VDB checks every write it makes, in a snapshot, but cannot stop the Source |
| Source triggers do not fire in VDB | They are not copied (D-65) |
| `ALTER TABLE` that needs a table copy on a Source table (renaming a column, changing a type or the primary key) is refused | A copy would move the whole Source table into the Delta (D-45) |
| Indexes in collations not proven for order are read unordered; tables keyed by a collation not proven for equality accept only inserts of Source rows | Sort images must reproduce MySQL's comparisons exactly (D-59, D-62) |
| New rows of Source tables get AUTO_INCREMENT values above a gap | So they never collide with the Source's later inserts (D-15) |
| XA transactions and events are refused | Not supported in v1 |
| FULLTEXT and spatial indexes are removed, with a warning | Not supported in v1 |
| Writes past the memory reserve fail with error 1114 | The Delta lives in memory (D-30) |
