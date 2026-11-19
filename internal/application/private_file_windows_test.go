//go:build windows

package application

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func makeInsecureFileForTest(path string) error {
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil { return err }
	acl, _, err := sd.DACL()
	if err != nil || acl == nil { return fmt.Errorf("invalid test DACL: %w", err) }
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil)
}
