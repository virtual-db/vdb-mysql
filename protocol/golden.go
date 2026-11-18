// Package protocol — golden.go
//
// Golden examples: one encoded example of every message, written to
// testdata/ by `go test ./protocol/tests -update`, read back by the codec
// tests and by the PROTO suite's stubcore, which compares every frame vdb.so
// sends with them (doc 07 §6).
package protocol

import "google.golang.org/protobuf/proto"

// Golden is one named example frame.
type Golden struct {
	Name string
	Op   Op
	Msg  proto.Message
}

func v(s string) *Value { return &Value{B: []byte(s)} }

func row(vals ...string) *Row {
	r := &Row{}
	for _, s := range vals {
		r.Values = append(r.Values, v(s))
	}
	return r
}

// Goldens returns the examples, requests then replies.
func Goldens() []Golden {
	key := &Key{Raw: []byte{1, 0, 0, 0}, Image: []byte{0x80, 0, 0, 1}}
	rng := &Range{Low: key, LowIncl: true, LowParts: 1, High: &Key{Raw: []byte{9, 0, 0, 0}, Image: []byte{0x80, 0, 0, 9}}, HighParts: 1}
	ref := &RowRef{Origin: Origin_ORIGIN_SOURCE, Pk: key, SourceKey: []byte{1, 0, 0, 0, '1'}, Whole: row("1", "ann")}
	batch := &Batch{Rows: []*Row{row("1", "ann"), {Values: []*Value{v("2"), {Null: true}}}}, Refs: []*RowRef{ref, ref}, End: true}
	ims := &Images{Images: [][]byte{{0x80, 0, 0, 1}}}
	def := &TableDefinition{Database: "shop", Name: "users", Definition: "CREATE TABLE `users` (`id` int NOT NULL, PRIMARY KEY (`id`))",
		Columns: []*ColumnDef{{Name: "id", Type: "int", Charset: "", Collation: ""}},
		Indexes: []*IndexDef{{Name: "PRIMARY", Primary: true, Unique: true, Columns: []uint32{0}, PartLens: []uint32{4}}}}
	return []Golden{
		{"Hello", OpHello, &Hello{Version: Version, Kind: ConnKind_CONN_DATA, Build: "8.4.11/vdb.so 0.1.0", ConnId: 7}},
		{"TableOpen", OpTableOpen, &TableOpen{Database: "shop", Table: "users"}},
		{"StmtBegin", OpStmtBegin, &StmtBegin{Account: "app@%", Database: "shop", Isolation: Isolation_REPEATABLE_READ, Autocommit: true,
			ForeignKeyChecks: true, LockWaitMs: 50000, Bka: true, Eval: []*Setting{{Name: "time_zone", Value: "+00:00"}}}},
		{"StmtEnd", OpStmtEnd, &Empty{}},
		{"StmtRollback", OpStmtRollback, &Empty{}},
		{"Commit", OpCommit, &Empty{}},
		{"Rollback", OpRollback, &Empty{}},
		{"Savepoint", OpSavepoint, &Savepoint{Name: "sp1"}},
		{"RollbackTo", OpRollbackTo, &Savepoint{Name: "sp1"}},
		{"Release", OpRelease, &Savepoint{Name: "sp1"}},
		{"Snapshot", OpSnapshot, &Empty{}},
		{"ScanOpen", OpScanOpen, &ScanOpen{Table: 1, Version: 1, Index: 0, Kind: ScanKind_SCAN_PRIMARY_RANGE, Ranges: []*Range{rng},
			Columns: []uint32{0, 1}, Pushed: "(`shop`.`users`.`id` > 0)", MaxRows: 4096}},
		{"ScanNext", OpScanNext, &ScanNext{Scan: 1, Cursor: CursorKind_CURSOR_SOURCE, MaxRows: 4096}},
		{"ScanClose", OpScanClose, &ScanClose{Scan: 1}},
		{"Lookup", OpLookup, &Lookup{Table: 1, Version: 1, Keys: []*Key{key}, Columns: []uint32{0, 1}, Lock: LockMode_LOCK_EXCLUSIVE}},
		{"LookupNext", OpLookupNext, &LookupNext{Continuation: []byte{1, 2, 3}}},
		{"Count", OpCount, &Count{Table: 1}},
		{"Stats", OpStats, &Stats{Table: 1, Index: 0, Range: rng}},
		{"Insert", OpInsert, &Insert{Table: 1, Row: row("4", "dan"), Images: ims}},
		{"InsertBatch", OpInsertBatch, &InsertBatch{Table: 1, Rows: []*Row{row("4", "dan")}, Images: []*Images{ims}}},
		{"Update", OpUpdate, &Update{Table: 1, Old: ref, Row: row("1", "anna"), Images: ims}},
		{"Delete", OpDelete, &Delete{Table: 1, Old: ref}},
		{"AutoIncrement", OpAutoIncrement, &AutoIncrement{Table: 1, Count: 10}},
		{"Truncate", OpTruncate, &Truncate{Table: 1}},
		{"Catalog", OpCatalog, &Catalog{Kind: CatalogKind_CATALOG_CREATE, Table: def}},
		{"Alter", OpAlter, &Alter{Table: 1, Kinds: []AlterKind{AlterKind_ALTER_ADD_COLUMN}, Object: "c", TableDef: def, Statement: "ALTER TABLE users ADD c int"}},
		{"AlterRows", OpAlterRows, &AlterRows{Table: 1}},
		{"AlterPut", OpAlterPut, &AlterPut{Table: 1, Rows: []*Row{row("1", "ann")}, Images: []*Images{ims}}},
		{"SessionEnd", OpSessionEnd, &Empty{}},
		{"Kill", OpKill, &Kill{ConnId: 7}},
		{"AuthBegin", OpAuthBegin, &AuthBegin{User: "alice", Host: "10.0.0.5", Method: "caching_sha2_password", Secure: true, Tls: true}},
		{"AuthAnswer", OpAuthAnswer, &AuthAnswer{Answer: make([]byte, 32)}},
		{"AuthPass", OpAuthPass, &AuthPass{Password: []byte("secret")}},
		{"AuthAbort", OpAuthAbort, &Empty{}},
		{"Rewrite", OpRewrite, &Rewrite{ConnId: 7, Statement: "CREATE TABLE t (a int) ENGINE=InnoDB"}},
		{"Ping", OpPing, &Empty{}},
		{"DdlCaptured", OpDdlCaptured, &DdlCaptured{ConnId: 7, Statement: "CREATE VIEW v AS SELECT 1", Database: "shop", SqlMode: "ONLY_FULL_GROUP_BY", Definer: "app@%", Kind: 1, ObjectName: "v"}},
		{"Event", OpEvent, &Event{Kind: EventKind_EVENT_STATEMENT, ConnId: 7, Account: "app@%", Host: "10.0.0.5", Digest: "abc", DurationUs: 120, Rows: 1}},
		{"QueryReceived", OpQueryReceived, &QueryReceived{ConnId: 7, Statement: "SELECT 1", Database: "shop"}},
		{"ImagesNeeded", OpImagesNeeded, &ImagesNeeded{Table: 2, Rows: []*Row{row("10", "1")}, Database: "shop", Name: "orders"}},
		{"Waiting", OpWaiting, &Waiting{Holder: 8}},
		{"Error", OpError, &Error{Code: 1, Index: 0, Message: "Duplicate entry for key 'PRIMARY'"}},
		// Replies.
		{"HelloReply", OpHello.Reply(), &HelloReply{Version: Version, Build: "vdb-mysql 0.1.0"}},
		{"TableOpenReply", OpTableOpen.Reply(), &TableOpenReply{Table: 1, Version: 1, Kind: TableKind_KIND_MIRRORED,
			Indexes: []*IndexFlags{{Id: 0, Name: "PRIMARY", Ordered: true, EqualityProven: true}}, Fetch: []FetchMode{FetchMode_FETCH_SOURCE}}},
		{"EmptyReply", OpStmtEnd.Reply(), &Empty{}},
		{"ScanOpenReply", OpScanOpen.Reply(), &ScanOpenReply{Scan: 1, Source: batch, Delta: &Batch{End: true}}},
		{"ScanNextReply", OpScanNext.Reply(), &ScanNextReply{Batch: batch}},
		{"LookupReply", OpLookup.Reply(), &LookupReply{Batch: &Batch{Rows: []*Row{row("1", "ann")}, Refs: []*RowRef{ref}, KeyIdx: []uint32{0}, End: true}}},
		{"CountReply", OpCount.Reply(), &CountReply{Count: 1000}},
		{"StatsReply", OpStats.Reply(), &StatsReply{Records: 1000, MeanRecLength: 64, MrrLengthPerRec: 72,
			Indexes: []*IndexStats{{Index: 0, RecPerKey: []float64{1}}}, RecordsInRange: 9, BatchCost: 1.5, Estimated: true}},
		{"InsertReply", OpInsertBatch.Reply(), &InsertReply{Results: []*WriteResult{{Ok: true}}}},
		{"AutoIncrementReply", OpAutoIncrement.Reply(), &AutoIncrementReply{First: 1000001, Granted: 10}},
		{"CatalogReply", OpCatalog.Reply(), &CatalogReply{Table: 1, Version: 1}},
		{"AlterReply", OpAlter.Reply(), &AlterReply{Verdict: AlterVerdict_ALTER_OK, Version: 2}},
		{"AuthBeginReply", OpAuthBegin.Reply(), &AuthBeginReply{Scramble: make([]byte, 20), Method: "caching_sha2_password"}},
		{"AuthReply", OpAuthAnswer.Reply(), &AuthReply{Result: AuthResult_AUTH_ACCEPTED, Account: "'alice'@'10.%'", User: "alice", Host: "10.%"}},
		{"RewriteReply", OpRewrite.Reply(), &RewriteReply{Remove: []*ByteRange{{Start: 22, End: 36}}}},
		{"QueryReceivedReply", OpQueryReceived.Reply(), &QueryReceivedReply{Statement: "SELECT 1"}},
		{"ImagesReply", OpImagesNeeded, &ImagesReply{Rows: []*Images{ims}}},
	}
}
