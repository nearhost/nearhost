// Package api holds the OpenAPI schema of the core's control API, the
// contract between the core and its clients: the terminal interface, the
// CLI and the web app. The schema is the file openapi.yaml beside this one.
//
// The package only embeds the file. Tests in internal/control hold the
// routes and Go types to it.
package api

import _ "embed"

// Schema is openapi.yaml: the control API as OpenAPI 3.1.
//
//go:embed openapi.yaml
var Schema []byte
