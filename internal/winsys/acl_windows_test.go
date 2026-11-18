package winsys

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileAllAccess is FILE_ALL_ACCESS, what FA stands for in SDDL.
const fileAllAccess = 0x1F01FF

// entry is one access list entry, its SID as a string.
type entry struct {
	typ   byte
	flags byte
	mask  windows.ACCESS_MASK
	sid   string
}

// security reads the owner, whether the access list is protected, and the
// entries of the access list of path.
func security(t *testing.T, path string) (owner string, protected bool, entries []entry) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil {
		t.Fatalf("%s has no access list", path)
	}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		entries = append(entries, entry{ace.Header.AceType, ace.Header.AceFlags, ace.Mask, sid.String()})
	}
	return o.String(), control&windows.SE_DACL_PROTECTED != 0, entries
}

// userSID is the SID of the user this test runs as.
func userSID(t *testing.T) string {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Sid.String()
}

// wellKnown is the SID of a well-known account as a string.
func wellKnown(t *testing.T, typ windows.WELL_KNOWN_SID_TYPE) string {
	t.Helper()
	sid, err := windows.CreateWellKnownSid(typ)
	if err != nil {
		t.Fatal(err)
	}
	return sid.String()
}

// want are the entries RestrictToOwner sets, with flags: OICI on the folder,
// INHERITED_ACE on what it passes them down to.
func want(t *testing.T, flags byte) []entry {
	return []entry{
		{windows.ACCESS_ALLOWED_ACE_TYPE, flags, fileAllAccess, userSID(t)},
		{windows.ACCESS_ALLOWED_ACE_TYPE, flags, fileAllAccess, wellKnown(t, windows.WinLocalSystemSid)},
		{windows.ACCESS_ALLOWED_ACE_TYPE, flags, fileAllAccess, wellKnown(t, windows.WinBuiltinAdministratorsSid)},
	}
}

const (
	oici      = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	inherited = windows.INHERITED_ACE
)

// setSDDL sets path's owner (when the SDDL has one) and protected access
// list from sddl.
func setSDDL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	if owner != nil {
		info |= windows.OWNER_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, owner, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

// wideParent is a new folder whose access list lets Everyone read what is
// in it, as a data folder redirected to a shared drive can.
func wideParent(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	setSDDL(t, parent, "D:P(A;OICI;FA;;;"+userSID(t)+")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FR;;;WD)")
	return parent
}

// The folder's access list is protected and holds exactly the user, SYSTEM
// and Administrators, each with full access passed down to files and
// folders, and its owner is the user.
func TestRestrictToOwnerSetsAPrivateProtectedListOnWindows(t *testing.T) {
	dir := filepath.Join(wideParent(t), "caddy")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RestrictToOwner(dir); err != nil {
		t.Fatal(err)
	}
	owner, protected, entries := security(t, dir)
	if !protected {
		t.Error("the access list is not protected")
	}
	if owner != userSID(t) {
		t.Errorf("owner = %s, want the user %s", owner, userSID(t))
	}
	if !slices.Equal(entries, want(t, oici)) {
		t.Errorf("entries = %+v, want %+v", entries, want(t, oici))
	}
}

// A file that took its entries from the folder's old list, such as a key
// Caddy wrote before, takes the new ones, and so does a file created after,
// and a folder in it with the file in that.
func TestRestrictToOwnerCoversFilesBeforeAndAfterOnWindows(t *testing.T) {
	dir := filepath.Join(wideParent(t), "caddy")
	sub := filepath.Join(dir, "pki", "authorities", "local")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	before := filepath.Join(sub, "root.key")
	if err := os.WriteFile(before, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, entries := security(t, before); !slices.ContainsFunc(entries, func(e entry) bool { return e.sid == "S-1-1-0" }) {
		t.Fatalf("the setup is wrong: the file does not let Everyone in: %+v", entries)
	}
	if err := RestrictToOwner(dir); err != nil {
		t.Fatal(err)
	}
	after := filepath.Join(dir, "certificates.json")
	if err := os.WriteFile(after, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{before, after} {
		if _, protected, entries := security(t, path); protected || !slices.Equal(entries, want(t, inherited)) {
			t.Errorf("%s: protected %v, entries %+v, want inherited %+v", path, protected, entries, want(t, inherited))
		}
	}
	if _, _, entries := security(t, sub); !slices.Equal(entries, want(t, oici|inherited)) {
		t.Errorf("%s: entries %+v, want %+v", sub, entries, want(t, oici|inherited))
	}
}

// A second call changes nothing.
func TestRestrictToOwnerTwiceIsTheSameOnWindows(t *testing.T) {
	dir := filepath.Join(wideParent(t), "caddy")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RestrictToOwner(dir); err != nil {
		t.Fatal(err)
	}
	owner, protected, entries := security(t, dir)
	if err := RestrictToOwner(dir); err != nil {
		t.Fatal(err)
	}
	owner2, protected2, entries2 := security(t, dir)
	if owner != owner2 || protected != protected2 || !slices.Equal(entries, entries2) {
		t.Errorf("second call changed %s %v %+v to %s %v %+v", owner, protected, entries, owner2, protected2, entries2)
	}
}

// A folder an elevated run created can be owned by Administrators, whose
// entry does nothing for the same user without elevation. After the call
// the user owns it and has an entry of their own. CI's runner is elevated,
// so there the folder starts out as that case exactly: owned by
// Administrators, with no entry for the user.
func TestRestrictToOwnerLetsTheUserInAfterAnElevatedRunOnWindows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "caddy")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if windows.GetCurrentProcessToken().IsElevated() {
		setSDDL(t, dir, "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
		if owner, _, _ := security(t, dir); owner != wellKnown(t, windows.WinBuiltinAdministratorsSid) {
			t.Fatalf("the setup is wrong: owner = %s, want Administrators", owner)
		}
	} else {
		t.Log("not elevated: the folder starts out owned by the user")
	}
	if err := RestrictToOwner(dir); err != nil {
		t.Fatal(err)
	}
	owner, _, entries := security(t, dir)
	if owner != userSID(t) {
		t.Errorf("owner = %s, want the user %s", owner, userSID(t))
	}
	if !slices.Contains(entries, entry{windows.ACCESS_ALLOWED_ACE_TYPE, oici, fileAllAccess, userSID(t)}) {
		t.Errorf("no full-access entry for the user %s in %+v", userSID(t), entries)
	}
}

// A drive root and the user's profile are refused, and their access lists
// are not touched.
func TestRestrictToOwnerRefusesSharedFoldersOnWindows(t *testing.T) {
	profile, err := windows.KnownFolderPath(windows.FOLDERID_Profile, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.VolumeName(profile) + `\`, profile, "relative"} {
		if err := RestrictToOwner(dir); err == nil {
			t.Errorf("RestrictToOwner(%q) = nil, want a refusal", dir)
		}
	}
}
