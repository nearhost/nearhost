package proxy

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFiles creates each file, with its directories, under root.
func writeFiles(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// caFiles is the local CA as Caddy lays it out under the storage root.
var caFiles = []string{
	"pki/authorities/local/root.crt",
	"pki/authorities/local/root.key",
	"pki/authorities/local/intermediate.crt",
	"pki/authorities/local/intermediate.key",
}

// present reports whether path is there, without following a final link.
func present(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// RemoveCA removes the local CA's directory and the certificates it issued,
// and nothing else in the storage root.
func TestRemoveCARemovesTheAuthorityAndItsCertificatesOnly(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, caFiles...)
	writeFiles(t, root,
		"certificates/local/app.test/app.test.crt",
		"certificates/local/app.test/app.test.key",
		"certificates/other/x.test/x.test.crt",
		"pki/authorities/other/root.key",
		"locks/app.test.lock",
		"instance.uuid",
	)
	if err := RemoveCA(root); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"pki/authorities/local", "certificates/local"} {
		if present(filepath.Join(root, filepath.FromSlash(gone))) {
			t.Errorf("%s is still there", gone)
		}
	}
	for _, kept := range []string{
		"certificates/other/x.test/x.test.crt",
		"pki/authorities/other/root.key",
		"locks/app.test.lock",
		"instance.uuid",
	} {
		if !present(filepath.Join(root, filepath.FromSlash(kept))) {
			t.Errorf("%s was removed", kept)
		}
	}
}

// With no CA there is nothing to remove, and that is not an error.
func TestRemoveCAWithNoCAIsNoError(t *testing.T) {
	if err := RemoveCA(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := RemoveCA(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
}

// A storage root that is not an absolute path is refused before anything is
// removed.
func TestRemoveCARefusesARelativeRoot(t *testing.T) {
	for _, root := range []string{"", "caddy", "."} {
		if err := RemoveCA(root); err == nil {
			t.Errorf("RemoveCA(%q) succeeded", root)
		}
	}
}

// linkOrSkip makes link point at target, skipping the test where the
// system does not allow it (Windows without the privilege).
func linkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
}

// An authority directory that is a link out of the storage root is refused:
// neither the link's target nor the link is removed, and the error says why.
func TestRemoveCARefusesALinkOutOfTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFiles(t, outside, "root.key", "root.crt")
	link := filepath.Join(root, "pki", "authorities", "local")
	linkOrSkip(t, outside, link)
	err := RemoveCA(root)
	if err == nil || !strings.Contains(err.Error(), link) {
		t.Fatalf("err = %v", err)
	}
	if !present(filepath.Join(outside, "root.key")) || !present(link) {
		t.Fatal("RemoveCA removed something outside the storage root")
	}
}

// A link out of the root higher up the path is refused the same way.
func TestRemoveCARefusesAParentLinkOutOfTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFiles(t, outside, "authorities/local/root.key")
	linkOrSkip(t, outside, filepath.Join(root, "pki"))
	if err := RemoveCA(root); err == nil {
		t.Fatal("RemoveCA followed pki out of the storage root")
	}
	if !present(filepath.Join(outside, "authorities", "local", "root.key")) {
		t.Fatal("RemoveCA removed something outside the storage root")
	}
}

// No link below the storage root is followed, even one that stays inside it:
// a storage root that resolves to a broad directory would otherwise let a
// link lead to something that is not the CA. Each directory on the way to
// the CA and to its certificates is refused when it is a link, nothing is
// removed, and the error names the link.
func TestRemoveCARefusesEveryLinkOnTheWay(t *testing.T) {
	for _, linked := range []string{
		"pki", "pki/authorities", "pki/authorities/local",
		"certificates", "certificates/local",
	} {
		t.Run(linked, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, root, caFiles...)
			writeFiles(t, root, "certificates/local/app.test/app.test.crt")
			// Move the directory aside, inside the root, and link to it.
			link := filepath.Join(root, filepath.FromSlash(linked))
			moved := filepath.Join(root, "moved")
			if err := os.Rename(link, moved); err != nil {
				t.Fatal(err)
			}
			linkOrSkip(t, moved, link)
			err := RemoveCA(root)
			if err == nil || !strings.Contains(err.Error(), link) || !strings.Contains(err.Error(), "link") {
				t.Fatalf("err = %v", err)
			}
			// Nothing went: neither side's files nor the link.
			if !present(link) || !present(filepath.Join(root, "pki", "authorities", "local", "root.key")) ||
				!present(filepath.Join(root, "certificates", "local", "app.test", "app.test.crt")) {
				t.Fatal("RemoveCA removed something")
			}
		})
	}
}

// An authority directory that links to a directory above it is refused:
// removing the target would remove the other authorities too.
func TestRemoveCARefusesALinkToItsParent(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, "pki/authorities/other/root.key")
	link := filepath.Join(root, "pki", "authorities", "local")
	linkOrSkip(t, filepath.Join(root, "pki"), link)
	if err := RemoveCA(root); err == nil {
		t.Fatal("RemoveCA removed the link's parent")
	}
	if !present(filepath.Join(root, "pki", "authorities", "other", "root.key")) {
		t.Fatal("RemoveCA removed another authority")
	}
}

// A storage root that is itself a link, as into a dotfiles repository, is
// followed: the CA inside it goes.
func TestRemoveCAThroughALinkedRoot(t *testing.T) {
	real := t.TempDir()
	writeFiles(t, real, caFiles...)
	root := filepath.Join(t.TempDir(), "caddy")
	linkOrSkip(t, real, root)
	if err := RemoveCA(root); err != nil {
		t.Fatal(err)
	}
	if present(filepath.Join(real, "pki", "authorities", "local")) {
		t.Fatal("the CA is still there")
	}
}

// A CA that cannot be removed is an error naming the files still there.
func TestRemoveCANamesWhatRemains(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the user cannot write to")
	}
	root := t.TempDir()
	writeFiles(t, root, caFiles...)
	ca := filepath.Join(root, "pki", "authorities", "local")
	if err := os.Chmod(ca, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ca, 0o700) })
	err := RemoveCA(root)
	if err == nil || !strings.Contains(err.Error(), "root.key") || !strings.Contains(err.Error(), ca) {
		t.Fatalf("err = %v", err)
	}
}
