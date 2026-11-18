// Package engine — errors.go
//
// core.Error to protocol errors (doc 07 §4). The C++ maps each code to its
// HA_ERR_* value in one table (vdb-mysql-plugins/src/common/errors.cc).
package engine

import (
	"errors"

	core "github.com/virtual-db/vdb-core"

	"github.com/virtual-db/vdb-mysql/protocol"
)

// toError turns any error into a protocol error. Source errors keep their
// retryable message naming the table.
func toError(err error) *protocol.Error {
	var ce *core.Error
	if errors.As(err, &ce) {
		return &protocol.Error{Code: uint32(ce.Code), Index: uint32(ce.Index), Constraint: ce.Constraint, Holder: ce.Holder, Message: ce.Msg}
	}
	return &protocol.Error{Code: uint32(core.ErrInternal), Message: err.Error()}
}
