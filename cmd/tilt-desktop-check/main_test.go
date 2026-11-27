package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/jmeiracorbal/tilt-desktop/internal/environment"
)

func TestInstallerExitCodesAndJSON(t *testing.T) {
	for _, ready := range []bool{true,false} {
		var out,stderr bytes.Buffer
		report := environment.Report{Ready:ready,Checks:[]environment.CheckResult{{Name:"Docker daemon",Available:ready}}}
		code := run(context.Background(),&out,&stderr,nil,func(context.Context) environment.Report{return report})
		want := 1
		if ready { want = 0 }
		if code!=want || stderr.Len()!=0 {t.Fatalf("exit %d, stderr %s",code,stderr.String())}
		var decoded environment.Report
		if err := json.Unmarshal(out.Bytes(),&decoded);err!=nil || decoded.Ready!=ready || len(decoded.Checks)!=1 {
			t.Fatalf("invalid report: %s, %v",out.String(),err)
		}
	}
}

func TestInstallerRejectsUnexpectedArguments(t *testing.T) {
	var out,stderr bytes.Buffer
	code:=run(context.Background(),&out,&stderr,[]string{"unexpected"},func(context.Context) environment.Report{
		t.Fatal("invalid arguments invoked diagnostics")
		return environment.Report{}
	})
	if code!=2||out.Len()!=0||stderr.Len()==0{t.Fatalf("exit %d, stdout %s, stderr %s",code,out.String(),stderr.String())}
}
