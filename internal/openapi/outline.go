package openapi

// OutlineEntry is one sidebar row: a tag group header (Depth 0) or an
// operation (Depth 1). LineIdx anchors it into the rendered document.
type OutlineEntry struct {
	Label   string
	Method  string // empty for tag headers
	Path    string // empty for tag headers
	LineIdx int
	Depth   int
}
