package winsys

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// RestrictToOwner makes dir private to the user this process runs as: its
// owner becomes that user, and its access list, protected from what its
// parent passes down, grants full access to that user, SYSTEM and
// Administrators alone, inherited by every file and folder in it. Setting an
// access list passes its inheritable entries down to the files and folders
// already in dir, so a file that took its entries from dir's old list takes
// the new ones; entries a file holds of its own stay, and a file whose list
// is protected keeps it.
//
// The user is named by their SID rather than by Owner Rights (OW): an
// elevated administrator's new folders can be owned by Administrators, and a
// later run of the same user without elevation, in which Administrators only
// denies, must still get in.
//
// Its refusals of dir and its failures to set the list or the owner are
// *os.PathError naming dir.
//
// dir must be absolute. A drive or share root, the user's profile folder and
// the user's AppData folders are refused with ErrSharedFolder, as other
// programs keep files there that this list would close to them. A volume
// without access lists, such as FAT32 or exFAT, fails with ErrNoAccessLists.
// A folder whose access list and owner the user may not change, such as one
// another account owns, fails with ERROR_ACCESS_DENIED.
//
// When passing the list down fails part way, at a file the user may not
// change, the call fails: the folder has its new list, and files the
// system did not reach keep what they had. The next call tries them again.
func RestrictToOwner(dir string) error {
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("%s is not an absolute path", dir)
	}
	if shared(dir) {
		return &os.PathError{Op: "restrict", Path: dir, Err: ErrSharedFolder}
	}
	if !persistentACLs(dir) {
		return &os.PathError{Op: "restrict", Path: dir, Err: ErrNoAccessLists}
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("find this process's user: %w", err)
	}
	sid := user.User.Sid
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	// The access list first: it gives the user WRITE_OWNER, which taking
	// ownership of a folder Administrators own needs.
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return &os.PathError{Op: "set the access list of", Path: dir, Err: err}
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, sid, nil, nil, nil); err != nil {
		return &os.PathError{Op: "set the owner of", Path: dir, Err: err}
	}
	return nil
}

// ErrSharedFolder is RestrictToOwner's refusal of a folder other programs
// use too.
var ErrSharedFolder = errors.New("a folder other programs use too; its access list is left alone")

// ErrNoAccessLists is RestrictToOwner's failure on a volume whose file
// system keeps no access lists.
var ErrNoAccessLists = errors.New("the drive's file system keeps no access lists")

// persistentACLs reports whether the volume dir is on keeps access lists
// (FILE_PERSISTENT_ACLS). When the volume cannot be asked, as some network
// shares cannot, it reports true, and setting the list is what fails.
func persistentACLs(dir string) bool {
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return true
	}
	root := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumePathName(name, &root[0], uint32(len(root))); err != nil {
		return true
	}
	var flags uint32
	if err := windows.GetVolumeInformation(&root[0], nil, 0, nil, nil, &flags, nil, 0); err != nil {
		return true
	}
	return flags&windows.FILE_PERSISTENT_ACLS != 0
}

// shared reports whether dir, clean and absolute, is a drive or share root,
// or the user's profile, AppData\Local or AppData\Roaming folder, compared
// without case. It is a backstop: Nearhost's storage folder is always a
// nearhost\caddy folder, never one of these. Names are compared as given,
// so a short (8.3) name or a link to one of these folders is not caught.
func shared(dir string) bool {
	if dir == filepath.VolumeName(dir)+`\` || dir == filepath.VolumeName(dir) {
		return true
	}
	for _, id := range []*windows.KNOWNFOLDERID{windows.FOLDERID_Profile, windows.FOLDERID_LocalAppData, windows.FOLDERID_RoamingAppData} {
		if path, err := windows.KnownFolderPath(id, 0); err == nil && strings.EqualFold(filepath.Clean(path), dir) {
			return true
		}
	}
	for _, name := range []string{"USERPROFILE", "LOCALAPPDATA", "APPDATA"} {
		if path := os.Getenv(name); path != "" && strings.EqualFold(filepath.Clean(path), dir) {
			return true
		}
	}
	return false
}
