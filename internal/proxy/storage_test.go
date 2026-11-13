package proxy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/caddyserver/caddy/v2"
)

// The proxy's storage is the module below, built from the config the way
// Caddy builds it, and it stores, loads, locks and unlocks in the root.
func TestStorageModuleKeepsFilesInTheRoot(t *testing.T) {
	root := t.TempDir()
	cfg, err := BuildConfig(nil, Options{StorageRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Storage json.RawMessage `json:"storage"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatal(err)
	}
	mod, err := caddy.GetModule("caddy.storage." + storageModule)
	if err != nil {
		t.Fatal(err)
	}
	conv := mod.New().(caddy.StorageConverter)
	if err := json.Unmarshal(parsed.Storage, conv); err != nil {
		t.Fatal(err)
	}
	st, err := conv.CertMagicStorage()
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := st.Store(ctx, "certificates/local/a.test/a.test.crt", []byte("cert")); err != nil {
		t.Fatal(err)
	}
	if got, err := st.Load(ctx, "certificates/local/a.test/a.test.crt"); err != nil || string(got) != "cert" {
		t.Fatalf("Load = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "certificates", "local", "a.test", "a.test.crt")); err != nil {
		t.Fatalf("the stored file is not in the root: %v", err)
	}
	if _, err := st.Load(ctx, "missing"); !os.IsNotExist(err) {
		t.Fatalf("Load of a missing key = %v, want not-exist at once", err)
	}
	if err := st.Lock(ctx, "issue_cert_a.test"); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, "locks", "issue_cert_a.test.lock")
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("no lock file: %v", err)
	}
	if err := st.Unlock(ctx, "issue_cert_a.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("the lock file is still there after Unlock: %v", err)
	}
}
