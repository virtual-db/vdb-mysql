// Package protocol is the plug-in protocol between vdb.so, loaded into
// mysqld, and vdb-mysql (implementation plan doc 07; product doc 04). It holds
// only the protocol, no engine logic, and is public so vdb-tests/stubcore can
// act as a fake vdb-mysql.
//
// Transport: one Unix stream socket (/run/vdb/vdb.sock, mode 0600). Every
// frame is 1 byte op, 4 bytes little-endian body length, then the body, a
// Protocol Buffers message from vdb.proto (D-81). A reply uses its request's
// op with the high bit set; an error reply is op 0xFF with an Error body.
// Before the final reply to a request that waits on a row lock, vdb-mysql may
// send one Waiting frame (0xFE) naming the holder's connection id; during a
// write it may send ImagesNeeded frames (0xFD), which vdb.so answers with an
// ImagesReply under the same op before reading on (D-86).
//
// The first message of every connection is Hello, naming its kind: data
// (one per mysqld session), login (one per login attempt), event (a pool of
// four, owned by the statements plug-in) or kill (one, shared). vdb-mysql
// accepts the current and the previous protocol version and refuses others
// with an error naming both builds. Requests on one connection are answered
// in order by one goroutine.
//
// Data replies may carry warnings (MySQL warning code and text) that the
// engine pushes to the client's session. Values are NULL or MySQL's text
// form; TIMESTAMP values are Unix time with fractions; keys carry MySQL's raw
// key format and the engine's sort image.
package protocol
