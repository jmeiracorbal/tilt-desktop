package application

import (
    "fmt"
    "net"
    "strconv"
    "time"
)

// loopbackPortFree checks a previously assigned port before reusing it.
// The socket is released before Tilt binds; cross-process reservation is not yet implemented.
func loopbackPortFree(port uint16) bool {
    l,err:=net.Listen("tcp4",net.JoinHostPort("127.0.0.1",strconv.Itoa(int(port))))
    if err!=nil{return false}
    return l.Close()==nil
}

const cleanupTimeout = 90 * time.Second

// NetPorts asks the OS for an available loopback port. The probe socket is
// closed before k3d or Tilt binds, so this is not a cross-process reservation.
type NetPorts struct{}

func (NetPorts) Available() (uint16, error) {
    l, err := net.Listen("tcp4", "127.0.0.1:0")
    if err != nil { return 0, fmt.Errorf("find available loopback port: %w", err) }
    port := l.Addr().(*net.TCPAddr).Port
    if err := l.Close(); err != nil { return 0, fmt.Errorf("release loopback probe: %w", err) }
    return uint16(port), nil
}
