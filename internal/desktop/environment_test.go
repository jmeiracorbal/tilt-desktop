package desktop

import (
	"strings"
	"testing"

	"github.com/jmeiracorbal/tilt-desktop/internal/environment"
)

func TestFormatEnvironmentReport(t *testing.T) {
	report:=environment.Report{Checks:[]environment.CheckResult{
		{Name:"docker",Path:"/usr/bin/docker",Version:"Docker version 28.3.0",Available:true},
		{Name:"Docker daemon",Error:"permission denied"},
	}}
	output:=formatEnvironmentReport(report)
	for _, want:=range []string{"requirements are not satisfied","docker: Available","/usr/bin/docker","Docker version 28.3.0","Docker daemon: Unavailable","permission denied"} {
		if !strings.Contains(output,want){t.Fatalf("missing %q in %q",want,output)}
	}
}
