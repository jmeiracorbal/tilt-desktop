package environment

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestUnifiedReport(t *testing.T) {
	for _, fail := range []bool{false,true} {
		checker:=NewChecker()
		checker.lookup=func(name string)(string,error){
			if fail && name=="tilt" { return "",errors.New("missing Tilt") }
			return name,nil
		}
		checker.run=func(_ context.Context,path string,args ...string)(string,error){
			if path=="docker" && args[0]=="info" {
				return `{"serverVersion":"28.3.0","osType":"linux"}`,nil
			}
			return "version 1.2.3",nil
		}
		report:=checker.Check(context.Background())
		if report.Ready==fail || len(report.Checks)!=4 { t.Fatalf("unexpected report: %+v",report) }
		if report.Checks[2].Available==fail { t.Fatalf("unexpected Tilt status: %+v",report.Checks[2]) }
		if fail && !strings.Contains(report.Checks[2].Error,"missing Tilt"){t.Fatalf("missing detail: %+v",report.Checks[2])}
		if daemon:=report.Checks[3];!daemon.Available || daemon.OSType!="linux" || daemon.Version!="28.3.0"{t.Fatalf("unexpected daemon: %+v",daemon)}
	}
}

func TestRequirementsProbeOnlyRelevantDependencies(t *testing.T) {
	for _, tc:=range []struct{
		requirement Requirement
		want []string
	}{
		{StartProject,[]string{"docker --version","k3d version","tilt version","docker info"}},
		{ManageCluster,[]string{"docker --version","k3d version","docker info"}},
		{ObserveTilt,[]string{"tilt version"}},
	}{
		checker:=NewChecker()
		checker.lookup=func(name string)(string,error){return name,nil}
		var commands []string
		checker.run=func(_ context.Context,path string,args ...string)(string,error){
			commands=append(commands,path+" "+args[0])
			if args[0]=="info"{return `{"serverVersion":"28.3.0","osType":"linux"}`,nil}
			return "version 1.2.3",nil
		}
		if err:=checker.Require(context.Background(),tc.requirement);err!=nil{t.Fatal(err)}
		if !slices.Equal(commands,tc.want){t.Fatalf("requirement %d: %v, want %v",tc.requirement,commands,tc.want)}
	}
}

func TestRequirementsRejectMissingToolsAndInvalidScope(t *testing.T) {
	checker:=NewChecker()
	checker.lookup=func(name string)(string,error){
		if name=="tilt"{return "",errors.New("missing Tilt")}
		return name,nil
	}
	checker.run=func(_ context.Context,_ string,args ...string)(string,error){
		if args[0]=="info"{return `{"serverVersion":"28.3.0","osType":"linux"}`,nil}
		return "version 1.2.3",nil
	}
	if err:=checker.Require(context.Background(),ManageCluster);err!=nil{t.Fatalf("unrelated missing tool: %v",err)}
	for _,requirement:=range []Requirement{StartProject,ObserveTilt}{
		err:=checker.Require(context.Background(),requirement)
		if !errors.Is(err,ErrRequirements)||!strings.Contains(err.Error(),"missing Tilt"){t.Fatalf("requirement %d: %v",requirement,err)}
	}
	if err:=checker.Require(context.Background(),0);!errors.Is(err,ErrRequirements){t.Fatalf("invalid scope: %v",err)}
}
