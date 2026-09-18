//go:build !windows && !linux && !darwin

package app

import (
	"errors"
	"net"
)

// Refusing is the only safe answer where the OS cannot be asked who connected.
var errMCPPeerUnsupported = errors.New("telling MCP clients apart is not supported on this OS")

func mcpPeerPID(net.Conn) (int, error) { return 0, errMCPPeerUnsupported }

func readMCPProc(int) (mcpProcInfo, error) { return mcpProcInfo{}, errMCPPeerUnsupported }
