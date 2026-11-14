//go:build windows

// Package winauth checks a Windows user name and password and whether that
// account is a local administrator. The server panel uses it instead of its
// own password, the way Windows Admin Center does.
//
// Microsoft docs used: LogonUser (LOGON32_LOGON_INTERACTIVE needs the "log on
// locally" right, administrators have it on servers), TOKEN_ELEVATION_TYPE
// (with UAC an admin gets a limited token linked to the full one),
// TokenLinkedToken, CheckTokenMembership (needs an impersonation token).
package winauth

import (
	"errors"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	advapi32           = windows.NewLazySystemDLL("advapi32.dll")
	procLogonUserW     = advapi32.NewProc("LogonUserW")
	errLogonTypeDenied = windows.Errno(1385) // ERROR_LOGON_TYPE_NOT_GRANTED
)

const (
	logonInteractive = 2
	logonNetwork     = 3
	providerDefault  = 0
	elevationLimited = 3 // TokenElevationTypeLimited
)

func logon(user, domain, password string, kind uintptr) (windows.Token, error) {
	u, err := windows.UTF16PtrFromString(user)
	if err != nil {
		return 0, err
	}
	var d *uint16
	if domain != "" {
		if d, err = windows.UTF16PtrFromString(domain); err != nil {
			return 0, err
		}
	}
	p, err := windows.UTF16PtrFromString(password)
	if err != nil {
		return 0, err
	}
	var tok windows.Token
	r, _, e := procLogonUserW.Call(uintptr(unsafe.Pointer(u)), uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(p)),
		kind, providerDefault, uintptr(unsafe.Pointer(&tok)))
	if r == 0 {
		return 0, e
	}
	return tok, nil
}

// CheckAdmin verifies the password and that the account is in the local
// Administrators group. user may be "name", ".\name", "DOMAIN\name" or
// "name@domain".
func CheckAdmin(user, password string) error {
	user = strings.TrimSpace(user)
	if user == "" || password == "" {
		return errors.New("введіть ім'я та пароль адміністратора Windows")
	}
	domain := "."
	if i := strings.IndexByte(user, '\\'); i >= 0 {
		domain, user = user[:i], user[i+1:]
		if domain == "" {
			domain = "."
		}
	} else if strings.Contains(user, "@") {
		domain = "" // UPN: the domain is part of the name
	}
	tok, err := logon(user, domain, password, logonInteractive)
	if errors.Is(err, errLogonTypeDenied) {
		tok, err = logon(user, domain, password, logonNetwork)
	}
	if err != nil {
		switch err {
		case windows.Errno(1326): // ERROR_LOGON_FAILURE
			return errors.New("невірне ім'я або пароль")
		case windows.Errno(1327): // ERROR_ACCOUNT_RESTRICTION (e.g. empty password)
			return errors.New("цей обліковий запис не можна використати для входу (можливо, без пароля)")
		case windows.Errno(1909): // ERROR_ACCOUNT_LOCKED_OUT
			return errors.New("обліковий запис Windows заблоковано")
		case windows.Errno(1331): // ERROR_ACCOUNT_DISABLED
			return errors.New("обліковий запис Windows вимкнено")
		}
		return errors.New("вхід Windows не вдався: " + err.Error())
	}
	defer tok.Close()

	check := tok
	var et uint32
	var n uint32
	if windows.GetTokenInformation(tok, windows.TokenElevationType, (*byte)(unsafe.Pointer(&et)), 4, &n) == nil && et == elevationLimited {
		if linked, err := tok.GetLinkedToken(); err == nil {
			defer linked.Close()
			check = linked
		}
	}
	var imp windows.Token
	if err := windows.DuplicateTokenEx(check, windows.TOKEN_QUERY, nil, windows.SecurityIdentification, windows.TokenImpersonation, &imp); err != nil {
		return errors.New("перевірка прав не вдалася: " + err.Error())
	}
	defer imp.Close()
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	ok, err := imp.IsMember(admins)
	if err != nil {
		return errors.New("перевірка прав не вдалася: " + err.Error())
	}
	if !ok {
		return errors.New("цей обліковий запис не адміністратор Windows")
	}
	return nil
}

func Supported() bool { return true }
