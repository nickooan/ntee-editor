// Package graphql renders GraphQL SDL schemas as read-only styled documents,
// mirroring internal/openapi: pure data-in/data-out, no filesystem or UI —
// all IO is injected by the caller (GatherIO).
//
// Unlike OpenAPI there is no $ref: the ecosystem convention is to concatenate
// every SDL file and merge (`extend type` is the native cross-file mechanism),
// with the file set optionally declared by a GraphQL Config file. There is
// also no recursive schema expansion — every type reference is one hop to a
// sibling top-level block, so no depth budgets or cycle guards are needed.
package graphql

import (
	"path"
	"strings"
)

// Detect reports whether the file is a GraphQL SDL file. Strictly
// extension-based: the mode is only enterable from the @exec bar while
// editing a GraphQL file, so this is the pre-flight guard that produces the
// "not a graphql file" alert (the openapi.Detect role, keyed on file type
// instead of content).
func Detect(filename string) bool {
	switch strings.ToLower(path.Ext(filename)) {
	case ".graphql", ".gql", ".graphqls":
		return true
	}
	return false
}
