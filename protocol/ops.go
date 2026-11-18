// Package protocol — ops.go
//
// The op codes of every message (implementation plan doc 07 §3). A reply
// uses its request's op with the high bit set.
package protocol

// Op is a frame's operation code.
type Op uint8

// All connections.
const (
	OpHello Op = 0x01
)

// Data channel.
const (
	OpTableOpen     Op = 0x10
	OpStmtBegin     Op = 0x11
	OpStmtEnd       Op = 0x12
	OpStmtRollback  Op = 0x13
	OpCommit        Op = 0x14
	OpRollback      Op = 0x15
	OpSavepoint     Op = 0x16
	OpRollbackTo    Op = 0x17
	OpRelease       Op = 0x18
	OpSnapshot      Op = 0x19
	OpScanOpen      Op = 0x20
	OpScanNext      Op = 0x21
	OpScanClose     Op = 0x22
	OpLookup        Op = 0x23
	OpLookupNext    Op = 0x24
	OpCount         Op = 0x25
	OpStats         Op = 0x26
	OpInsert        Op = 0x30
	OpInsertBatch   Op = 0x31
	OpUpdate        Op = 0x32
	OpDelete        Op = 0x33
	OpAutoIncrement Op = 0x34
	OpTruncate      Op = 0x35
	OpCatalog       Op = 0x40
	OpAlter         Op = 0x41
	OpAlterRows     Op = 0x42
	OpAlterPut      Op = 0x43
	OpSessionEnd    Op = 0x50
)

// Kill connection.
const OpKill Op = 0x51

// Login channel.
const (
	OpAuthBegin  Op = 0x60
	OpAuthAnswer Op = 0x61
	OpAuthPass   Op = 0x62
	OpAuthAbort  Op = 0x63
)

// Event channel.
const (
	OpRewrite       Op = 0x70
	OpPing          Op = 0x71
	OpDdlCaptured   Op = 0x72
	OpEvent         Op = 0x73
	OpQueryReceived Op = 0x74
)

// Frames that are not replies.
const (
	OpImagesNeeded Op = 0xFD // D-86: vdb-mysql asks vdb.so for sort images mid-write
	OpWaiting      Op = 0xFE // a lock wait notice before the final reply
	OpError        Op = 0xFF // an error reply
)

// ReplyBit marks a reply.
const ReplyBit Op = 0x80

// Reply returns the reply op of a request op.
func (o Op) Reply() Op { return o | ReplyBit }

// IsReply reports whether o is a reply op (not Waiting, ImagesNeeded or Error).
func (o Op) IsReply() bool { return o&ReplyBit != 0 && o < OpImagesNeeded }

var opNames = map[Op]string{
	OpHello: "Hello", OpTableOpen: "TableOpen", OpStmtBegin: "StmtBegin", OpStmtEnd: "StmtEnd",
	OpStmtRollback: "StmtRollback", OpCommit: "Commit", OpRollback: "Rollback", OpSavepoint: "Savepoint",
	OpRollbackTo: "RollbackTo", OpRelease: "Release", OpSnapshot: "Snapshot", OpScanOpen: "ScanOpen",
	OpScanNext: "ScanNext", OpScanClose: "ScanClose", OpLookup: "Lookup", OpLookupNext: "LookupNext",
	OpCount: "Count", OpStats: "Stats", OpInsert: "Insert", OpInsertBatch: "InsertBatch", OpUpdate: "Update",
	OpDelete: "Delete", OpAutoIncrement: "AutoIncrement", OpTruncate: "Truncate", OpCatalog: "Catalog",
	OpAlter: "Alter", OpAlterRows: "AlterRows", OpAlterPut: "AlterPut", OpSessionEnd: "SessionEnd",
	OpKill: "Kill", OpAuthBegin: "AuthBegin", OpAuthAnswer: "AuthAnswer", OpAuthPass: "AuthPass",
	OpAuthAbort: "AuthAbort", OpRewrite: "Rewrite", OpPing: "Ping", OpDdlCaptured: "DdlCaptured",
	OpEvent: "Event", OpQueryReceived: "QueryReceived", OpImagesNeeded: "ImagesNeeded",
	OpWaiting: "Waiting", OpError: "Error",
}

// String names the op.
func (o Op) String() string {
	if n, ok := opNames[o]; ok {
		return n
	}
	if n, ok := opNames[o&^ReplyBit]; ok && o&ReplyBit != 0 {
		return n + "Reply"
	}
	return "Op(0x" + hex(byte(o)) + ")"
}

func hex(b byte) string {
	const d = "0123456789abcdef"
	return string([]byte{d[b>>4], d[b&15]})
}
