package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
)

const plistHeader = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
`

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// RenderPlist is d as a launchd agent plist labelled label. launchd restarts
// the daemon after a failure but not after a clean exit, which is how
// nearhost stop stays stopped.
func RenderPlist(label string, d Definition) []byte {
	var b bytes.Buffer
	b.WriteString(plistHeader)
	b.WriteString("<dict>\n")
	fmt.Fprintf(&b, "\t<key>Label</key>\n\t<string>%s</string>\n", xmlText(label))
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range d.command() {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", xmlText(a))
	}
	b.WriteString("\t</array>\n")
	if len(d.Env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, e := range d.Env {
			fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", xmlText(e.Name), xmlText(e.Value))
		}
		b.WriteString("\t</dict>\n")
	}
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	b.WriteString("\t<key>ProcessType</key>\n\t<string>Interactive</string>\n")
	if d.LogPath != "" {
		fmt.Fprintf(&b, "\t<key>StandardErrorPath</key>\n\t<string>%s</string>\n", xmlText(d.LogPath))
	}
	b.WriteString("\t<key>StandardOutPath</key>\n\t<string>/dev/null</string>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

// plistPair is one key and value of a plist dict, in file order.
type plistPair struct {
	key string
	val any // string, bool, []any or []plistPair
}

// ParsePlist reads a plist RenderPlist could have written.
func ParsePlist(b []byte) (Definition, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		t, err := dec.Token()
		if err != nil {
			return Definition{}, errors.New("not a launchd plist")
		}
		if s, ok := t.(xml.StartElement); ok && s.Name.Local == "dict" {
			v, err := plistValue(dec, s)
			if err != nil {
				return Definition{}, err
			}
			return definitionFromPlist(v.([]plistPair))
		}
	}
}

// plistValue reads the value that starts at start.
func plistValue(dec *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "true":
		return true, dec.Skip()
	case "false":
		return false, dec.Skip()
	case "string", "integer", "real", "date", "data":
		var s string
		return s, dec.DecodeElement(&s, &start)
	case "array":
		var out []any
		for {
			t, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := t.(type) {
			case xml.StartElement:
				v, err := plistValue(dec, t)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			case xml.EndElement:
				return out, nil
			}
		}
	case "dict":
		var out []plistPair
		key, haveKey := "", false
		for {
			t, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := t.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					if err := dec.DecodeElement(&key, &t); err != nil {
						return nil, err
					}
					haveKey = true
					continue
				}
				if !haveKey {
					return nil, errors.New("plist value without a key")
				}
				v, err := plistValue(dec, t)
				if err != nil {
					return nil, err
				}
				out = append(out, plistPair{key, v})
				haveKey = false
			case xml.EndElement:
				return out, nil
			}
		}
	}
	return nil, io.ErrUnexpectedEOF
}

func definitionFromPlist(top []plistPair) (Definition, error) {
	var d Definition
	var argv []string
	for _, p := range top {
		switch p.key {
		case "ProgramArguments":
			arr, _ := p.val.([]any)
			for _, a := range arr {
				s, ok := a.(string)
				if !ok {
					return Definition{}, errors.New("ProgramArguments holds something other than strings")
				}
				argv = append(argv, s)
			}
		case "EnvironmentVariables":
			dict, _ := p.val.([]plistPair)
			for _, e := range dict {
				s, _ := e.val.(string)
				d.Env = append(d.Env, EnvVar{e.key, s})
			}
		case "StandardErrorPath":
			d.LogPath, _ = p.val.(string)
		}
	}
	exe, args, ok := fromCommand(argv)
	if !ok {
		return Definition{}, errors.New("the plist does not start nearhost daemon --started-by service")
	}
	d.Exe, d.Args = exe, args
	return d, nil
}
