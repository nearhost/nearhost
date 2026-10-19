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
