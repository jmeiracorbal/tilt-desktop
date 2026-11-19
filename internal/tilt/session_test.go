package tilt

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectVerifiedSession(t *testing.T) {
	c:=testConfig(t)
	l:=&fakeLauncher{output:[]byte(fmt.Sprintf(
		`{"kind":"Session","spec":{"tiltfilePath":%q},"status":{"pid":412,"startTime":"2026-11-19T03:00:00Z"}}`,
		filepath.Join(c.Directory,"Tiltfile")))}
	m,err:=NewManager(l)
	if err!=nil{t.Fatal(err)}
	got,err:=m.Inspect(context.Background(),c)
	if err!=nil{t.Fatal(err)}
	if got.PID!=412||got.TiltfilePath!=filepath.Join(c.Directory,"Tiltfile")||got.StartTime==""{
		t.Fatalf("invalid inspected identity %+v",got)
	}
	if len(l.runs)!=1||strings.Join(l.runs[0]," ")!="get session Tiltfile -o json --host 127.0.0.1 --port 10351"{
		t.Fatalf("wrong session request: %v",l.runs)
	}
}

func TestInspectRejectsForeignOrUnverifiedSession(t *testing.T) {
	c:=testConfig(t)
	for _,json := range []string{
		`{"kind":"Session","spec":{"tiltfilePath":"/other/Tiltfile"},"status":{"pid":412,"startTime":"now"}}`,
		fmt.Sprintf(`{"kind":"Session","spec":{"tiltfilePath":%q},"status":{"pid":0,"startTime":"now"}}`,filepath.Join(c.Directory,"Tiltfile")),
		fmt.Sprintf(`{"kind":"Other","spec":{"tiltfilePath":%q},"status":{"pid":412,"startTime":"now"}}`,filepath.Join(c.Directory,"Tiltfile")),
		fmt.Sprintf(`{"kind":"Session","spec":{"tiltfilePath":%q},"status":{"pid":412}}`,filepath.Join(c.Directory,"Tiltfile")),
		"invalid",
	}{
		l:=&fakeLauncher{output:[]byte(json)}
		m,err:=NewManager(l);if err!=nil{t.Fatal(err)}
		if _,err:=m.Inspect(context.Background(),c);err==nil{t.Fatalf("accepted %q",json)}
	}
}
