package k3d

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type step struct {
	args []string
	out []byte
	err error
}
type script struct {
	t *testing.T
	steps []step
	seen int
}

func (s *script) Run(_ context.Context, args ...string) ([]byte, error) {
	s.t.Helper()
	if s.seen >= len(s.steps) {
		s.t.Fatalf("unexpected call: %v", args)
	}
	exp := s.steps[s.seen]
	s.seen++
	if !reflect.DeepEqual(exp.args, args) {
		s.t.Fatalf("call %d: got %q, want %q", s.seen, args, exp.args)
	}
	return exp.out, exp.err
}

func (s *script) done() {
	s.t.Helper()
	if s.seen != len(s.steps) {
		s.t.Fatalf("saw %d/%d calls", s.seen, len(s.steps))
	}
}

func listing(clusters ...Cluster) step {
	b, _ := json.Marshal(clusters)
	return step{args: []string{"cluster", "list", "--output", "json"}, out: b}
}

func owned(name, id string, running bool) Cluster {
	n := Node{Role: "server", RuntimeLabels: map[string]string{managedLabel: managedValue, ownerLabel: id}}
	n.State.Running = running
	return Cluster{Name: name, Nodes: []Node{n}}
}

func foreign(name string) Cluster {
	return Cluster{Name: name, Nodes: []Node{{Role: "server"}}}
}

func action(args ...string) step {
	return step{args: args}
}

func client(t *testing.T, steps ...step) (*Client, *script) {
	t.Helper()
	s := &script{t: t, steps: steps}
	c, err := NewClient(s)
	if err != nil {
		t.Fatal(err)
	}
	return c, s
}

func TestEnsureCreatesAndVerifies(t *testing.T) {
	s := Spec{Name: "sample", ManagementID: "project-123"}
	c, r := client(t, listing(), action("cluster", "create", "sample", "--wait", "--timeout", "120s",
		"--kubeconfig-update-default=false", "--kubeconfig-switch-context=false",
		"--runtime-label", managedLabel+"="+managedValue+"@all",
		"--runtime-label", ownerLabel+"=project-123@all",
		"--registry-create", "sample-registry:127.0.0.1:0"), listing(owned("sample", "project-123", true)))
	if err := c.Ensure(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	r.done()
}

func TestEnsureNoopAndResume(t *testing.T) {
	s := Spec{Name: "sample", ManagementID: "project-123"}
	t.Run("running", func(t *testing.T) {
		c, r := client(t, listing(owned("sample", s.ManagementID, true)))
		if err := c.Ensure(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		r.done()
	})
	t.Run("stopped", func(t *testing.T) {
		c, r := client(t,
			listing(owned("sample", s.ManagementID, false)),
			action("cluster", "start", "sample", "--wait", "--timeout", "120s"),
			listing(owned("sample", s.ManagementID, true)))
		if err := c.Ensure(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		r.done()
	})
}

func TestUnmanagedClusterNeverMutated(t *testing.T) {
	s := Spec{Name: "sample", ManagementID: "mine"}
	for _, op := range []struct {
		name string
		fn   func(*Client) error
	}{
		{"ensure", func(c *Client) error { return c.Ensure(context.Background(), s) }},
		{"stop", func(c *Client) error { return c.Stop(context.Background(), s) }},
		{"delete", func(c *Client) error { return c.Delete(context.Background(), s) }},
	} {
		t.Run(op.name, func(t *testing.T) {
			c, r := client(t, listing(foreign("sample")))
			err := op.fn(c)
			if !errors.Is(err, ErrUnmanaged) {
				t.Fatalf("got %v", err)
			}
			r.done()
		})
	}
}

func TestWrongManagementIDNeverMutated(t *testing.T) {
	c, r := client(t, listing(owned("sample", "someone-else", true)))
	if err := c.Ensure(context.Background(), Spec{Name: "sample", ManagementID: "mine"}); !errors.Is(err, ErrUnmanaged) {
		t.Fatalf("got %v", err)
	}
	r.done()
}

func TestStopAndDelete(t *testing.T) {
	s := Spec{Name: "sample", ManagementID: "mine"}
	t.Run("stop running", func(t *testing.T) {
		c, r := client(t, listing(owned("sample", "mine", true)), action("cluster", "stop", "sample"), listing(owned("sample", "mine", false)))
		if err := c.Stop(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		r.done()
	})
	t.Run("stop already stopped", func(t *testing.T) {
		c, r := client(t, listing(owned("sample", "mine", false)))
		if err := c.Stop(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		r.done()
	})
	t.Run("delete", func(t *testing.T) {
		c, r := client(t, listing(owned("sample", "mine", true)), action("cluster", "delete", "sample"), listing())
		if err := c.Delete(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		r.done()
	})
	t.Run("delete already missing", func(t *testing.T) {
		c, r := client(t, listing())
		if err := c.Delete(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		r.done()
	})
	t.Run("stop missing", func(t *testing.T) {
		c, r := client(t, listing())
		if err := c.Stop(context.Background(), s); !errors.Is(err, ErrNotFound) {
			t.Fatalf("got %v", err)
		}
		r.done()
	})
}

func TestKubeconfigRequiresOwnership(t *testing.T) {
	s := Spec{Name: "sample", ManagementID: "mine"}
	c, r := client(t, listing(owned("sample", "mine", true)),
		step{args: []string{"kubeconfig", "get", "sample"}, out: []byte("apiVersion: v1\n")})
	got, err := c.Kubeconfig(context.Background(), s)
	if err != nil || string(got) != "apiVersion: v1\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	r.done()
}

func TestInvalidSpecNoCalls(t *testing.T) {
	for _, s := range []Spec{
		{}, {Name: "CAPS", ManagementID: "mine"},
		{Name: "mine", ManagementID: "evil@all"},
		{Name: "-foo", ManagementID: "mine"},
	} {
		t.Run(fmt.Sprintf("%+v", s), func(t *testing.T) {
			c, r := client(t)
			if err := c.Ensure(context.Background(), s); err == nil {
				t.Fatal("expected validation error")
			}
			r.done()
		})
	}
}

func TestInvalidJSONAndErrors(t *testing.T) {
	c, r := client(t, step{args: []string{"cluster", "list", "--output", "json"}, out: []byte("invalid")})
	if _, err := c.List(context.Background()); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("got %v", err)
	}
	r.done()
	c, r = client(t, step{args: []string{"cluster", "list", "--output", "json"}, err: errors.New("runtime unavailable")})
	if err := c.Ensure(context.Background(), Spec{Name: "sample", ManagementID: "mine"}); err == nil || !strings.Contains(err.Error(), "runtime unavailable") {
		t.Fatalf("got %v", err)
	}
	r.done()
}

func TestEnsureChecksPostconditions(t *testing.T) {
	s := Spec{Name: "sample", ManagementID: "mine"}
	c, r := client(t, listing(),
		action("cluster", "create", "sample", "--wait", "--timeout", "120s",
			"--kubeconfig-update-default=false", "--kubeconfig-switch-context=false",
			"--runtime-label", managedLabel+"="+managedValue+"@all",
			"--runtime-label", ownerLabel+"=mine@all",
			"--registry-create", "sample-registry:127.0.0.1:0"),
		listing(foreign("sample")))
	if err := c.Ensure(context.Background(), s); !errors.Is(err, ErrUnmanaged) {
		t.Fatalf("got %v", err)
	}
	r.done()
}
