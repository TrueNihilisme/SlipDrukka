//go:build windows

package winsvc

// The data folder holds the TLS key, point keys and password hashes.
//
// %ProgramData% lets every user create folders, so an ordinary user of a
// terminal server could create "SlipDrukka\server" before the program is
// installed: they would own it, keep access whatever permissions are set
// later, and could leave a settings file of their own in it. A folder can
// also be planted as a link to some other folder, so that the permissions
// set here land on the wrong place. Therefore, whenever this runs with
// administrator rights:
//
//   - a link in place of the folder is removed;
//   - a folder that belongs to someone who is not an administrator is put
//     aside (renamed) and a new, empty one is used;
//   - the folders get Administrators as owner and a closed list of who may
//     enter: SYSTEM, Administrators and the service's own account;
//   - everything inside is reset to those same permissions.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	daclAdminsOnly = "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	fileModify     = "0x1301bf" // read, write, execute, delete: no change of permissions or owner
	trustedInstSID = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
)

// SecureDir creates dir and closes it to everyone but SYSTEM,
// Administrators and the account of service (if that service exists).
// Without administrator rights it only makes sure the folder exists: the
// service under its own account relies on what was set up before.
func SecureDir(dir, service string) error {
	if !Privileged() {
		return os.MkdirAll(dir, 0o700)
	}
	serviceSID, _ := accountSID(AccountName(service))
	parent := filepath.Dir(dir)
	if err := claimDir(parent, serviceSID, false); err != nil {
		return err
	}
	if err := setDirSecurity(parent, daclAdminsOnly); err != nil {
		return fmt.Errorf("%s: %w", parent, err)
	}
	if err := claimDir(dir, serviceSID, true); err != nil {
		return err
	}
	dacl := daclAdminsOnly
	if serviceSID != "" {
		dacl += "(A;OICI;" + fileModify + ";;;" + serviceSID + ")"
	}
	if err := setDirSecurity(dir, dacl); err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	return resetChildren(dir)
}

func isReparsePoint(path string) bool {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attrs, err := windows.GetFileAttributes(p)
	return err == nil && attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// claimDir makes sure path is a real folder that an administrator owns.
// With quarantine, a folder owned by anyone else is renamed aside.
func claimDir(path, serviceSID string, quarantine bool) error {
	if isReparsePoint(path) {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("%s — посилання на іншу теку, прибрати не вдалося: %w", path, err)
		}
	}
	fi, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
		return os.MkdirAll(path, 0o700)
	case err != nil:
		return err
	case !fi.IsDir():
		return fmt.Errorf("%s — не тека", path)
	}
	if quarantine && !trustedOwner(path, serviceSID) {
		aside := path + ".foreign-" + time.Now().Format("20060102-150405")
		if err := os.Rename(path, aside); err != nil {
			return fmt.Errorf("%s створив не адміністратор, відкласти її не вдалося: %w", path, err)
		}
		_ = setDirSecurity(aside, daclAdminsOnly)
		return os.MkdirAll(path, 0o700)
	}
	return nil
}

// trustedOwner: SYSTEM, Administrators, TrustedInstaller, the service's own
// account, or a member of the local Administrators group.
func trustedOwner(path, serviceSID string) bool {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return false
	}
	if owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return true
	}
	if s := owner.String(); s == trustedInstSID || (serviceSID != "" && s == serviceSID) {
		return true
	}
	return isLocalAdmin(owner)
}

var (
	netapi32                    = windows.NewLazySystemDLL("netapi32.dll")
	procNetLocalGroupGetMembers = netapi32.NewProc("NetLocalGroupGetMembers")
)

// isLocalAdmin reports whether sid is listed in the local Administrators
// group (direct members only).
func isLocalAdmin(sid *windows.SID) bool {
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	group, _, _, err := admins.LookupAccount("") // the group's name is localised
	if err != nil {
		return false
	}
	g, err := windows.UTF16PtrFromString(group)
	if err != nil {
		return false
	}
	var buf *byte
	var read, total uint32
	const maxPreferredLength = 0xFFFFFFFF
	r, _, _ := procNetLocalGroupGetMembers.Call(0, uintptr(unsafe.Pointer(g)), 0, uintptr(unsafe.Pointer(&buf)),
		maxPreferredLength, uintptr(unsafe.Pointer(&read)), uintptr(unsafe.Pointer(&total)), 0)
	if r != 0 || buf == nil {
		return false
	}
	defer windows.NetApiBufferFree(buf)
	// LOCALGROUP_MEMBERS_INFO_0 is a single PSID.
	for _, member := range unsafe.Slice((**windows.SID)(unsafe.Pointer(buf)), read) {
		if member != nil && member.Equals(sid) {
			return true
		}
	}
	return false
}

// setDirSecurity makes Administrators the owner of path and replaces its
// access list with dacl (SDDL), cutting off anything inherited from above.
func setDirSecurity(path, dacl string) error {
	sd, err := windows.SecurityDescriptorFromString(dacl)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		admins, nil, acl, nil)
}

// resetChildren gives everything inside dir the folder's own permissions
// and Administrators as owner, and removes links.
func resetChildren(dir string) error {
	// An empty access list (ACL header only: revision 2, size 8, no entries),
	// so that only the entries inherited from the folder remain. It must be a
	// real empty list: a missing one would mean "everyone may do everything".
	raw := [8]byte{2, 0, 8, 0, 0, 0, 0, 0}
	empty := (*windows.ACL)(unsafe.Pointer(&raw[0]))
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	// The service may be working in the folder meanwhile (queued jobs come
	// and go): a file that disappeared is not an error.
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if path == dir {
			return nil
		}
		if isReparsePoint(path) {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("%s — посилання, прибрати не вдалося: %w", path, err)
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
			admins, nil, empty, nil); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("%s: %w", path, err)
		}
		return nil
	})
}
