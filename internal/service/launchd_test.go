package service

import (
	"reflect"
	"strings"
	"testing"
)

func TestRoundTripPlist(t *testing.T) {
	for name, d := range map[string]Definition{"plain": plain(), "nasty": nasty(), "empty": {Exe: "/x"}} {
		t.Run(name, func(t *testing.T) {
			got, err := ParsePlist(RenderPlist("dev.nearhost.core", d))
			if err != nil {
				t.Fatal(err)
			}
			if want := d.For(Plist); !reflect.DeepEqual(got, want) {
				t.Errorf("got %+v, want %+v", got, want)
			}
		})
	}
}

func TestPlistKeepAlive(t *testing.T) {
	s := string(RenderPlist("dev.nearhost.core", plain()))
	for _, want := range []string{
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>",
		"<string>dev.nearhost.core</string>",
		"<key>StandardOutPath</key>\n\t<string>/dev/null</string>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("plist lacks %q:\n%s", want, s)
		}
	}
}

func TestParsePlistRejects(t *testing.T) {
	for _, in := range []string{"", "not xml", "<plist><dict><key>Label</key><string>x</string></dict></plist>"} {
		if _, err := ParsePlist([]byte(in)); err == nil {
			t.Errorf("ParsePlist(%q) succeeded", in)
		}
	}
}
