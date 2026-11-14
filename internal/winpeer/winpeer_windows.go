//go:build windows

package winpeer

import (
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
)

const (
	afInet                     = 2
	tcpTableOwnerPidConnection = 4 // TCP_TABLE_OWNER_PID_CONNECTIONS
	loopbackV4                 = 0x0100007f
)

// tcpRow mirrors MIB_TCPROW_OWNER_PID. Addresses and ports are in network
// byte order; a port occupies the low 16 bits of its field.
type tcpRow struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32
	RemoteAddr uint32
	RemotePort uint32
	OwningPid  uint32
}

func netPort(p int) uint32 {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], uint16(p))
	return uint32(binary.LittleEndian.Uint16(b[:]))
}

// Session finds the IPv4 loopback connection whose other end uses peerPort
// and whose end on this side uses localPort, and returns the session and
// process id of the program that owns the other end.
func Session(peerPort, localPort int) (session, pid uint32, err error) {
	var size uint32
	var buf []byte
	for i := 0; i < 4; i++ {
		var p unsafe.Pointer
		if len(buf) > 0 {
			p = unsafe.Pointer(&buf[0])
		}
		r, _, _ := procGetExtendedTcpTable.Call(uintptr(p), uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPidConnection, 0)
		if r == 0 {
			break
		}
		if r != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) || size == 0 {
			return 0, 0, ErrUnavailable
		}
		buf = make([]byte, size)
	}
	if len(buf) < 4 {
		return 0, 0, ErrUnavailable
	}
	n := binary.LittleEndian.Uint32(buf[:4])
	rowSize := uint32(unsafe.Sizeof(tcpRow{}))
	if uint64(4)+uint64(n)*uint64(rowSize) > uint64(len(buf)) {
		return 0, 0, ErrUnavailable
	}
	rows := unsafe.Slice((*tcpRow)(unsafe.Pointer(&buf[4])), n)
	// From the other program's point of view its own port is the local one.
	wantLocal, wantRemote := netPort(peerPort), netPort(localPort)
	for _, row := range rows {
		if row.LocalAddr == loopbackV4 && row.RemoteAddr == loopbackV4 &&
			row.LocalPort&0xffff == wantLocal && row.RemotePort&0xffff == wantRemote {
			pid = row.OwningPid
			break
		}
	}
	if pid == 0 {
		return 0, 0, ErrUnknown
	}
	session, ok := sessionOf(pid)
	if !ok {
		return 0, pid, ErrNoSession
	}
	return session, pid, nil
}

// sessionOf returns the session of a process.
//
// ProcessIdToSessionId needs the PROCESS_QUERY_INFORMATION right on the
// process (Microsoft docs), which a service without administrator rights
// does not have for the spooler: measured on Windows Server 2019, the call
// failed and the spooler's own connection was turned away. The list of all
// processes (NtQuerySystemInformation, SystemProcessInformation) carries the
// session of every process and needs no rights on them.
func sessionOf(pid uint32) (uint32, bool) {
	var session uint32
	if windows.ProcessIdToSessionId(pid, &session) == nil {
		return session, true
	}
	size := uint32(1 << 18)
	for i := 0; i < 6; i++ {
		buf := make([]byte, size)
		var ret uint32
		err := windows.NtQuerySystemInformation(windows.SystemProcessInformation, unsafe.Pointer(&buf[0]), size, &ret)
		if err == nil {
			entry := uint32(unsafe.Sizeof(windows.SYSTEM_PROCESS_INFORMATION{}))
			for off := uint32(0); off+entry <= size; {
				p := (*windows.SYSTEM_PROCESS_INFORMATION)(unsafe.Pointer(&buf[off]))
				if uint32(p.UniqueProcessID) == pid {
					return p.SessionID, true
				}
				if p.NextEntryOffset == 0 {
					break
				}
				off += p.NextEntryOffset
			}
			return 0, false
		}
		if err != windows.STATUS_INFO_LENGTH_MISMATCH {
			return 0, false
		}
		if ret > size {
			size = ret + 1<<16
		} else {
			size *= 2
		}
	}
	return 0, false
}
