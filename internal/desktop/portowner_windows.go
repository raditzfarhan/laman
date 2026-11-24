//go:build windows

package desktop

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi            = windows.NewLazySystemDLL("iphlpapi.dll")
	getExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
	user32              = windows.NewLazySystemDLL("user32.dll")
	messageBoxW         = user32.NewProc("MessageBoxW")
)

// PortOwner returns the pid and executable path of the process listening on
// a local TCP port (IPv4 first, then IPv6), or 0 when none is found.
func PortOwner(port int) (pid uint32, path string) {
	for _, af := range []uint32{windows.AF_INET, windows.AF_INET6} {
		if pid = listenerPID(af, port); pid != 0 {
			return pid, processPath(pid)
		}
	}
	return 0, ""
}

func listenerPID(af uint32, port int) uint32 {
	const tcpTableOwnerPIDListener = 3
	var size uint32
	getExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(af), tcpTableOwnerPIDListener, 0)
	if size == 0 {
		return 0
	}
	buf := make([]byte, size)
	r, _, _ := getExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(af), tcpTableOwnerPIDListener, 0)
	if r != 0 {
		return 0
	}
	n := binary.LittleEndian.Uint32(buf)
	// MIB_TCPROW_OWNER_PID: state, localAddr, localPort, remoteAddr,
	// remotePort, pid (6 x uint32). MIB_TCP6ROW_OWNER_PID: localAddr[16],
	// localScopeId, localPort, remoteAddr[16], remoteScopeId, remotePort,
	// state, pid (56 bytes).
	rowSize, portOff, pidOff := 24, 8, 20
	if af == windows.AF_INET6 {
		rowSize, portOff, pidOff = 56, 20, 52
	}
	for i := 0; i < int(n); i++ {
		row := buf[4+i*rowSize:]
		if len(row) < rowSize {
			break
		}
		// The port sits in the low 16 bits in network byte order.
		p := int(row[portOff])<<8 | int(row[portOff+1])
		if p == port {
			return binary.LittleEndian.Uint32(row[pidOff:])
		}
	}
	return 0
}

func processPath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// DescribePortOwner says in plain words who holds a port, e.g.
// "wslrelay.exe (pid 28448), which forwards a server running inside WSL".
// It returns "" when the owner can't be found.
func DescribePortOwner(port int) string {
	pid, path := PortOwner(port)
	if pid == 0 {
		return ""
	}
	return describeOwner(pid, path)
}

func describeOwner(pid uint32, path string) string {
	name := filepath.Base(path)
	if path == "" {
		name = "another program"
		if pid == 4 {
			name = "Windows (System)"
		}
	}
	s := fmt.Sprintf("%s (pid %d)", name, pid)
	lower := strings.ToLower(path)
	switch {
	case strings.EqualFold(name, "wslrelay.exe"):
		s += ", which forwards a server running inside WSL. See `wsl -l -v` and stop that server or run `wsl --shutdown`"
	case strings.Contains(lower, "docker") || strings.EqualFold(name, "vpnkit.exe"):
		s += ", a Docker container. Stop the container that publishes this port"
	case strings.Contains(lower, `\laragon\`):
		s += ", part of Laragon. Quit Laragon"
	case pid == 4:
		s += ", usually IIS or another Windows web service"
	}
	return s
}

// Alert shows a warning message box on top of other windows. It blocks
// until the user closes it, so call it from its own goroutine.
func Alert(title, text string) {
	const mbIconWarning, mbSetForeground, mbTopmost = 0x30, 0x10000, 0x40000
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	messageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbIconWarning|mbSetForeground|mbTopmost)
}
