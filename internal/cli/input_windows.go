//go:build windows

package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/nearhost/nearhost/internal/elevate"
)

// heldDirs are the directories `hosts apply` reads --input from and writes
// --report to, each opened once, before the file in it, and held until
// Close. The elevator puts both files in one new folder in the user's temp
// directory, which other processes of that user can write to, so that
// folder is held from before apply reads its input until the report is
// written. A directory held without FILE_SHARE_DELETE cannot be renamed,
// replaced or deleted, so a file checked to be in it stays where apply
// expects. It also keeps the elevator's cleanup from removing the folder
// itself while apply runs; the files in it can still go, and the empty
// folder is left behind.
type heldDirs struct {
	held map[string]*heldDir
}

// heldDir is one held directory: its handle and its final path, as
// GetFinalPathNameByHandle gives it.
type heldDir struct {
	h     windows.Handle
	final string
}

// hold opens dir without following a reparse point and holds it, or returns
// the handle already held for dir. A symlink or a junction is refused,
// checked on the opened handle.
func (d *heldDirs) hold(dir string) (*heldDir, error) {
	if hd, ok := d.held[dir]; ok {
		return hd, nil
	}
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return nil, err
	}
	// GENERIC_READ includes FILE_LIST_DIRECTORY, a data access, so the
	// share mode is enforced against a rename or delete of dir.
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: dir, Err: err}
	}
	hd, err := checkHeldDir(h, dir)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	if d.held == nil {
		d.held = map[string]*heldDir{}
	}
	d.held[dir] = hd
	return hd, nil
}

// checkHeldDir is the heldDir for the handle h, opened at dir, or an error
// when h is a reparse point or not a directory.
func checkHeldDir(h windows.Handle, dir string) (*heldDir, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return nil, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, fmt.Errorf("%s is a link", dir)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	final, err := finalPath(h)
	if err != nil {
		return nil, err
	}
	return &heldDir{h: h, final: final}, nil
}

// Close lets go of every held directory.
func (d *heldDirs) Close() {
	for _, hd := range d.held {
		windows.CloseHandle(hd.h)
	}
	d.held = nil
}

// finalPath is the path of the open file h as the file system names it,
// every link and short name resolved (\\?\C:\Users\...).
func finalPath(h windows.Handle) (string, error) {
	buf := make([]uint16, windows.MAX_PATH)
	for {
		// Flags 0 are FILE_NAME_NORMALIZED and VOLUME_NAME_DOS.
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return "", err
		}
		if n < uint32(len(buf)) {
			return windows.UTF16ToString(buf[:n]), nil
		}
		// Too small: n is the size needed, with the terminating NUL.
		buf = make([]uint16, n)
	}
}

// holds returns an error unless the open file h is the entry named like
// path's last element directly in hd, by final path. Names are compared
// without case, as NTFS does.
func (hd *heldDir) holds(h windows.Handle, path string) error {
	got, err := finalPath(h)
	if err != nil {
		return err
	}
	want := strings.TrimSuffix(hd.final, `\`) + `\` + filepath.Base(path)
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("%s opened as %s, not in its directory %s", path, got, hd.final)
	}
	return nil
}

// errHelperFolder refuses an elevated run's --input or --report.
var errHelperFolder = errors.New("an elevated run reads and reports only in a folder Nearhost made for it")

// checkElevatedIO checks, before an elevated run opens either file, that
// --input and --report are both given or both left out, and when given are
// the files input and report of one folder that HelperDir could have made.
// Whoever starts the helper chooses both paths, and an administrator's run
// could otherwise read any file into its report, or create the report where
// only administrators can write, such as the Startup folder of every user.
//
// The folder is held (see heldDirs), so it is not a link or a junction and
// stays what was checked, and its owner and access list are read from that
// handle. checkHelperDirSecurity says what they must be; the helper cannot
// tell which user asked for it, as a standard user who typed an
// administrator's password runs it as that administrator, so it checks that
// the only one besides SYSTEM and Administrators who can write in the folder
// is a user account that owns it. A folder under the Windows directory,
// Program Files or ProgramData is refused as well. openInput and
// createReport then use the held folder and check each file's final path.
func checkElevatedIO(dirs *heldDirs, input, report string) error {
	if input == "" && report == "" {
		return nil
	}
	dir := filepath.Dir(input)
	if input == "" || report == "" ||
		!filepath.IsAbs(input) || filepath.Clean(input) != input || filepath.Clean(report) != report ||
		filepath.Base(input) != "input" || filepath.Base(report) != "report" || filepath.Dir(report) != dir ||
		!strings.HasPrefix(filepath.Base(dir), elevate.HelperDirPrefix) {
		return errHelperFolder
	}
	hd, err := dirs.hold(dir)
	if err != nil {
		return errHelperFolder
	}
	sd, err := windows.GetSecurityInfo(hd.h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || checkHelperDirSecurity(sd) != nil || inSystemFolder(hd.final) {
		return errHelperFolder
	}
	return nil
}

// fileDeleteChild is FILE_DELETE_CHILD: on a folder, the right to delete
// any file in it.
const fileDeleteChild = 0x40

// writeRights are the rights on a folder that let a holder add, replace or
// remove a file in it, or give itself those rights.
const writeRights = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | fileDeleteChild | windows.DELETE |
	windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_WRITE | windows.GENERIC_ALL

// checkHelperDirSecurity returns an error unless sd, a folder's security
// descriptor, has a user account for its owner and an access list that lets
// no one but that owner, SYSTEM and Administrators write in the folder or
// in what it passes down to new files. A user account is a local or domain
// account (S-1-5-21-...) or a Microsoft Entra account (S-1-12-1-...), never
// a built-in group or service such as TrustedInstaller, which own the
// system's folders. Allow entries of a kind other than the plain one are
// refused, as are a missing or null access list, which let anyone in; deny
// entries only take rights away.
func checkHelperDirSecurity(sd *windows.SECURITY_DESCRIPTOR) error {
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner == nil {
		return errors.New("the folder has no owner")
	}
	if o := owner.String(); !strings.HasPrefix(o, "S-1-5-21-") && !strings.HasPrefix(o, "S-1-12-1-") {
		return fmt.Errorf("the folder's owner %s is not a user account", o)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return errors.New("the folder's access list lets anyone in")
	}
	allowed := []*windows.SID{owner}
	for _, t := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid,
		windows.WinCreatorOwnerSid, windows.WinCreatorOwnerRightsSid} {
		sid, err := windows.CreateWellKnownSid(t)
		if err != nil {
			return err
		}
		allowed = append(allowed, sid)
	}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE:
		case windows.ACCESS_DENIED_ACE_TYPE, accessDeniedObjectACEType, accessDeniedCallbackACEType, accessDeniedCallbackObjectACEType:
			continue
		default:
			return fmt.Errorf("the folder's access list has an entry of type %d", ace.Header.AceType)
		}
		if ace.Mask&writeRights == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !slices.ContainsFunc(allowed, sid.Equals) {
			return fmt.Errorf("%s can write in the folder", sid)
		}
	}
	return nil
}

// The deny entry types other than ACCESS_DENIED_ACE_TYPE, from winnt.h.
const (
	accessDeniedObjectACEType         = 0x6
	accessDeniedCallbackACEType       = 0xA
	accessDeniedCallbackObjectACEType = 0xC
)

// systemFolders are the known folders an elevated run's folder must not be
// in: the Windows directory, both Program Files and ProgramData.
var systemFolders = []*windows.KNOWNFOLDERID{windows.FOLDERID_Windows, windows.FOLDERID_ProgramFiles,
	windows.FOLDERID_ProgramFilesX86, windows.FOLDERID_ProgramData}

// inSystemFolder reports whether final, a final path as finalPath gives it,
// is one of systemFolders or in one, each compared by its own final path. A
// known folder this system does not have is skipped.
func inSystemFolder(final string) bool {
	for _, id := range systemFolders {
		path, err := windows.KnownFolderPath(id, 0)
		if err != nil {
			continue
		}
		root, err := finalPathOf(path)
		if err != nil {
			// Not there to open: compare the path as the API gives it.
			root = `\\?\` + path
		}
		if within(final, root) {
			return true
		}
	}
	return false
}

// finalPathOf is the final path of the file or folder at path.
func finalPathOf(path string) (string, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	return finalPath(h)
}

// within reports whether path is root or in it, without case, as NTFS
// compares names.
func within(path, root string) bool {
	root = strings.TrimSuffix(root, `\`)
	return strings.EqualFold(path, root) ||
		len(path) > len(root) && path[len(root)] == '\\' && strings.EqualFold(path[:len(root)], root)
}

// openInput opens the `hosts apply --input` file. Its directory is held
// first (see heldDirs) and must not be a symlink or a junction; the file is
// opened without following a reparse point at its last component and
// refused when it is a directory or a reparse point, checked on the opened
// handle, or when its final path is not the held directory's plus its name.
// The file is in a directory other processes of the user can write to, and
// apply runs elevated, so a link must not lead it to a file of its own. What
// remains: a hard link at input passes, as it is a name in that directory,
// and so would a change above the temp directory that the user can make
// anyway. The input is opened with FILE_SHARE_DELETE, so the elevator can
// still remove it when it stops waiting.
func openInput(dirs *heldDirs, path string) (*os.File, error) {
	hd, err := dirs.hold(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("input directory: %w", err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if err := hd.holds(h, path); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// createReport creates the `hosts apply --report` file. Its directory is
// held first (see heldDirs) and must not be a symlink or a junction; the
// file is created with CREATE_NEW, as O_EXCL does, which fails on an
// existing file or link rather than following it. When the new file's
// final path is not the held directory's plus its name, it is deleted
// through its own handle and refused. What remains is as for openInput: a
// change above the temp directory that the user can make anyway.
func createReport(dirs *heldDirs, path string) (*os.File, error) {
	hd, err := dirs.hold(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("report directory: %w", err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE|windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	if err := hd.holds(h, path); err != nil {
		// FILE_DISPOSITION_INFO is one BOOLEAN, DeleteFile.
		del := byte(1)
		derr := windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, &del, 1)
		windows.CloseHandle(h)
		return nil, errors.Join(err, derr)
	}
	return os.NewFile(uintptr(h), path), nil
}
