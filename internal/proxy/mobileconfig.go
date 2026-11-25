package proxy

import (
	"crypto/rand"
	"fmt"

	"howett.net/plist"
)

// profilePayload is one payload of a configuration profile, or the profile
// itself, as Apple's Configuration Profile Reference names its keys.
type profilePayload struct {
	PayloadType        string `plist:"PayloadType"`
	PayloadVersion     int    `plist:"PayloadVersion"`
	PayloadIdentifier  string `plist:"PayloadIdentifier"`
	PayloadUUID        string `plist:"PayloadUUID"`
	PayloadDisplayName string `plist:"PayloadDisplayName"`
	PayloadDescription string `plist:"PayloadDescription,omitempty"`
	PayloadContent     any    `plist:"PayloadContent"`
	CertificateName    string `plist:"PayloadCertificateFileName,omitempty"`
}

// mobileconfig is a configuration profile that installs rootDER, the local
// CA's root certificate, as a trusted root: one com.apple.security.root
// payload, with fresh UUIDs, in a profile identified as dev.nearhost.ca so
// a new one replaces the old.
func mobileconfig(rootDER []byte, caName string) []byte {
	root := profilePayload{
		PayloadType:        "com.apple.security.root",
		PayloadVersion:     1,
		PayloadIdentifier:  "dev.nearhost.ca.root",
		PayloadUUID:        newUUID(),
		PayloadDisplayName: caName,
		PayloadContent:     rootDER,
		CertificateName:    "nearhost-root.cer",
	}
	profile := profilePayload{
		PayloadType:        "Configuration",
		PayloadVersion:     1,
		PayloadIdentifier:  "dev.nearhost.ca",
		PayloadUUID:        newUUID(),
		PayloadDisplayName: "Nearhost: " + caName,
		PayloadDescription: "Trusts the certificate authority Nearhost signs its sites with, so this device can open them.",
		PayloadContent:     []profilePayload{root},
	}
	out, err := plist.MarshalIndent(profile, plist.XMLFormat, "\t")
	if err != nil {
		// Only strings, ints and bytes go in, which always encode.
		panic(fmt.Sprintf("proxy: mobileconfig: %v", err))
	}
	return out
}

// newUUID is a random (version 4) UUID, in upper case as Apple writes them.
func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("proxy: no randomness: %v", err))
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%X-%X-%X-%X-%X", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
