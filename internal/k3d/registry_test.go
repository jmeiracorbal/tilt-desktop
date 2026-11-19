package k3d

import (
	"context"
	"testing"
)

func TestParseRegistryPort(t *testing.T) {
	cases := []struct {
		name, input string
		want uint16
		valid bool
	}{
		{"loopback", \`[{"name":"test-registry","runtimeLabels":{"k3d.cluster":"test"},"portMappings":{"5000/tcp":[{"HostIp":"127.0.0.1","HostPort":"49321"}]}}]\`, 49321, true},
		{"stopped", \`[{"name":"test-registry","State":{"Running":false},"runtimeLabels":{"k3d.cluster":"test"},"portMappings":{"5000/tcp":[{"HostIp":"127.0.0.1","HostPort":"5001"}]}}]\`, 5001, true},
		{"wrong cluster", \`[{"name":"test-registry","runtimeLabels":{"k3d.cluster":"other"},"portMappings":{"5000/tcp":[{"HostIp":"127.0.0.1","HostPort":"5001"}]}}]\`, 0, false},
		{"wrong registry", \`[{"name":"other-registry","runtimeLabels":{"k3d.cluster":"test"},"portMappings":{"5000/tcp":[{"HostIp":"127.0.0.1","HostPort":"5001"}]}}]\`, 0, false},
		{"wildcard", \`[{"name":"test-registry","runtimeLabels":{"k3d.cluster":"test"},"portMappings":{"5000/tcp":[{"HostIp":"0.0.0.0","HostPort":"5001"}]}}]\`, 0, false},
		{"missing", \`[]\`, 0, false},
		{"zero", \`[{"name":"test-registry","runtimeLabels":{"k3d.cluster":"test"},"portMappings":{"5000/tcp":[{"HostIp":"127.0.0.1","HostPort":"0"}]}}]\`, 0, false},
		{"multiple", \`[{"name":"test-registry","runtimeLabels":{"k3d.cluster":"test"},"portMappings":{"5000/tcp":[{"HostIp":"127.0.0.1","HostPort":"5001"},{"HostIp":"::1","HostPort":"5001"}]}}]\`, 0, false},
		{"malformed", "not-json", 0, false},
	}
	for _,tc := range cases {
		t.Run(tc.name,func(t *testing.T){
			port,err:=parseRegistryPort("test",[]byte(tc.input))
			if (err==nil)!=tc.valid || (tc.valid && port!=tc.want){t.Fatalf("port=%d err=%v",port,err)}
		})
	}
}

func TestRegistryPortUsesK3dInventory(t *testing.T) {
	input := []byte(\`[{"name":"sample-registry","runtimeLabels":{"k3d.cluster":"sample"},"portMappings":{"5000/tcp":[{"HostIp":"127.0.0.1","HostPort":"49321"}]}}]\`)
	c,r:=client(t,"mine",step{args:[]string{"registry","list","--output","json"},out:input})
	port,err:=c.RegistryPort(context.Background(),"sample")
	if err!=nil || port!=49321{t.Fatalf("got %d, %v",port,err)}
	r.done()
}
