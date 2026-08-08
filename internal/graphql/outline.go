package graphql

// OutlineEntry is one sidebar row: a group header (Depth 0) or a root field /
// type definition (Depth 1). LineIdx anchors it into the rendered document.
type OutlineEntry struct {
	Label   string
	Kind    string // "query", "mutation", "subscription", "type", "input", … ; "" for headers
	LineIdx int
	Depth   int
}

// KindColor is the ANSI color name for an outline kind badge, for callers
// styling sidebar rows outside this package (the openapi.MethodColor role).
func KindColor(kind string) string {
	switch kind {
	case "query":
		return "green"
	case "mutation":
		return "yellow"
	case "subscription":
		return "magenta"
	case "type":
		return "cyan"
	case "interface":
		return "blue"
	case "input":
		return "yellow"
	case "enum":
		return "green"
	case "union":
		return "magenta"
	case "scalar":
		return ""
	case "directive":
		return "red"
	}
	return "cyan"
}

// KindBadge is the short sidebar badge for a kind — the longest full kind
// ("subscription") would eat the narrow outline pane.
func KindBadge(kind string) string {
	switch kind {
	case "query":
		return "qry"
	case "mutation":
		return "mut"
	case "subscription":
		return "sub"
	case "type":
		return "type"
	case "interface":
		return "ifce"
	case "input":
		return "input"
	case "enum":
		return "enum"
	case "union":
		return "union"
	case "scalar":
		return "scal"
	case "directive":
		return "dir"
	}
	return kind
}
