package graph

// FormatSymbolInfoResult is a clean JSON-friendly representation of SymbolInfo
// with only user-facing fields.
type FormatSymbolInfoResult struct {
	Definitions []FormatSymbolDef `json:"definitions"`
	Usages      []FormatRefResult `json:"usages"`
	Callers     []FormatRefResult `json:"callers"`
	Callees     []FormatRefResult `json:"callees"`
}

// FormatSymbolDef is a clean representation of a symbol definition.
type FormatSymbolDef struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	FilePath  string `json:"file_path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Signature string `json:"signature,omitempty"`
}

// FormatRefResult is a clean representation of a reference (usage/caller/callee).
type FormatRefResult struct {
	TargetName string  `json:"target_name"`
	Kind       string  `json:"kind"`
	FilePath   string  `json:"file_path"`
	Line       int     `json:"line"`
	Confidence float64 `json:"confidence,omitempty"`
}

// FormatSymbolInfo converts SymbolInfo into its clean output representation
// with only user-facing fields.
func FormatSymbolInfo(info *SymbolInfo) *FormatSymbolInfoResult {
	if info == nil {
		return nil
	}
	result := &FormatSymbolInfoResult{
		Definitions: make([]FormatSymbolDef, 0, len(info.Definitions)),
		Usages:      make([]FormatRefResult, 0, len(info.Usages)),
		Callers:     make([]FormatRefResult, 0, len(info.Callers)),
		Callees:     make([]FormatRefResult, 0, len(info.Callees)),
	}
	for _, d := range info.Definitions {
		result.Definitions = append(result.Definitions, FormatSymbolDef{
			Name:      d.Name,
			Kind:      d.Kind.String(),
			FilePath:  d.FilePath,
			StartLine: d.StartLine,
			EndLine:   d.EndLine,
			Signature: d.Signature,
		})
	}
	for _, u := range info.Usages {
		result.Usages = append(result.Usages, FormatRefResult{
			TargetName: u.TargetName,
			Kind:       u.Kind.String(),
			FilePath:   u.FilePath,
			Line:       u.Line,
			Confidence: u.Confidence,
		})
	}
	for _, c := range info.Callers {
		result.Callers = append(result.Callers, FormatRefResult{
			TargetName: c.TargetName,
			Kind:       c.Kind.String(),
			FilePath:   c.FilePath,
			Line:       c.Line,
		})
	}
	for _, c := range info.Callees {
		result.Callees = append(result.Callees, FormatRefResult{
			TargetName: c.TargetName,
			Kind:       c.Kind.String(),
			FilePath:   c.FilePath,
			Line:       c.Line,
		})
	}

	// Deduplicate: if usages and callers are identical (same ref entries),
	// keep only callers (more specific) and clear usages to avoid redundant output.
	// Only dedup when both have content — two empty slices should both stay as [].
	if len(result.Usages) > 0 && refsEqual(result.Usages, result.Callers) {
		result.Usages = nil
	}

	return result
}

// refsEqual compares two ref slices by their identity fields, ignoring Confidence
// (which is only populated in usages, not in callers/callees).
func refsEqual(a, b []FormatRefResult) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].TargetName != b[i].TargetName ||
			a[i].Kind != b[i].Kind ||
			a[i].FilePath != b[i].FilePath ||
			a[i].Line != b[i].Line {
			return false
		}
	}
	return true
}
