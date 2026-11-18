// Package collations holds the list of collations proven for order and for
// equality per mysqld release (implementation plan doc 12 §4.3, D-59, D-62).
// Columns without a collation (numbers, dates, binary strings) and binary
// collations are always proven.
package collations

import (
	_ "embed"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed proven.yaml
var provenYAML []byte

// Release is one mysqld release's proven collations.
type Release struct {
	Order    []string `yaml:"order"`
	Equality []string `yaml:"equality"`
}

type file struct {
	Releases map[string]Release `yaml:"releases"`
}

// Proven answers whether a collation is proven on one release.
type Proven struct {
	order, equality map[string]bool
}

// For returns the proven lists of a mysqld release (an exact version, or the
// longest listed prefix of it).
func For(release string) *Proven {
	p := &Proven{order: map[string]bool{}, equality: map[string]bool{}}
	var f file
	if err := yaml.Unmarshal(provenYAML, &f); err != nil {
		return p
	}
	best := ""
	for r := range f.Releases {
		if strings.HasPrefix(release, r) && len(r) > len(best) {
			best = r
		}
	}
	if best == "" {
		return p
	}
	for _, c := range f.Releases[best].Order {
		p.order[strings.ToLower(c)] = true
	}
	for _, c := range f.Releases[best].Equality {
		p.equality[strings.ToLower(c)] = true
	}
	return p
}

func binary(c string) bool {
	c = strings.ToLower(c)
	return c == "" || c == "binary" || strings.HasSuffix(c, "_bin")
}

// Order reports whether image order equals ORDER BY order for a collation.
func (p *Proven) Order(c string) bool { return binary(c) || p.order[strings.ToLower(c)] }

// Equality reports whether image equality equals = for a collation.
func (p *Proven) Equality(c string) bool { return binary(c) || p.equality[strings.ToLower(c)] }
