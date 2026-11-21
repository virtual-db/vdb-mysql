// Command oracle runs the oracle (plan doc 15 §3) against a running Source,
// VDB and reference: first every regression file, then every mode over the
// seeds, then the safety checker over the Source's general log. It exits 0
// when there is no mismatch and the Source is untouched, 1 on a mismatch or a
// safety finding, 2 when it cannot run.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/virtual-db/vdb-mysql/oracle"
	"github.com/virtual-db/vdb-mysql/oracle/safety"
)

func main() {
	seeds := flag.Int("seeds", 5, "number of seeds, from 1")
	seedsFile := flag.String("seeds-file", "", "file of seed numbers, one per line (overrides -seeds)")
	steps := flag.Int("steps", 200, "steps per seed")
	sourceDSN := flag.String("source", env("ORACLE_SOURCE_DSN", "test_admin:testadmin@tcp(127.0.0.1:13307)/testdb?tls=skip-verify"), "Source DSN")
	vdbDSN := flag.String("vdb", env("ORACLE_VDB_DSN", "test_admin:testadmin@tcp(127.0.0.1:13306)/testdb?tls=skip-verify"), "VDB DSN")
	refDSN := flag.String("reference", env("ORACLE_REFERENCE_DSN", "test_admin:testadmin@tcp(127.0.0.1:13308)/testdb"), "reference DSN")
	generalLog := flag.String("general-log", env("ORACLE_GENERAL_LOG", ""), "the Source's general log, for the safety checker")
	out := flag.String("out", "out", "directory for the report and new regression files")
	modes := flag.String("modes", strings.Join(oracle.DefaultModes, ","), "modes: single, source, concurrency")
	prepared := flag.Float64("prepared", 0.25, "share of statements run as prepared statements")
	regressions := flag.String("regressions", "regressions", "directory of regression files to replay first")
	replay := flag.String("replay", "", "replay one regression file and exit")
	run := flag.String("run", "", "table-name run id (default: from the clock)")
	flag.Parse()

	list, err := seedList(*seeds, *seedsFile)
	if err != nil {
		fatal(err)
	}
	for _, d := range []string{*sourceDSN, *vdbDSN, *refDSN} {
		if err := oracle.WaitReady(d, 3*time.Minute); err != nil {
			fatal(err)
		}
	}
	source, err := oracle.Open(*sourceDSN)
	if err != nil {
		fatal(err)
	}
	vdb, err := oracle.Open(*vdbDSN)
	if err != nil {
		fatal(err)
	}
	ref, err := oracle.Open(*refDSN)
	if err != nil {
		fatal(err)
	}
	env := oracle.Env{Source: source, VDB: vdb, Reference: ref,
		WaitTables: func(prefix string) error {
			return oracle.WaitTablesLike(source, vdb, prefix+"\\_%", 30*time.Second)
		}}
	code := 0
	runID := *run
	if runID == "" {
		runID = fmt.Sprintf("%x", time.Now().UnixNano()%0xfffffff)
	}
	files := []string{*replay}
	if *replay == "" {
		files, _ = filepath.Glob(filepath.Join(*regressions, "*.sql"))
	}
	for _, f := range files {
		m, err := oracle.Replay(env, f, runID)
		if err != nil {
			fatal(fmt.Errorf("%s: %w", f, err))
		}
		if m != nil {
			fmt.Printf("regression %s fails: step %d, %s differ\n  vdb:       %+v\n  reference: %+v\n", f, m.Step, m.What, m.VDB, m.Ref)
			code = 1
		} else {
			fmt.Printf("regression %s passes\n", f)
		}
	}
	if *replay != "" {
		os.Exit(code)
	}
	cfg := oracle.Config{Run: runID, Steps: *steps, Modes: strings.Split(*modes, ","), Prepared: *prepared,
		Regressions: filepath.Join(*out, "regressions")}
	rep, err := oracle.RunWith(env, list, cfg)
	if rep != nil {
		fmt.Print(rep.String())
		_ = os.MkdirAll(*out, 0o755)
		_ = os.WriteFile(filepath.Join(*out, "report.txt"), []byte(rep.String()), 0o644)
	}
	if err != nil {
		fatal(err)
	}
	if len(rep.Mismatches) > 0 {
		code = 1
	}
	if *generalLog != "" {
		safety.RecordPassword("testadmin")
		safety.RecordPassword("vdbsvc")
		findings, err := safety.CheckGeneralLog(*generalLog, "vdb_svc")
		if err != nil {
			fmt.Fprintf(os.Stderr, "safety: cannot read %s: %v\n", *generalLog, err)
			code = 1
		}
		if len(findings) == 0 && err == nil {
			fmt.Println("safety: clean")
		}
		for _, f := range findings {
			fmt.Printf("safety: [%s] %s\n", f.Check, f.Detail)
			code = 1
		}
	}
	os.Exit(code)
}

func seedList(n int, file string) ([]int64, error) {
	var list []int64
	if file == "" {
		for i := 1; i <= n; i++ {
			list = append(list, int64(i))
		}
		return list, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		v, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a seed", file, line)
		}
		list = append(list, v)
	}
	return list, sc.Err()
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "oracle: %v\n", err)
	os.Exit(2)
}
