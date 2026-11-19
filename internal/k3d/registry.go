package k3d

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "net"
    "os/exec"
    "strconv"
    "strings"
)

// RegistryPort returns the observed localhost port of an existing cluster's
// registry container without changing Docker or k3d state.
func (d DockerInspector) RegistryPort(ctx context.Context, clusterName string) (uint16, error) {
    if strings.TrimSpace(d.Binary) == "" { return 0, errors.New("Docker executable is required") }
    if len(clusterName) > 63 || !validName.MatchString(clusterName) { return 0, fmt.Errorf("invalid cluster name %q", clusterName) }
    name := "k3d-" + clusterName + "-registry"
    cmd := exec.CommandContext(ctx, d.Binary, "container", "inspect", "--format", "{{json .NetworkSettings.Ports}}", name)
    output, err := cmd.Output()
    if err != nil {
        var exit *exec.ExitError
        if errors.As(err, &exit) { return 0, fmt.Errorf("inspect registry %q: %w: %s", name, err, strings.TrimSpace(string(exit.Stderr))) }
        return 0, fmt.Errorf("inspect registry %q: %w", name, err)
    }
    return parseRegistryPort(name, output)
}

func parseRegistryPort(name string, output []byte) (uint16, error) {
    var published map[string][]struct {
        HostIP string `json:"HostIp"`
        HostPort string `json:"HostPort"`
    }
    if err := json.Unmarshal(output, &published); err != nil { return 0, fmt.Errorf("decode registry ports: %w", err) }
    bindings := published["5000/tcp"]
    if len(bindings) != 1 { return 0, fmt.Errorf("registry %q requires exactly one published port, got %d", name, len(bindings)) }
    ip := net.ParseIP(bindings[0].HostIP)
    if ip == nil || !ip.IsLoopback() { return 0, fmt.Errorf("registry %q is not bound to loopback", name) }
    port, err := strconv.ParseUint(bindings[0].HostPort, 10, 16)
    if err != nil || port == 0 { return 0, fmt.Errorf("registry %q has invalid host port %q", name, bindings[0].HostPort) }
    return uint16(port), nil
}
