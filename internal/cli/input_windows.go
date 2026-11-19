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
// helperPaths checks the paths as given, before anything is opened, and
// checkHelperDir the folder they name, held (see heldDirs) so it stays what
// was checked. openInput and createReport then open both files through the
// held folder's handle.
//
// Nearhost's own folder fails this when TMP is not on a local NTFS or ReFS
// drive: a network share, UNC or a Samba share on a drive letter, is
// refused by its path or by its final path, and FAT32 or exFAT has no owner
// or access list to check.
func checkElevatedIO(dirs *heldDirs, input, report string) error {
	if input == "" && report == "" {
		return nil
	}
	if !helperPaths(input, report) {
		return errHelperFolder
	}
	if err := checkHelperDir(dirs, filepath.Dir(input)); err != nil {
		return errHelperFolder
	}
	return nil
}

// helperPaths reports whether input and report are both given, clean and
// on a drive letter (C:\...), and are the files input and report of one
// folder whose name starts with elevate.HelperDirPrefix. A UNC or device
// path (\\server\share, \\?\, \\.\) is refused before anything opens it,
// as opening it would have the administrator's run sign in to a server or
// follow an object manager link the caller chose. A drive-letter path can
// still pass through a link in a parent folder that leads to a network path
// or elsewhere; checkHelperDir, on the final path of the folder as opened,
// is what catches that.
func helperPaths(input, report string) bool {
	if input == "" || report == "" {
		return false
	}
	dir := filepath.Dir(input)
	return driveLetter(filepath.VolumeName(input)) && driveLetter(filepath.VolumeName(report)) &&
		filepath.IsAbs(input) && filepath.Clean(input) == input && filepath.Clean(report) == report &&
		filepath.Base(input) == "input" && filepath.Base(report) == "report" && filepath.Dir(report) == dir &&
		strings.HasPrefix(filepath.Base(dir), elevate.HelperDirPrefix)
}

// driveLetter reports whether volume is a drive letter and a colon.
func driveLetter(volume string) bool {
	if len(volume) != 2 || volume[1] != ':' {
		return false
	}
	c := volume[0] | 0x20 // lower case
	return c >= 'a' && c <= 'z'
}

// checkHelperDir holds dir and returns an error unless the folder opened is
// one HelperDir could have made: its final path is on a local drive and ends
// in the name dir gives, which starts with elevate.HelperDirPrefix (a link
// in a parent folder or in the object manager could otherwise lead a name
// that passes helperPaths to another folder); checkHelperDirSecurity passes
// its owner and access list, read from the held handle; and it is not in a
// system folder. The helper cannot tell which user asked for it, as a
// standard user who typed an administrator's password runs it as that
// administrator, so the rule is that no one but its owner, SYSTEM and
// Administrators can write in the folder.
func checkHelperDir(dirs *heldDirs, dir string) error {
	hd, err := dirs.hold(dir)
	if err != nil {
		return err
	}
	if !finalNameMatches(dir, hd.final) {
		return fmt.Errorf("%s opened as %s", dir, hd.final)
	}
	sd, err := windows.GetSecurityInfo(hd.h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	if err := checkHelperDirSecurity(sd); err != nil {
		return err
	}
	if inSystemFolder(hd.final) {
		return fmt.Errorf("%s is in a system folder", hd.final)
	}
	return nil
}

// finalNameMatches reports whether final, the final path of the folder
// opened at dir, is on a drive letter (\\?\C:\..., never \\?\UNC\...) and
// has the same last element as dir, without case, starting with
// elevate.HelperDirPrefix.
func finalNameMatches(dir, final string) bool {
	rest, ok := strings.CutPrefix(final, `\\?\`)
	if !ok || !driveLetter(filepath.VolumeName(rest)) {
		return false
	}
	name := filepath.Base(final)
	return strings.EqualFold(name, filepath.Base(dir)) &&
		strings.HasPrefix(strings.ToLower(name), elevate.HelperDirPrefix)
}

// fileDeleteChild is FILE_DELETE_CHILD: on a folder, the right to delete
// any file in it.
const fileDeleteChild = 0x40

// writeRights are the rights on a folder that let a holder add, replace or
// remove a file in it, or give itself those rights.
const writeRights = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | fileDeleteChild | windows.DELETE |
	windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_WRITE | windows.GENERIC_ALL

// checkHelperDirSecurity returns an error unless sd, a folder's security
// descriptor, has an owner whose SID is in a machine's or domain's own
// accounts (S-1-5-21-...) or Microsoft Entra's (S-1-12-1-...), and an access
// list that lets no one but that owner, SYSTEM and Administrators write in
// the folder or in what it passes down to new files. Those SID ranges hold
// user accounts and also the machine's or domain's own groups; they never
// hold a built-in group (S-1-5-32-...) or a service such as SYSTEM or
// TrustedInstaller, which own the system's folders. Allow entries of a kind
// other than the plain one are refused, as are a missing or null access
// list, which let anyone in; deny entries only take rights away.
func checkHelperDirSecurity(sd *windows.SECURITY_DESCRIPTOR) error {
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner == nil {
		return errors.New("the folder has no owner")
	}
	if o := owner.String(); !strings.HasPrefix(o, "S-1-5-21-") && !strings.HasPrefix(o, "S-1-12-1-") {
		return fmt.Errorf("the folder's owner %s is a built-in or service account", o)
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

// openIn opens the file name directly in hd through hd's handle, never by a
// path, so a link or junction swapped into a parent folder after hd was
// held cannot move it. OBJ_DONT_REPARSE and FILE_OPEN_REPARSE_POINT keep it
// from following a reparse point at name; FILE_NON_DIRECTORY_FILE refuses a
// folder. The handle is synchronous, as os.File expects. name is the last
// element of the path the caller gave, which errors name.
func (hd *heldDir) openIn(path string, access, share, disposition uint32) (windows.Handle, error) {
	name, err := windows.NewNTUnicodeString(filepath.Base(path))
	if err != nil {
		return 0, err
	}
	oa := &windows.OBJECT_ATTRIBUTES{
		RootDirectory: hd.h,
		ObjectName:    name,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	oa.Length = uint32(unsafe.Sizeof(*oa))
	var h windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, access|windows.SYNCHRONIZE, oa, &iosb, nil, windows.FILE_ATTRIBUTE_NORMAL, share, disposition,
		windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if err != nil {
		if st, ok := err.(windows.NTStatus); ok {
			err = st.Errno()
		}
		return 0, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return h, nil
}

// openInput opens the `hosts apply --input` file. Its directory is held
// first (see heldDirs) and must not be a symlink or a junction; the file is
// opened through the held directory's handle (see openIn) and refused when
// it is a directory or a reparse point, checked on the opened handle, or
// when its final path is not the held directory's plus its name, which
// stays as a backstop. The file is in a directory other processes of the
// user can write to, and apply runs elevated, so a link must not lead it to
// a file of its own. What remains: a hard link at input passes, as it is a
// name in that directory; an elevated run's report never quotes the input,
// so a hard link reads nothing out. The input is opened with
// FILE_SHARE_DELETE, so the elevator can still remove it when it stops
// waiting.
func openInput(dirs *heldDirs, path string) (*os.File, error) {
	hd, err := dirs.hold(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("input directory: %w", err)
	}
	h, err := hd.openIn(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, windows.FILE_OPEN)
	if err != nil {
		return nil, err
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
// file is created through the held directory's handle (see openIn) with
// FILE_CREATE, as O_EXCL does, which fails on an existing file or link
// rather than following it. When the new file's final path is not the held
// directory's plus its name, a backstop that the handle-relative create
// should never trip, it is deleted through its own handle and refused.
func createReport(dirs *heldDirs, path string) (*os.File, error) {
	hd, err := dirs.hold(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("report directory: %w", err)
	}
	h, err := hd.openIn(path, windows.GENERIC_WRITE|windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, windows.FILE_CREATE)
	if err != nil {
		return nil, err
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
