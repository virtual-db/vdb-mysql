// Package safety is the safety checker (implementation plan doc 15 §4): it
// runs after every suite run and fails the run when the Source saw anything
// but VDB's read-only shapes, when a value written through VDB reached the
// Source, or when a password appears in a log.
package safety

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
)

// Allowed are the first keywords of every Source statement shape (product doc 08 §5).
var Allowed = []string{"SELECT", "(SELECT", "EXPLAIN", "SHOW", "SET", "START TRANSACTION", "COMMIT", "ROLLBACK"}

var (
	mu          sync.Mutex
	deltaValues []string
	passwords   []string
)

// RecordDeltaValue remembers a value a test wrote through VDB that never
// existed on the Source; it must never appear in a Source statement.
func RecordDeltaValue(v string) {
	if len(v) < 6 {
		return // too short to search for without false alarms
	}
	mu.Lock()
	deltaValues = append(deltaValues, v)
	mu.Unlock()
}

// RecordPassword remembers a password the run used; it must appear in no log.
func RecordPassword(p string) {
	mu.Lock()
	passwords = append(passwords, p)
	mu.Unlock()
}

var lineRe = regexp.MustCompile(`^\S+\s+(\d+)\s+(\w+)\s?(.*)$`)
var commentRe = regexp.MustCompile(`^/\*.*?\*/\s*`)

// Finding is one safety violation.
type Finding struct{ Check, Detail string }

// CheckGeneralLog reads the Source's general log and checks every statement
// the service account sent.
func CheckGeneralLog(path, serviceUser string) ([]Finding, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Finding
	svc := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		m := lineRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		id, cmd, arg := m[1], m[2], m[3]
		switch cmd {
		case "Connect":
			svc[id] = strings.HasPrefix(arg, serviceUser+"@")
		case "Query":
			if !svc[id] {
				continue
			}
			stmt := strings.TrimSpace(commentRe.ReplaceAllString(arg, ""))
			up := strings.ToUpper(stmt)
			ok := false
			for _, a := range Allowed {
				if strings.HasPrefix(up, a) {
					ok = true
					break
				}
			}
			if strings.HasPrefix(up, "SET") && !strings.HasPrefix(up, "SET SESSION") && !strings.HasPrefix(up, "SET NAMES") &&
				!strings.HasPrefix(up, "SET AUTOCOMMIT") && !strings.HasPrefix(up, "SET TRANSACTION") &&
				!strings.HasPrefix(up, "SET CHARACTER_SET") && !strings.HasPrefix(up, "SET MAX_EXECUTION_TIME") &&
				!strings.HasPrefix(up, "SET TRANSACTION_READ_ONLY") {
				ok = false
			}
			if !ok {
				out = append(out, Finding{"shape", trim(stmt)})
			}
			mu.Lock()
			for _, v := range deltaValues {
				if strings.Contains(stmt, v) {
					out = append(out, Finding{"delta value reached the Source", trim(stmt)})
				}
			}
			mu.Unlock()
		}
	}
	return out, sc.Err()
}

// CheckLogs searches log files for every recorded password.
func CheckLogs(paths ...string) []Finding {
	mu.Lock()
	pw := append([]string(nil), passwords...)
	mu.Unlock()
	var out []Finding
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, w := range pw {
			if w != "" && strings.Contains(string(b), w) {
				out = append(out, Finding{"password in a log", fmt.Sprintf("%s contains a password used by the run", p)})
			}
		}
	}
	return out
}

func trim(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
