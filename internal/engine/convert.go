// Package engine — convert.go
//
// Conversions between protocol messages and core types.
package engine

import (
	"time"

	core "github.com/virtual-db/vdb-core"

	"github.com/virtual-db/vdb-mysql/protocol"
)

func toRow(r *protocol.Row) core.Row {
	if r == nil {
		return nil
	}
	out := make(core.Row, len(r.Values))
	for i, v := range r.Values {
		if v == nil || v.Null {
			out[i] = core.Value{Null: true}
		} else {
			out[i] = core.Value{B: v.B}
		}
	}
	return out
}

func fromRow(r core.Row) *protocol.Row {
	out := &protocol.Row{Values: make([]*protocol.Value, len(r))}
	for i, v := range r {
		if v.Null {
			out.Values[i] = &protocol.Value{Null: true}
		} else {
			out.Values[i] = &protocol.Value{B: v.B}
		}
	}
	return out
}

func toImages(im *protocol.Images) []core.KeyImage {
	if im == nil {
		return nil
	}
	out := make([]core.KeyImage, len(im.Images))
	for i, b := range im.Images {
		if len(b) > 0 {
			out[i] = b
		}
	}
	return out
}

func fromImages(ims []core.KeyImage) *protocol.Images {
	out := &protocol.Images{Images: make([][]byte, len(ims))}
	for i, b := range ims {
		out.Images[i] = b
	}
	return out
}

func toKey(k *protocol.Key) core.Key {
	if k == nil {
		return core.Key{}
	}
	return core.Key{Image: k.Image, Raw: k.Raw}
}

func toRef(r *protocol.RowRef) core.RowRef {
	if r == nil {
		return core.RowRef{}
	}
	ref := core.RowRef{PK: toKey(r.Pk), SourceKey: r.SourceKey, Whole: toRow(r.Whole)}
	if r.Origin == protocol.Origin_ORIGIN_DELTA {
		ref.Origin = core.OriginDelta
	}
	return ref
}

func fromRef(r core.RowRef) *protocol.RowRef {
	out := &protocol.RowRef{Pk: &protocol.Key{Image: r.PK.Image, Raw: r.PK.Raw}, SourceKey: r.SourceKey}
	if r.Origin == core.OriginDelta {
		out.Origin = protocol.Origin_ORIGIN_DELTA
	}
	if r.Whole != nil {
		out.Whole = fromRow(r.Whole)
	}
	return out
}

func toRange(r *protocol.Range) core.KeyRange {
	kr := core.KeyRange{}
	if r.Low != nil {
		kr.Images.Low, kr.RawLow, kr.LowParts = r.Low.Image, r.Low.Raw, uint16(r.LowParts)
		kr.Images.LowIncl, kr.RawLowIncl = r.LowIncl, r.LowIncl
	}
	if r.High != nil {
		kr.Images.High, kr.RawHigh, kr.HighParts = r.High.Image, r.High.Raw, uint16(r.HighParts)
		kr.Images.HighIncl, kr.RawHighIncl = r.HighIncl, r.HighIncl
	}
	return kr
}

func toCols(c []uint32) core.ColumnSet {
	out := make(core.ColumnSet, len(c))
	for i, x := range c {
		out[i] = uint16(x)
	}
	return out
}

func toLock(m protocol.LockMode) core.LockMode {
	switch m {
	case protocol.LockMode_LOCK_SHARED:
		return core.LockShared
	case protocol.LockMode_LOCK_EXCLUSIVE:
		return core.LockExclusive
	}
	return core.LockNone
}

func toIsolation(i protocol.Isolation) core.Isolation {
	switch i {
	case protocol.Isolation_READ_COMMITTED:
		return core.ReadCommitted
	case protocol.Isolation_READ_UNCOMMITTED:
		return core.ReadUncommitted
	case protocol.Isolation_SERIALIZABLE:
		return core.Serializable
	}
	return core.RepeatableRead
}

func toStmtContext(m *protocol.StmtBegin) core.StmtContext {
	eval := core.EvalContext{}
	for _, s := range m.Eval {
		eval[s.Name] = s.Value
	}
	return core.StmtContext{Database: m.Database, Isolation: toIsolation(m.Isolation), Autocommit: m.Autocommit,
		LockIntent: toLock(m.LockIntent), ForeignKeyChecks: m.ForeignKeyChecks,
		LockWait: time.Duration(m.LockWaitMs) * time.Millisecond, Eval: eval, ReadOnly: m.ReadOnly, Digest: m.Digest}
}

func fromBatch(b core.Batch, end bool) *protocol.Batch {
	out := &protocol.Batch{End: end}
	for i, r := range b.Rows {
		out.Rows = append(out.Rows, fromRow(r))
		if i < len(b.Refs) {
			out.Refs = append(out.Refs, fromRef(b.Refs[i]))
		}
	}
	for _, k := range b.KeyIdx {
		out.KeyIdx = append(out.KeyIdx, k)
	}
	return out
}

func kindOf(k core.TableKind) protocol.TableKind {
	switch k {
	case core.Local:
		return protocol.TableKind_KIND_LOCAL
	case core.Detached:
		return protocol.TableKind_KIND_DETACHED
	case core.Provided:
		return protocol.TableKind_KIND_PROVIDED
	}
	return protocol.TableKind_KIND_MIRRORED
}

func fetchOf(f core.FetchMode) protocol.FetchMode {
	switch f {
	case core.Default:
		return protocol.FetchMode_FETCH_DEFAULT
	case core.Virtual:
		return protocol.FetchMode_FETCH_VIRTUAL
	case core.Transformed:
		return protocol.FetchMode_FETCH_TRANSFORMED
	}
	return protocol.FetchMode_FETCH_SOURCE
}

func fkAction(a uint32) core.FKAction {
	switch a {
	case 1:
		return core.FKNoAction
	case 2:
		return core.FKCascade
	case 3:
		return core.FKSetNull
	}
	return core.FKRestrict
}
