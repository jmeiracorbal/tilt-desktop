package k3d

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
)

// RegistryPort discovers a cluster's existing registry via the k3d inventory.
// This works after the registry is stopped, because k3d retains portMappings.
func (c *Client) RegistryPort(ctx context.Context, clusterName string) (uint16, error) {
	if len(clusterName) > 63 || !validName.MatchString(clusterName) {
		return 0, fmt.Errorf("invalid cluster name %q", clusterName)
	}
	output, err := c.runner.Run(ctx, "registry", "list", "--output", "json")
	if err != nil {
		return 0, fmt.Errorf("list k3d registries: %w", err)
	}
	return parseRegistryPort(clusterName, output)
}

type registryInfo struct {
	Name string \`json:"name"\`
	RuntimeLabels map[string]string \`json:"runtimeLabels"\`
	PortMappings map[string][]struct {
		HostIP string \`json:"HostIp"\`
		HostPort string \`json:"HostPort"\`
	} \`json:"portMappings"\`
}

func parseRegistryPort(clusterName string, output []byte) (uint16, error) {
	var all []registryInfo
	if err := json.Unmarshal(output, &all); err != nil {
		return 0, fmt.Errorf("decode k3d registry inventory: %w", err)
	}
	name := clusterName + "-registry"
	for _, registry := range all {
		if registry.Name != name {
			continue
		}
		if registry.RuntimeLabels["k3d.cluster"] != clusterName {
			return 0, fmt.Errorf("registry %q is not associated with cluster %q", name, clusterName)
		}
		bindings := registry.PortMappings["5000/tcp"]
		if len(bindings) != 1 {
			return 0, fmt.Errorf("registry %q requires one published host port, got %d", name, len(bindings))
		}
		ip := net.ParseIP(bindings[0].HostIP)
		if ip == nil || !ip.IsLoopback() {
			return 0, fmt.Errorf("registry %q is not bound to loopback", name)
		}
		port, err := strconv.ParseUint(bindings[0].HostPort, 10, 16)
		if err != nil || port == 0 {
			return 0, fmt.Errorf("registry %q has invalid host port %q", name, bindings[0].HostPort)
		}
		return uint16(port), nil
	}
	return 0, fmt.Errorf("registry %q not found in k3d inventory", name)
}
