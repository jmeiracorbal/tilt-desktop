package tilt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ExecLauncher runs the installed Tilt CLI directly as a child process.
type ExecLauncher struct {
	Binary string
}

func (l ExecLauncher) binary() (string, error) {
	if strings.TrimSpace(l.Binary) == "" {
		return "", errors.New("Tilt executable is required")
	}
	return l.Binary, nil
}

func isolatedEnvironment(kubeconfig string) []string {
	out := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if key, _, ok := strings.Cut(e, "="); ok && !strings.EqualFold(key, "KUBECONFIG") {
			out = append(out, e)
		}
	}
	return append(out, "KUBECONFIG="+kubeconfig)
}

func upArgs(c Config) []string {
	return []string{"up", "--file", c.tiltfile(), "--context", c.contextName(), "--host", "127.0.0.1", "--port", strconv.Itoa(int(c.Port))}
}

func (l ExecLauncher) Start(ctx context.Context, c Config) (Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	binary, err := l.binary()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binary, upArgs(c)...)
	cmd.Dir = c.Directory
	cmd.Env = isolatedEnvironment(c.Kubeconfig)
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	stderr := &tailBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("execute Tilt: %w", err)
	}
	return &execProcess{cmd: cmd, stderr: stderr}, nil
}

func (l ExecLauncher) Run(ctx context.Context, c Config, args ...string) ([]byte, error) {
	binary, err := l.binary()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = c.Directory
	cmd.Env = isolatedEnvironment(c.Kubeconfig)
	data, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("tilt %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// tailBuffer retains only the most recent diagnostic output from a long-running
// Tilt process. Cmd.Wait finishes all stderr writes before the buffer is read.
type tailBuffer struct {
	data []byte
}

const stderrLimit = 8192

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= stderrLimit {
		b.data = append(b.data[:0], p[n-stderrLimit:]...)
		return n, nil
	}
	excess := len(b.data) + n - stderrLimit
	if excess > 0 {
		b.data = b.data[excess:]
	}
	b.data = append(b.data, p...)
	return n, nil
}

type execProcess struct {
	cmd    *exec.Cmd
	stderr *tailBuffer
}

func (p *execProcess) PID() int { return p.cmd.Process.Pid }
func (p *execProcess) Interrupt() error { return p.cmd.Process.Signal(os.Interrupt) }
func (p *execProcess) Wait() error {
	if err := p.cmd.Wait(); err != nil {
		return fmt.Errorf("Tilt process exited: %w: %s", err, strings.TrimSpace(string(p.stderr.data)))
	}
	return nil
}
