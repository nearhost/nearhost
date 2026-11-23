package service

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestRoundTripUnit(t *testing.T) {
	for name, d := range map[string]Definition{"plain": plain(), "nasty": nasty(), "empty": {Exe: "/x"}} {
		for _, g := range []bool{false, true} {
			d.Graphical = g
			t.Run(fmt.Sprintf("%s graphical=%v", name, g), func(t *testing.T) {
				got, err := ParseUnit(RenderUnit(d))
				if err != nil {
					t.Fatal(err)
				}
				if want := d.For(Unit); !reflect.DeepEqual(got, want) {
					t.Errorf("got %+v, want %+v", got, want)
				}
			})
		}
	}
}

func TestUnitQuoting(t *testing.T) {
	d := Definition{Exe: "/bin/a%b$c", Env: []EnvVar{{"K", `v%$"\`}}, LogPath: "/l/50%.log"}
	s := string(RenderUnit(d))
	for _, want := range []string{
		`ExecStart="/bin/a%%b$$c" "daemon" "--started-by" "service"`,
		`Environment="K=v%%$\"\\"`,
		"StandardError=append:/l/50%%.log",
		"Restart=on-failure\nRestartSec=5",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("unit lacks %q:\n%s", want, s)
		}
	}
}

func TestUnitGraphical(t *testing.T) {
	on := string(RenderUnit(Definition{Exe: "/x", Graphical: true}))
	if !strings.Contains(on, "After=graphical-session.target\n") || !strings.Contains(on, "WantedBy=graphical-session.target\n") {
		t.Errorf("graphical unit:\n%s", on)
	}
	off := string(RenderUnit(Definition{Exe: "/x"}))
	if strings.Contains(off, "After=") || !strings.Contains(off, "WantedBy=default.target\n") {
		t.Errorf("default unit:\n%s", off)
	}
}
