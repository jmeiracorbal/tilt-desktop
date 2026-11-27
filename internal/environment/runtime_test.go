package environment

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func TestCheckRuntime(t *testing.T) {
	denied := errors.New("permission denied connecting to Docker daemon")
	unreachable := errors.New("cannot connect to Docker daemon")
	cases := []struct {
		name, output, osType, version string
		lookupErr, runErr, wantErr error
		reachable, available bool
	}{
		{name:"linux", output:`{"serverVersion":"28.3.0","osType":"linux"}`, osType:"linux", version:"28.3.0", reachable:true, available:true},
		{name:"windows", output:`{"serverVersion":"28.3.0","osType":"windows"}`, osType:"windows", version:"28.3.0", reachable:true, wantErr:ErrUnsupportedRuntimeOS},
		{name:"missing CLI", lookupErr:exec.ErrNotFound, wantErr:exec.ErrNotFound},
		{name:"permission denied", runErr:denied, wantErr:denied},
		{name:"unreachable daemon", runErr:unreachable, wantErr:unreachable},
		{name:"missing server version", output:`{"serverVersion":"","osType":"linux"}`, osType:"linux", wantErr:ErrInvalidRuntimeInfo},
		{name:"missing OS", output:`{"serverVersion":"28.3.0"}`, version:"28.3.0", wantErr:ErrInvalidRuntimeInfo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checker := NewChecker()
			checker.lookup = func(name string) (string,error) {
				if name != "docker" { t.Fatalf("unexpected lookup: %s", name) }
				if tc.lookupErr != nil { return "",tc.lookupErr }
				return "resolved-docker",nil
			}
			var invoked bool
			checker.run = func(_ context.Context, path string, args ...string) (string,error) {
				invoked = true
				if path != "resolved-docker" || !reflect.DeepEqual(args, []string{"info","--format",runtimeInfoFormat}) {
					t.Fatalf("unexpected probe: %s %v",path,args)
				}
				return tc.output,tc.runErr
			}
			result := checker.CheckRuntime(context.Background())
			if invoked != (tc.lookupErr == nil) { t.Fatalf("unexpected command invocation: %t",invoked) }
			if !errors.Is(result.Err,tc.wantErr) || result.Available()!=tc.available ||
				result.Reachable!=tc.reachable || result.OSType!=tc.osType || result.ServerVersion!=tc.version {
				t.Fatalf("unexpected result: %+v, want %+v",result,tc)
			}
		})
	}
}

func TestCheckRuntimeRejectsInvalidJSON(t *testing.T) {
	checker := NewChecker()
	checker.lookup = func(string)(string,error){return "docker",nil}
	for _, value := range []string{"not JSON", `{"serverVersion":32,"osType":"linux"}`, `{"serverVersion":"28.3.0","osType":"linux"} trailing`} {
		checker.run = func(context.Context,string,...string)(string,error){return value,nil}
		result := checker.CheckRuntime(context.Background())
		if result.Err==nil || result.Available() || result.Reachable {
			t.Fatalf("accepted invalid output %q: %+v",value,result)
		}
	}
}

func TestCheckRuntimeHonorsDeadlineAndCancellation(t *testing.T) {
	checker := NewChecker()
	checker.timeout = 10*time.Millisecond
	checker.lookup = func(string)(string,error){return "docker",nil}
	checker.run = func(ctx context.Context,_ string,_ ...string)(string,error){
		<-ctx.Done()
		return "",ctx.Err()
	}
	result := checker.CheckRuntime(context.Background())
	if !errors.Is(result.Err,context.DeadlineExceeded) || result.Available(){
		t.Fatalf("timeout not reported: %+v",result)
	}
	ctx,cancel := context.WithCancel(context.Background())
	cancel()
	checker.lookup = func(string)(string,error){t.Fatal("lookup after cancellation");return "",nil}
	result = checker.CheckRuntime(ctx)
	if !errors.Is(result.Err,context.Canceled) || result.Available(){
		t.Fatalf("cancellation not reported: %+v",result)
	}
}
