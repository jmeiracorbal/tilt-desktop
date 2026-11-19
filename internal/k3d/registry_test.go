package k3d

import "testing"

func TestParseRegistryPort(t *testing.T) {
    cases := []struct { name, input string; want uint16; valid bool }{
        {"loopback", `{"5000/tcp":[{"HostIp":"127.0.0.1","HostPort":"49321"}]}`, 49321, true},
        {"ipv6", `{"5000/tcp":[{"HostIp":"::1","HostPort":"5001"}]}`, 5001, true},
        {"wildcard", `{"5000/tcp":[{"HostIp":"0.0.0.0","HostPort":"5001"}]}`, 0, false},
        {"missing", `{}`, 0, false},
        {"zero", `{"5000/tcp":[{"HostIp":"127.0.0.1","HostPort":"0"}]}`, 0, false},
        {"invalid", `{"5000/tcp":[{"HostIp":"127.0.0.1","HostPort":"NaN"}]}`, 0, false},
        {"multiple", `{"5000/tcp":[{"HostIp":"127.0.0.1","HostPort":"5001"},{"HostIp":"::1","HostPort":"5001"}]}`, 0, false},
        {"malformed", "not-json", 0, false},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            port, err := parseRegistryPort("sample", []byte(tc.input))
            if (err == nil) != tc.valid || (tc.valid && port != tc.want) {
                t.Fatalf("port=%d error=%v", port, err)
            }
        })
    }
}
