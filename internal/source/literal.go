// Package source — literal.go
//
// Rendering values as SQL literals for Source statements (doc 10 §4.1).
// Strings carry their character set introducer and COLLATE clause, so the
// Source compares them exactly as mysqld would; binary values are hex
// literals; numbers are checked to contain only number characters; TIMESTAMP
// values travel as Unix time and render as FROM_UNIXTIME. The same code
// renders vdb.delta_sql's values.
package source

import (
	"encoding/hex"
	"strings"

	core "github.com/virtual-db/vdb-core"
)

// Kind is a column's literal kind.
type Kind uint8

const (
	KindString Kind = iota
	KindBinary
	KindNumber
	KindTemporal
	KindTimestamp
	KindBit
	KindJSON
	KindOther
)

// KindOf classifies a column by its SQL type and character set.
func KindOf(c core.ColumnDef) Kind {
	t := strings.ToLower(c.Type)
	base := t
	if i := strings.IndexAny(t, "( "); i >= 0 {
		base = t[:i]
	}
	switch base {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint", "decimal", "numeric", "float", "double", "real", "year":
		return KindNumber
	case "bit":
		return KindBit
	case "timestamp":
		return KindTimestamp
	case "date", "datetime", "time":
		return KindTemporal
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob", "geometry", "point", "linestring", "polygon":
		return KindBinary
	case "json":
		return KindJSON
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext", "enum", "set":
		if c.Charset == "binary" {
			return KindBinary
		}
		return KindString
	}
	return KindOther
}

// Literal renders one value for column c.
func Literal(c core.ColumnDef, v core.Value) string {
	if v.Null {
		return "NULL"
	}
	switch KindOf(c) {
	case KindNumber:
		if isNumber(v.B) {
			return string(v.B)
		}
		return "X'" + hex.EncodeToString(v.B) + "'"
	case KindTimestamp:
		if isNumber(v.B) {
			return "FROM_UNIXTIME(" + string(v.B) + ")"
		}
		return quote(v.B)
	case KindTemporal:
		return quote(v.B)
	case KindBinary, KindBit:
		return "X'" + hex.EncodeToString(v.B) + "'"
	case KindJSON:
		return "CAST(_utf8mb4 X'" + hex.EncodeToString(v.B) + "' AS JSON)"
	case KindString:
		cs := c.Charset
		if cs == "" {
			cs = "utf8mb4"
		}
		s := "_" + cs + " X'" + hex.EncodeToString(v.B) + "'"
		if c.Collation != "" {
			s += " COLLATE " + c.Collation
		}
		return s
	}
	return quote(v.B)
}

func isNumber(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for i, ch := range b {
		switch {
		case ch >= '0' && ch <= '9', ch == '.', ch == 'e', ch == 'E':
		case (ch == '-' || ch == '+') && (i == 0 || b[i-1] == 'e' || b[i-1] == 'E'):
		default:
			return false
		}
	}
	return true
}

// quote renders a string literal with every special character escaped.
func quote(b []byte) string {
	var sb strings.Builder
	sb.WriteByte('\'')
	for _, ch := range b {
		switch ch {
		case '\'':
			sb.WriteString("''")
		case '\\':
			sb.WriteString("\\\\")
		case 0:
			sb.WriteString("\\0")
		case '\n':
			sb.WriteString("\\n")
		case '\r':
			sb.WriteString("\\r")
		case 26:
			sb.WriteString("\\Z")
		default:
			sb.WriteByte(ch)
		}
	}
	sb.WriteByte('\'')
	return sb.String()
}

// Ident quotes an identifier with backticks, doubling backticks inside.
func Ident(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
