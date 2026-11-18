package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// typo is a config a user edited by hand: a route has host: for hosts:, and a
// key a newer Nearhost might write sits at the top. Comments and an anchor
// with a merge key are there too, which are not unknown keys.
const typo = `# my projects
servers: []
projects:
  - name: shop
    server: local
    path: /srv/shop
    alias: shop.test
    enabled: true
    routes:
      - port: 8000
        role: web
        host: [admin.shop.test]
      - &vite
        port: 5173
        role: loopback
      - <<: *vite
        port: 5174
future_option: true
`

// TestUnknownKeysNamesEveryKeyWithItsLine checks that every key the structs do
// not model is reported with its line, not only the first, and that a merge
// key is not one of them.
func TestUnknownKeysNamesEveryKeyWithItsLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(typo), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := UnknownKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Unknown{{Key: "host", Line: 12}, {Key: "future_option", Line: 18}}
	if !slices.Equal(got, want) {
		t.Fatalf("UnknownKeys = %+v, want %+v", got, want)
	}

	none, err := UnknownKeys(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil || none != nil {
		t.Fatalf("missing file: %+v, %v", none, err)
	}
	if err := os.WriteFile(path, []byte("projects: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := UnknownKeys(path); err == nil {
		t.Fatal("a file that is not YAML must be an error")
	}
}

// TestUpdateRefusesToDropAnUnknownKey checks that a save is refused while the
// file holds a key Nearhost does not know, naming each with its line, and
// that the file is left as it was. Reading it stays tolerant.
func TestUpdateRefusesToDropAnUnknownKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(typo), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("Load must still read a file with unknown keys: %v", err)
	}
	called := false
	err := Update(path, func(c *Config) error { called = true; return appendProject("blog")(c) })
	want := path + ` line 12: Nearhost does not know the key "host". Fix or remove it; saving now would delete it.` + "\n" +
		path + ` line 18: Nearhost does not know the key "future_option". Fix or remove it; saving now would delete it.`
	if err == nil || err.Error() != want {
		t.Fatalf("Update err = %v\nwant %s", err, want)
	}
	var uk *UnknownKeysError
	if !errors.As(err, &uk) || len(uk.Keys) != 2 {
		t.Fatalf("err is not an *UnknownKeysError with both keys: %#v", err)
	}
	if called {
		t.Error("change ran although nothing could be saved")
	}
	if data, _ := os.ReadFile(path); string(data) != typo {
		t.Fatalf("file changed:\n%s", data)
	}
}

// fill sets every field reachable from v to a value that is not its zero
// value: one element in each slice, a set pointer, true, 1 and "x". A field
// added to the config later is filled too, so the round trip below covers it
// without anyone remembering to.
func fill(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i))
			}
		}
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem())
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0))
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fill(k)
		fill(e)
		v.SetMapIndex(k, e)
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	default:
		panic("fill: no value for " + v.Type().String())
	}
}

// TestSavedConfigHasNoUnknownKeys checks that a file Save writes from a
// config with every field set reports no unknown key, loads back the same,
// and decodes under yaml's own KnownFields check, so a config this version
// writes never blocks this version's next save.
func TestSavedConfigHasNoUnknownKeys(t *testing.T) {
	var c Config
	fill(reflect.ValueOf(&c).Elem())
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, &c); err != nil {
		t.Fatal(err)
	}
	got, err := UnknownKeys(path)
	if err != nil || len(got) != 0 {
		t.Fatalf("UnknownKeys = %+v, %v", got, err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*back, c) {
		t.Fatalf("round trip lost a field:\n got %+v\nwant %+v", *back, c)
	}
	data, _ := os.ReadFile(path)
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var strict Config
	if err := dec.Decode(&strict); err != nil {
		t.Fatalf("KnownFields decode: %v\n%s", err, data)
	}
}

// selfDecoding decodes itself, so its keys are its own business.
type selfDecoding struct{}

// UnmarshalYAML accepts anything.
func (*selfDecoding) UnmarshalYAML(*yaml.Node) error { return nil }

// Common is inlined into shapes below.
type Common struct {
	ID string `yaml:"id"`
}

// shapes has every kind of field the walk must tell apart.
type shapes struct {
	Common   `yaml:",inline"`
	Untagged string
	Skipped  string            `yaml:"-"`
	Labels   map[string]Server `yaml:"labels"`
	Custom   selfDecoding      `yaml:"custom"`
	Extra    *struct {
		Rest map[string]string `yaml:",inline"`
	} `yaml:"extra"`
}

// TestUnknownKeysFollowsYAMLTags checks the walk against the forms a field
// can take: an inline struct's keys are known, an untagged field goes by its
// lowercase name, a field tagged "-" is not a key, a map's keys are data
// while its values are checked, a type that decodes itself is not looked
// into, and an inline map takes any key.
func TestUnknownKeysFollowsYAMLTags(t *testing.T) {
	const src = `id: a
untagged: b
skipped: c
labels:
  anything: {name: n, ssh_host: h, sshhost: typo}
custom: {whatever: 1}
extra: {free: form}
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	var got []Unknown
	unknownIn(&doc, reflect.TypeFor[shapes](), &got)
	want := []Unknown{{Key: "skipped", Line: 3}, {Key: "sshhost", Line: 5}}
	if !slices.Equal(got, want) {
		t.Fatalf("unknown = %+v, want %+v", got, want)
	}
}

// TestUpdateReadsTheFileOnce checks that Update looks for unknown keys in the
// bytes it loaded: a second read could see a hand edit saved in between and
// check a version other than the one it changes and saves.
func TestUpdateReadsTheFileOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, sample()); err != nil {
		t.Fatal(err)
	}
	reads := 0
	real := readFile
	readFile = func(name string) ([]byte, error) { reads++; return real(name) }
	t.Cleanup(func() { readFile = real })
	if err := Update(path, appendProject("blog")); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("Update read the config %d times, want 1", reads)
	}
}

// TestUnknownKeysNamesAFileItCannotRead checks that a read error names the
// file once.
func TestUnknownKeysNamesAFileItCannotRead(t *testing.T) {
	path := t.TempDir() // a directory cannot be read as a file
	_, err := UnknownKeys(path)
	if err == nil || !strings.HasPrefix(err.Error(), "read "+path+": ") || strings.Count(err.Error(), path) != 1 {
		t.Fatalf("err = %v", err)
	}
}
