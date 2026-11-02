package web

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Access says who may call an operation. It comes from the
// x-hogwatch-access mark of the operation in api/openapi.yaml.
type Access int

const (
	// AccessPublic: anyone.
	AccessPublic Access = iota + 1
	// AccessUser: any logged-in user.
	AccessUser
	// AccessScoped: a logged-in user; results are limited to their paths.
	AccessScoped
	// AccessListing: a scoped user with can_list_files, or an admin.
	AccessListing
	// AccessAdmin: admins only.
	AccessAdmin
)

var accessNames = map[string]Access{
	"public": AccessPublic, "user": AccessUser, "scoped": AccessScoped,
	"listing": AccessListing, "admin": AccessAdmin,
}

var methods = []string{"get", "put", "post", "delete", "patch", "head", "options", "trace"}

// AccessTable maps each operation of spec to its access mark, keyed by the
// ServeMux pattern the operation is registered under, for example
// "GET /api/v1/nodes/{ref}". An operation without a valid mark is an error:
// no endpoint can exist without an access decision.
func AccessTable(spec []byte) (map[string]Access, error) {
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		return nil, fmt.Errorf("web: parse spec: %w", err)
	}
	table := map[string]Access{}
	for path, item := range doc.Paths {
		for method, node := range item {
			if !slices.Contains(methods, method) {
				continue
			}
			var op struct {
				ID     string `yaml:"operationId"`
				Access string `yaml:"x-hogwatch-access"`
			}
			if err := node.Decode(&op); err != nil {
				return nil, fmt.Errorf("web: %s %s: %w", method, path, err)
			}
			a, ok := accessNames[op.Access]
			if !ok {
				return nil, fmt.Errorf("web: operation %s (%s %s) has no valid x-hogwatch-access (%q)", op.ID, method, path, op.Access)
			}
			table[strings.ToUpper(method)+" "+path] = a
		}
	}
	return table, nil
}
