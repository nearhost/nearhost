package doctor

import (
	"reflect"
	"testing"
)

func TestComposeServicesReadsBothForms(t *testing.T) {
	const src = `services:
  a:
    environment:
      HOST: https://${APP_HOST}/x
      PLAIN: one
    extra_hosts:
      - "Api.X.io:host-gateway"
      - "b.x.io=host-gateway"
    env_file: one.env
    volumes:
      - .:/var/www/html:ro
  b:
    environment:
      - K=v
      - FROM_ENV
    extra_hosts:
      c.x.io: host-gateway
    env_file:
      - two.env
      - path: three.env
    volumes:
      - type: bind
        source: ./
        target: /app
      - ${CERT}:/c:ro
    network_mode: host
`
	got, err := composeServices([]byte(src), map[string]string{"APP_HOST": "app.test", "FROM_ENV": "val", "CERT": "/p/root.crt"})
	if err != nil {
		t.Fatal(err)
	}
	want := []service{
		{Name: "a", Line: 2, Env: map[string]string{"HOST": "https://app.test/x", "PLAIN": "one"},
			EnvFiles: []string{"one.env"}, ExtraHosts: []string{"api.x.io", "b.x.io"}, Sources: []string{"."}},
		{Name: "b", Line: 12, Env: map[string]string{"K": "v", "FROM_ENV": "val"},
			EnvFiles: []string{"two.env", "three.env"}, ExtraHosts: []string{"c.x.io"},
			Sources: []string{"./", "/p/root.crt"}, HostNetwork: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestComposeServicesRefusesBadYAML(t *testing.T) {
	if _, err := composeServices([]byte("services: [\n"), nil); err == nil {
		t.Error("no error")
	}
}

func TestComposeServicesFollowsMergeKeys(t *testing.T) {
	const src = `x-common: &common
  environment:
    CALLBACK: https://api.x.io
  extra_hosts:
    - "api.x.io:host-gateway"
x-net: &net
  network_mode: host
  extra_hosts:
    - "ignored.x.io:host-gateway"
services:
  a:
    <<: *common
  b:
    <<: [*net, *common]
  c:
    <<: *common
    extra_hosts:
      - "own.x.io:host-gateway"
`
	got, err := composeServices([]byte(src), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	a, b, c := got[0], got[1], got[2]
	if !reflect.DeepEqual(a.ExtraHosts, []string{"api.x.io"}) || a.Env["CALLBACK"] != "https://api.x.io" {
		t.Errorf("a = %+v", a)
	}
	// The earlier entry of a merge sequence wins, and the other still supplies what it lacks.
	if !reflect.DeepEqual(b.ExtraHosts, []string{"ignored.x.io"}) || !b.HostNetwork || b.Env["CALLBACK"] != "https://api.x.io" {
		t.Errorf("b = %+v", b)
	}
	if !reflect.DeepEqual(c.ExtraHosts, []string{"own.x.io"}) || c.Env["CALLBACK"] != "https://api.x.io" {
		t.Errorf("c = %+v", c)
	}
}

func TestComposeServicesFollowsMergeKeysInsideMaps(t *testing.T) {
	const src = `x-shared: &shared
  NODE_EXTRA_CA_CERTS: /ca.crt
  KEEP: merged
x-hosts: &hosts
  api.x.io: host-gateway
services:
  <<: {z: {image: z}}
  a:
    environment:
      <<: *shared
      OTHER: x
      KEEP: own
    extra_hosts:
      <<: *hosts
`
	got, err := composeServices([]byte(src), nil)
	if err != nil {
		t.Fatal(err)
	}
	var a service
	for _, s := range got {
		if s.Name == "<<" {
			t.Errorf("merge key read as a service")
		}
		if s.Name == "a" {
			a = s
		}
	}
	want := map[string]string{"NODE_EXTRA_CA_CERTS": "/ca.crt", "OTHER": "x", "KEEP": "own"}
	if !reflect.DeepEqual(a.Env, want) || !reflect.DeepEqual(a.ExtraHosts, []string{"api.x.io"}) {
		t.Errorf("a = %+v", a)
	}
}
