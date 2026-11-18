package tilt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeProcess struct {
	mu           sync.Mutex
	once         sync.Once
	done         chan struct{}
	interrupts   int
	interruptErr error
	waitErr      error
}

func newFakeProcess() *fakeProcess { return &fakeProcess{done: make(chan struct{})} }
func (p *fakeProcess) Interrupt() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interrupts++
	if p.interruptErr != nil {
		return p.interruptErr
	}
	p.once.Do(func() { close(p.done) })
	return nil
}
func (p *fakeProcess) Wait() error { <-p.done; return p.waitErr }
func (p *fakeProcess) finish()     { p.once.Do(func() { close(p.done) }) }

type fakeLauncher struct {
	mu         sync.Mutex
	startCount int
	startErr   error
	processes  []*fakeProcess
	runs       [][]string
	output     []byte
	runErr     error
}

func (l *fakeLauncher) Start(_ context.Context, _ Config) (Process, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.startCount++
	if l.startErr != nil {
		return nil, l.startErr
	}
	p := newFakeProcess()
	l.processes = append(l.processes, p)
	return p, nil
}
func (l *fakeLauncher) Run(_ context.Context, _ Config, args ...string) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.runs = append(l.runs, append([]string(nil), args...))
	return l.output, l.runErr
}

func testConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Tiltfile"), []byte("# Tiltfile\n"), 0600); err != nil {
		t.Fatal(err)
	}
	kc := filepath.Join(root, "kubeconfig")
	if err := os.WriteFile(kc, []byte("apiVersion: v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return Config{ProjectID: "my-app", Directory: root, Kubeconfig: kc, ClusterName: "my-cluster", Port: 10351}
}

func TestStartStopAndIdempotency(t *testing.T) {
	config := testConfig(t)
	l := &fakeLauncher{}
	m, err := NewManager(l)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Start(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if err = m.Start(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if l.startCount != 1 {
		t.Fatalf("start count: %d", l.startCount)
	}
	state, err := m.Status(config.ProjectID)
	if err != nil || !state.Running {
		t.Fatalf("status: %+v, %v", state, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = m.Stop(ctx, config.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err = m.Stop(ctx, config.ProjectID); err != nil {
		t.Fatal(err)
	}
	state, err = m.Status(config.ProjectID)
	if err != nil || state.Running {
		t.Fatalf("status after stop: %+v, %v", state, err)
	}
	if l.processes[0].interrupts != 1 {
		t.Fatalf("interrupt count: %d", l.processes[0].interrupts)
	}
}

func TestStartRejectsConflicts(t *testing.T) {
	config := testConfig(t)
	l := &fakeLauncher{}
	m, _ := NewManager(l)
	if err := m.Start(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	changed := config
	changed.Port++
	if err := m.Start(context.Background(), changed); err == nil {
		t.Fatal("expected changed configuration error")
	}
	other := config
	other.ProjectID = "another-app"
	if err := m.Start(context.Background(), other); err == nil || !strings.Contains(err.Error(), "already assigned") {
		t.Fatalf("port collision: %v", err)
	}
	if l.startCount != 1 {
		t.Fatalf("unexpected starts: %d", l.startCount)
	}
	l.processes[0].finish()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		state, _ := m.Status(config.ProjectID)
		if !state.Running {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("wait never completed")
		}
		select {
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := m.Start(context.Background(), other); err != nil {
		t.Fatal(err)
	}
}

func TestStartAndStopErrors(t *testing.T) {
	config := testConfig(t)
	l := &fakeLauncher{startErr: errors.New("missing executable")}
	m, _ := NewManager(l)
	if err := m.Start(context.Background(), config); err == nil || !strings.Contains(err.Error(), "missing executable") {
		t.Fatalf("start error: %v", err)
	}
	if err := m.Stop(context.Background(), config.ProjectID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stop error: %v", err)
	}
	l.startErr = nil
	if err := m.Start(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	l.processes[0].interruptErr = errors.New("signal unsupported")
	if err := m.Stop(context.Background(), config.ProjectID); err == nil || !strings.Contains(err.Error(), "signal unsupported") {
		t.Fatalf("interrupt error: %v", err)
	}
	l.processes[0].interruptErr = nil
	l.processes[0].waitErr = errors.New("unexpected exit")
	l.processes[0].finish()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		state, _ := m.Status(config.ProjectID)
		if !state.Running {
			if state.ExitErr == nil || state.ExitErr.Error() != "unexpected exit" {
				t.Fatalf("exit status: %+v", state)
			}
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestObservabilityUsesInstancePort(t *testing.T) {
	config := testConfig(t)
	l := &fakeLauncher{output: []byte(`{"kind":"List","items":[{"metadata":{"name":"api"},"status":{"runtimeStatus":"ok","updateStatus":"pending"}}]}`)}
	m, _ := NewManager(l)
	if err := m.Start(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	got, err := m.Resources(context.Background(), config.ProjectID)
	if err != nil || !reflect.DeepEqual(got, []Resource{{Name: "api", RuntimeStatus: "ok", UpdateStatus: "pending"}}) {
		t.Fatalf("resources: %+v, %v", got, err)
	}
	want := []string{"get", "uiresources", "-o", "json", "--host", "127.0.0.1", "--port", "10351"}
	if !reflect.DeepEqual(l.runs[0], want) {
		t.Fatalf("resource args: %v", l.runs[0])
	}
	l.output = []byte("a log\n")
	gotLog, err := m.Logs(context.Background(), config.ProjectID, "api", 75)
	if err != nil || string(gotLog) != "a log\n" {
		t.Fatalf("logs: %q, %v", gotLog, err)
	}
	want = []string{"logs", "--host", "127.0.0.1", "--port", "10351", "--tail", "75", "api"}
	if !reflect.DeepEqual(l.runs[1], want) {
		t.Fatalf("logs args: %v", l.runs[1])
	}
}

func TestResourcesRejectMalformedResponses(t *testing.T) {
	config := testConfig(t)
	l := &fakeLauncher{}
	m, _ := NewManager(l)
	if err := m.Start(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`invalid`, `{"kind":"OtherList","items":[]}`, `{"kind":"List","items":[{}]}`} {
		l.output = []byte(payload)
		if _, err := m.Resources(context.Background(), config.ProjectID); err == nil {
			t.Fatalf("accepted %q", payload)
		}
	}
	l.runErr = errors.New("port unreachable")
	if _, err := m.Resources(context.Background(), config.ProjectID); err == nil || !strings.Contains(err.Error(), "port unreachable") {
		t.Fatalf("read error: %v", err)
	}
}

func TestLogAndConfigValidation(t *testing.T) {
	config := testConfig(t)
	l := &fakeLauncher{}
	m, _ := NewManager(l)
	if _, err := m.Logs(context.Background(), config.ProjectID, "", 3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("not found: %v", err)
	}
	if err := m.Start(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		tail int
	}{{"", 0}, {"", 1001}, {"--bad", 5}} {
		if _, err := m.Logs(context.Background(), config.ProjectID, tc.name, tc.tail); err == nil {
			t.Fatal("accepted invalid log query")
		}
	}
	invalid := []Config{
		{Directory: config.Directory, Kubeconfig: config.Kubeconfig, ClusterName: config.ClusterName, Port: config.Port},
		{ProjectID: "app", Directory: ".", Kubeconfig: config.Kubeconfig, ClusterName: config.ClusterName, Port: config.Port},
		{ProjectID: "app", Directory: config.Directory, Kubeconfig: "relative", ClusterName: config.ClusterName, Port: config.Port},
		{ProjectID: "app", Directory: config.Directory, Kubeconfig: config.Kubeconfig, ClusterName: "../../prod", Port: config.Port},
		{ProjectID: "app", Directory: config.Directory, Kubeconfig: config.Kubeconfig, ClusterName: config.ClusterName, Port: 0},
	}
	for i, c := range invalid {
		if err := m.Start(context.Background(), c); err == nil {
			t.Fatal(fmt.Sprintf("accepted invalid config %d", i))
		}
	}
}

func TestEnvironmentOverridesGlobalKubeconfig(t *testing.T) {
	t.Setenv("KUBECONFIG", "/tmp/global")
	vars := isolatedEnvironment("/private/kubeconfig")
	count := 0
	for _, e := range vars {
		if strings.HasPrefix(e, "KUBECONFIG=") {
			count++
			if e != "KUBECONFIG=/private/kubeconfig" {
				t.Fatalf("inherited global config: %q", e)
			}
		}
	}
	if count != 1 {
		t.Fatalf("kubeconfig count: %d", count)
	}
}

func TestLaunchArgumentsAreIsolated(t *testing.T) {
	c := testConfig(t)
	want := []string{"up", "--file", filepath.Join(c.Directory, "Tiltfile"), "--context", "k3d-my-cluster", "--host", "127.0.0.1", "--port", "10351"}
	if got := upArgs(c); !reflect.DeepEqual(got, want) {
		t.Fatalf("upArgs: %v", got)
	}
}

func TestStderrIsBounded(t *testing.T) {
	b := &tailBuffer{}
	input := strings.Repeat("x", stderrLimit+200) + "final"
	if _, err := b.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	if len(b.data) != stderrLimit || !strings.HasSuffix(string(b.data), "final") {
		t.Fatal("stderr tail was not retained")
	}
	if _, err := b.Write([]byte("tail")); err != nil {
		t.Fatal(err)
	}
	if len(b.data) != stderrLimit || !strings.HasSuffix(string(b.data), "tail") {
		t.Fatal("stderr tail failed to rotate")
	}
}
