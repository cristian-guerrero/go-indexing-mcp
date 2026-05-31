package graph

// FormatSymbolInfoResult is a clean JSON-friendly representation of SymbolInfo
// with only user-facing fields. Each definition carries its own callers and
// callees so the relationship is explicit.
type FormatSymbolInfoResult struct {
	Definitions []FormatSymbolDef `json:"definitions"`
	Usages      []FormatRefResult `json:"usages"`
}

// FormatSymbolDef is a clean representation of a symbol definition with its
// associated callers and callees.
type FormatSymbolDef struct {
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	FilePath  string            `json:"file_path"`
	StartLine int               `json:"start_line"`
	EndLine   int               `json:"end_line"`
	Signature string            `json:"signature,omitempty"`
	Callers   []FormatRefResult `json:"callers"`
	Callees   []FormatRefResult `json:"callees"`
}

// FormatRefResult is a clean representation of a reference (usage/caller/callee).
type FormatRefResult struct {
	TargetName string  `json:"target_name"`
	Kind       string  `json:"kind,omitempty"`
	FilePath   string  `json:"file_path"`
	Line       int     `json:"line"`
	Confidence float64 `json:"confidence,omitempty"`
}

// FormatSymbolInfo converts SymbolInfo into its clean output representation
// with only user-facing fields. Each definition includes its own callers and
// callees arrays so the caller/callee → definition relationship is explicit.
func FormatSymbolInfo(info *SymbolInfo) *FormatSymbolInfoResult {
	if info == nil {
		return nil
	}
	result := &FormatSymbolInfoResult{
		Definitions: make([]FormatSymbolDef, 0, len(info.Definitions)),
		Usages:      make([]FormatRefResult, 0, len(info.Usages)),
	}
	for _, d := range info.Definitions {
		def := FormatSymbolDef{
			Name:      d.Name,
			Kind:      d.Kind.String(),
			FilePath:  d.FilePath,
			StartLine: d.StartLine,
			EndLine:   d.EndLine,
			Signature: d.Signature,
			Callers:   make([]FormatRefResult, 0, len(d.Callers)),
			Callees:   make([]FormatRefResult, 0, len(d.Callees)),
		}
		for _, c := range d.Callers {
			def.Callers = append(def.Callers, FormatRefResult{
				TargetName: c.TargetName,
				FilePath:   c.FilePath,
				Line:       c.Line,
			})
		}
		for _, c := range d.Callees {
			def.Callees = append(def.Callees, FormatRefResult{
				TargetName: c.TargetName,
				FilePath:   c.FilePath,
				Line:       c.Line,
			})
		}
		result.Definitions = append(result.Definitions, def)
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

	// Suppress usages when they are the same as the combined callers across all
	// definitions — usages at the top level would just repeat per-definition data.
	if len(result.Usages) > 0 {
		var flatCallers []FormatRefResult
		for _, d := range result.Definitions {
			flatCallers = append(flatCallers, d.Callers...)
		}
		if refSetsEqual(flatCallers, result.Usages) {
			result.Usages = nil
		}
	}

	return result
}

// refKey is a comparable identity for a reference, ignoring Kind and Confidence.
func refKey(r FormatRefResult) string {
	return r.TargetName + "\x00" + r.FilePath + "\x00" + itoa(r.Line)
}

// refSetsEqual checks whether two slices contain the same set of references
// (by TargetName + FilePath + Line), ignoring Kind, Confidence, and order.
func refSetsEqual(a, b []FormatRefResult) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]int, len(a))
	for _, r := range a {
		set[refKey(r)]++
	}
	for _, r := range b {
		k := refKey(r)
		set[k]--
		if set[k] < 0 {
			return false
		}
	}
	return true
}
