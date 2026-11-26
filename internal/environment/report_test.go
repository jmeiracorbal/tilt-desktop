package environment

import (
	"context"
	"errors"
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
