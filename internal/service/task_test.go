package service

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

func decodeUTF16(t *testing.T, b []byte) string {
	t.Helper()
	var u []uint16
	for i := 2; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return string(utf16.Decode(u))
}

func TestRoundTripTask(t *testing.T) {
	win := Definition{Exe: `C:\Users\Ed Smith\AppData\Local\nearhost\nearhost.exe`}
	winArgs := Definition{Exe: `C:\Users\Ed "Q" Smith\near&host<1>.exe`, Args: []string{"--web-port", "0", "", `a b\`}}
	for name, d := range map[string]Definition{"plain": win, "nasty": winArgs, "unix nasty": nasty(), "empty": {Exe: "x"}} {
		t.Run(name, func(t *testing.T) {
			got, err := ParseTask(RenderTask(d, `PC\Ed`))
			if err != nil {
				t.Fatal(err)
			}
			if want := d.For(Task); !reflect.DeepEqual(got, want) {
				t.Errorf("got %+v, want %+v", got, want)
			}
		})
	}
}

func TestTaskXML(t *testing.T) {
	b := RenderTask(Definition{Exe: `C:\Program Files\nearhost.exe`}, `PC\Ed`)
	if !bytes.HasPrefix(b, []byte{0xFF, 0xFE}) {
		t.Fatalf("no UTF-16LE byte order mark: % x", b[:4])
	}
	s := decodeUTF16(t, b)
	for _, want := range []string{
		`<Command>%SystemRoot%\System32\conhost.exe</Command>`,
		`<Arguments>--headless &#34;C:\Program Files\nearhost.exe&#34; daemon --started-by service</Arguments>`,
		"<LogonType>InteractiveToken</LogonType>",
		"<RunLevel>LeastPrivilege</RunLevel>",
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
		"<UserId>PC\\Ed</UserId>",
		"<Interval>PT1M</Interval>",
		"<Count>3</Count>",
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("task lacks %q:\n%s", want, s)
		}
	}
	// schtasks /Query /XML output may also arrive as UTF-8.
	got, err := ParseTask([]byte(s))
	if err != nil || got.Exe != `C:\Program Files\nearhost.exe` {
		t.Errorf("UTF-8: %+v, %v", got, err)
	}
}
