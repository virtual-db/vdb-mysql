// Package config — classes.go
//
// Environment classes (doc 13 §3.3): one preset per class name. The values
// are starting points until spike S10 measures footprints (product doc 18
// §3); any single value can be set with its own variable.
package config

// Class is one environment class's sizes.
type Class struct {
	SortBuffer           string
	JoinBuffer           string
	TmpTableSize         string
	TemptableMaxRAM      string
	MaxConnections       int
	SourceMaxConnections int
}

// Classes are the known presets.
var Classes = map[string]Class{
	"small":  {SortBuffer: "256K", JoinBuffer: "256K", TmpTableSize: "16M", TemptableMaxRAM: "64M", MaxConnections: 50, SourceMaxConnections: 8},
	"medium": {SortBuffer: "1M", JoinBuffer: "1M", TmpTableSize: "64M", TemptableMaxRAM: "256M", MaxConnections: 150, SourceMaxConnections: 24},
	"large":  {SortBuffer: "2M", JoinBuffer: "2M", TmpTableSize: "256M", TemptableMaxRAM: "1G", MaxConnections: 500, SourceMaxConnections: 64},
}
