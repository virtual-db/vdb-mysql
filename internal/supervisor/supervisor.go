// Package supervisor runs mysqld as vdb-mysql's child (implementation plan
// doc 12 §1, doc 02 §4): it rebuilds the data directory from the template,
// writes the option file, starts mysqld with or without networking, shuts it
// down, watches for unexpected exits, forwards its error log, and reports its
// resident memory.
package supervisor

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/virtual-db/vdb-mysql/internal/config"
)

var log = slog.With("pkg", "supervisor")

//go:embed mycnf.tmpl
var mycnfTmpl string

var tmpl = template.Must(template.New("my.cnf").Parse(mycnfTmpl))

// Options are the option file's values.
type Options struct {
	Datadir, Socket, PidFile, PluginDir, VdbSocket string
	Port                                           int
	Networking                                     bool
	SortBuffer, JoinBuffer, TmpTableSize           string
	TemptableMaxRAM                                string
	MaxConnections                                 int
	TmpDir, FilesDir, ClientCert, ClientKey        string
}

// RenderMyCnf renders the option file.
func RenderMyCnf(o Options) (string, error) {
	var b strings.Builder
	if err := tmpl.Execute(&b, o); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Supervisor owns the mysqld child. Safe for concurrent use.
type Supervisor struct {
	cfg       config.Mysqld
	vdbSocket string
	socket    string

	mu        sync.Mutex
	cmd       *exec.Cmd
	expected  bool
	exited    chan struct{}
	exits     []time.Time
	onCrash   func()
	stopping  bool
	shutdownF func(ctx context.Context) error
}

// New returns a Supervisor.
func New(cfg config.Mysqld, vdbSocket, mysqldSocket string) *Supervisor {
	return &Supervisor{cfg: cfg, vdbSocket: vdbSocket, socket: mysqldSocket}
}

// OnCrash installs the recovery called after an unexpected exit.
func (s *Supervisor) OnCrash(f func()) { s.onCrash = f }

// SetShutdown installs the control-channel SHUTDOWN.
func (s *Supervisor) SetShutdown(f func(ctx context.Context) error) { s.shutdownF = f }

// Socket returns mysqld's Unix socket path.
func (s *Supervisor) Socket() string { return s.socket }

func (s *Supervisor) options(networking bool) Options {
	return Options{Datadir: s.cfg.Datadir, Socket: s.socket, PidFile: filepath.Join(s.cfg.RunDir, "mysqld.pid"),
		PluginDir: filepath.Join(s.cfg.Basedir, "lib", "plugin"), VdbSocket: s.vdbSocket, Port: s.cfg.ListenPort,
		Networking: networking, SortBuffer: s.cfg.SortBuffer, JoinBuffer: s.cfg.JoinBuffer, TmpTableSize: s.cfg.TmpTableSize,
		TemptableMaxRAM: s.cfg.TemptableMaxRAM, MaxConnections: s.cfg.MaxConnections, TmpDir: s.cfg.TmpDir,
		FilesDir: s.cfg.FilesDir, ClientCert: s.cfg.ClientCert, ClientKey: s.cfg.ClientKey}
}

func (s *Supervisor) myCnf() string { return filepath.Join(s.cfg.Datadir, "my.cnf") }

// Prepare deletes the data directory, copies the template to it and writes
// the option file (D-50).
func (s *Supervisor) Prepare() error {
	if err := os.RemoveAll(s.cfg.Datadir); err != nil {
		return fmt.Errorf("supervisor.prepare: %w", err)
	}
	if err := copyTree(s.cfg.Template, s.cfg.Datadir); err != nil {
		return fmt.Errorf("supervisor.prepare: template: %w", err)
	}
	for _, d := range []string{s.cfg.RunDir, s.cfg.TmpDir, s.cfg.FilesDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("supervisor.prepare: %w", err)
		}
	}
	return nil
}

func (s *Supervisor) writeCnf(networking bool) error {
	text, err := RenderMyCnf(s.options(networking))
	if err != nil {
		return err
	}
	return os.WriteFile(s.myCnf(), []byte(text), 0o600)
}

// Start starts mysqld and returns when its socket accepts a connection.
func (s *Supervisor) Start(networking bool) error {
	if err := s.writeCnf(networking); err != nil {
		return fmt.Errorf("supervisor.start: %w", err)
	}
	cmd := exec.Command(filepath.Join(s.cfg.Basedir, "bin", "mysqld"), "--defaults-file="+s.myCnf())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("supervisor.start: %w", err)
	}
	exited := make(chan struct{})
	s.mu.Lock()
	s.cmd, s.expected, s.exited = cmd, false, exited
	s.mu.Unlock()
	go forward(stderr)
	go s.watch(cmd, exited)
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return errors.New("supervisor.start: mysqld exited during start-up; see the mysqld: lines above")
		default:
		}
		if c, err := net.DialTimeout("unix", s.socket, time.Second); err == nil {
			c.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("supervisor.start: mysqld did not open its socket within 2 minutes")
}

// forward turns each mysqld error-log line into a vdb-mysql log line.
func forward(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		slog.Info("mysqld: "+sc.Text(), "pkg", "mysqld")
	}
}

// watch waits for mysqld to exit; an exit not requested by Shutdown triggers
// recovery. Three exits within the restart window stop vdb-mysql (D-80).
func (s *Supervisor) watch(cmd *exec.Cmd, exited chan struct{}) {
	err := cmd.Wait()
	close(exited)
	s.mu.Lock()
	expected := s.expected || s.stopping
	now := time.Now()
	if !expected {
		var recent []time.Time
		for _, t := range s.exits {
			if now.Sub(t) < s.cfg.RestartWindow {
				recent = append(recent, t)
			}
		}
		s.exits = append(recent, now)
	}
	crashes := len(s.exits)
	s.mu.Unlock()
	if expected {
		return
	}
	log.Error("mysqld exited unexpectedly", "err", err, "exits_in_window", crashes)
	if crashes >= 3 {
		log.Error("mysqld crashed three times within the restart window; stopping so the container restarts")
		os.Exit(1)
	}
	if s.onCrash != nil {
		go s.onCrash()
	}
}

// Shutdown stops mysqld: SHUTDOWN over the control channel; SIGTERM after
// the stop timeout; SIGKILL after twice that.
func (s *Supervisor) Shutdown() error {
	s.mu.Lock()
	cmd, exited := s.cmd, s.exited
	s.expected = true
	s.mu.Unlock()
	if cmd == nil {
		return nil
	}
	select {
	case <-exited:
		return nil
	default:
	}
	if s.shutdownF != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = s.shutdownF(ctx)
		cancel()
	}
	t := s.cfg.StopTimeout
	if t <= 0 {
		t = 30 * time.Second
	}
	select {
	case <-exited:
		return nil
	case <-time.After(t):
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
		return nil
	case <-time.After(t):
	}
	_ = cmd.Process.Kill()
	<-exited
	return nil
}

// Stopping marks the supervisor as stopping for good.
func (s *Supervisor) Stopping() {
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
}

// Pid returns mysqld's pid, or 0.
func (s *Supervisor) Pid() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// MemoryProbe returns the resident memory of vdb-mysql and mysqld together.
func MemoryProbe(s *Supervisor) func() (uint64, error) {
	return func() (uint64, error) {
		self, err := VmRSS("/proc/self/status")
		if err != nil {
			return 0, err
		}
		if pid := s.Pid(); pid > 0 {
			if m, err := VmRSS("/proc/" + strconv.Itoa(pid) + "/status"); err == nil {
				self += m
			}
		}
		return self, nil
	}
}

// VmRSS reads the VmRSS line of a /proc status file, in bytes.
func VmRSS(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				n, err := strconv.ParseUint(f[1], 10, 64)
				return n * 1024, err
			}
		}
	}
	return 0, errors.New("supervisor: no VmRSS in " + path)
}

// TmpdirUse returns the bytes used under mysqld's tmpdir.
func (s *Supervisor) TmpdirUse() uint64 {
	var n uint64
	_ = filepath.WalkDir(s.cfg.TmpDir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += uint64(fi.Size())
			}
		}
		return nil
	})
	return n
}

// copyTree copies a directory tree.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			l, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(l, target)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}
