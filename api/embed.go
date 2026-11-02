// Package api embeds the OpenAPI contract the server and the code generators
// share.
package api

import _ "embed"

// Spec is api/openapi.yaml.
//
//go:embed openapi.yaml
var Spec []byte
