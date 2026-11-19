//go:build windows

package application

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows uses a protected DACL rather than POSIX mode bits for privacy.
// The current user, SYSTEM and Administrators receive full access.
func protectPrivateFile(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("read current Windows user: %w", err)
	}
	sddl := "D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)(A;;FA;;;BA)"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("create private Windows DACL: %w", err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return fmt.Errorf("read private Windows DACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		return fmt.Errorf("secure Windows file %q: %w", path, err)
	}
	return nil
}

// The DACL is checked on every catalog reopen. os.FileMode.Perm has no
// equivalent security semantics on Windows, so it must not be used here.
func requirePrivateFile(path string, _ os.FileInfo) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("inspect Windows file ACL %q: %w", path, err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("file %q has an unprotected Windows DACL: %w", path, err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return fmt.Errorf("file %q has no restrictive Windows DACL: %w", path, err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil { return err }
	allowed := map[string]bool{
		user.User.Sid.String(): true,
		"S-1-5-18": true,
		"S-1-5-32-544": true,
	}
	if acl.AceCount == 0 {
		return fmt.Errorf("file %q denies its owner all access", path)
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil { return err }
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("file %q has unsupported Windows access entries", path)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !allowed[sid.String()] {
			return fmt.Errorf("file %q permits access to an untrusted Windows principal", path)
		}
	}
	return nil
}
