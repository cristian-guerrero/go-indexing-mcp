package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
)

// ---- Graph Operations ----

// StoreFile stores symbols and references for a file atomically.
// Removes any existing data for the same relPath first.
func (s *Store) StoreFile(relPath string, symbols []Symbol, refs []Reference) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Remove existing data for this file
	s.removeFileTx(tx, relPath)

	symStmt, err := tx.Prepare(`INSERT OR REPLACE INTO symbols
		(id, name, kind, file_path, rel_path, start_line, end_line, signature, exported)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer symStmt.Close()

	for _, sym := range symbols {
		exported := 0
		if sym.Exported {
			exported = 1
		}
		if _, err := symStmt.Exec(sym.ID, sym.Name, int(sym.Kind),
			sym.FilePath, sym.RelPath, sym.StartLine, sym.EndLine,
			sym.Signature, exported); err != nil {
			return fmt.Errorf("insert symbol %s: %w", sym.ID, err)
		}
	}

	refStmt, err := tx.Prepare(`INSERT OR REPLACE INTO refs
		(id, source_id, target_name, target_id, kind, file_path, line, confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer refStmt.Close()

	for _, ref := range refs {
		if _, err := refStmt.Exec(ref.ID, ref.SourceID, ref.TargetName,
			ref.TargetID, int(ref.Kind), ref.FilePath, ref.Line, ref.Confidence); err != nil {
			return fmt.Errorf("insert ref %s: %w", ref.ID, err)
		}
	}

	return tx.Commit()
}

// RemoveFile removes all symbols and references for a file.
func (s *Store) RemoveFile(relPath string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	s.removeFileTx(tx, relPath)
	return tx.Commit()
}

func (s *Store) removeFileTx(tx *sql.Tx, relPath string) {
	tx.Exec("DELETE FROM symbols WHERE rel_path = ?", relPath)
	tx.Exec("DELETE FROM refs WHERE file_path = ?", relPath)
}

// HasFile returns true if symbols exist for the given relative path.
func (s *Store) HasFile(relPath string) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM symbols WHERE rel_path = ?", relPath).Scan(&count)
	return count > 0, err
}

// ResolveRefs resolves unresolved references (empty TargetID) by matching
// TargetName against known symbol names. Returns count of resolved refs.
func (s *Store) ResolveRefs() (int, error) {
	rows, err := s.db.Query(
		`SELECT r.id, r.target_name, r.file_path
		 FROM refs r
		 WHERE r.target_id = '' OR r.target_id IS NULL`)
	if err != nil {
		return 0, err
	}

	type unresolved struct {
		id         string
		targetName string
		filePath   string
	}
	var pending []unresolved
	for rows.Next() {
		var u unresolved
		if err := rows.Scan(&u.id, &u.targetName, &u.filePath); err != nil {
			continue
		}
		pending = append(pending, u)
	}
	rows.Close()

	if len(pending) == 0 {
		return 0, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	resolved := 0
	stmt, _ := tx.Prepare("UPDATE refs SET target_id = ? WHERE id = ?")
	for _, p := range pending {
		var targetID string
		// Prefer same-file definition first, fall back to any definition with that name.
		// This prevents cross-file collisions (e.g. detectLanguage in walker.go and indexer.go).
		err := tx.QueryRow(
			"SELECT id FROM symbols WHERE name = ? ORDER BY CASE WHEN file_path = ? THEN 0 ELSE 1 END LIMIT 1",
			p.targetName, p.filePath).Scan(&targetID)
		if err != nil || targetID == "" {
			continue
		}
		if _, err := stmt.Exec(targetID, p.id); err == nil {
			resolved++
		}
	}

	if resolved > 0 {
		slog.Info("graph: cross-file references resolved", "count", resolved)
	}

	return resolved, tx.Commit()
}

// FindDefinition looks up a symbol definition by name.
// For methods (kind=1), also matches by suffix (e.g. querying "GetCallers"
// finds "(*GraphQuery).GetCallers").
func (s *Store) FindDefinition(name string, pathFilter string) ([]Symbol, error) {
	rows, err := s.db.Query(
		`SELECT id, name, kind, file_path, rel_path, start_line, end_line, signature, exported
		 FROM symbols WHERE name = ? OR (kind = ? AND name LIKE ?)
		 ORDER BY kind, rel_path`, name, int(SymbolMethod), "%."+name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Symbol
	for rows.Next() {
		var sym Symbol
		var kind int
		var exported int
		if err := rows.Scan(&sym.ID, &sym.Name, &kind, &sym.FilePath,
			&sym.RelPath, &sym.StartLine, &sym.EndLine, &sym.Signature, &exported); err != nil {
			return nil, err
		}
		sym.Kind = SymbolKind(kind)
		sym.Exported = exported != 0

		if pathFilter != "" && !matchesFilter(sym.RelPath, pathFilter) {
			continue
		}
		result = append(result, sym)
	}
	return result, rows.Err()
}

// FindUsages returns all references to a symbol by name.
func (s *Store) FindUsages(name string, pathFilter string) ([]Reference, error) {
	rows, err := s.db.Query(
		`SELECT r.id, r.source_id, r.target_name, r.target_id, r.kind, r.file_path, r.line, r.confidence
		 FROM refs r
		 WHERE r.target_name = ?
		 ORDER BY r.file_path, r.line`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Reference
	for rows.Next() {
		var ref Reference
		var kind int
		if err := rows.Scan(&ref.ID, &ref.SourceID, &ref.TargetName, &ref.TargetID,
			&kind, &ref.FilePath, &ref.Line, &ref.Confidence); err != nil {
			return nil, err
		}
		ref.Kind = RefKind(kind)

		if pathFilter != "" && !matchesFilter(ref.FilePath, pathFilter) {
			continue
		}
		result = append(result, ref)
	}
	return result, rows.Err()
}

// FindImports returns import symbols matching a module path pattern.
func (s *Store) FindImports(pattern string) ([]*Symbol, error) {
	// Use LIKE for pattern matching (partial match on module paths)
	rows, err := s.db.Query(
		`SELECT id, name, kind, file_path, rel_path, start_line, end_line, signature, exported
		 FROM symbols
		 WHERE kind = ? AND (name LIKE ? OR signature LIKE ?)
		 ORDER BY name`, int(SymbolImport), "%"+pattern+"%", "%"+pattern+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*Symbol
	for rows.Next() {
		var sym Symbol
		var kind int
		var exported int
		if err := rows.Scan(&sym.ID, &sym.Name, &kind, &sym.FilePath,
			&sym.RelPath, &sym.StartLine, &sym.EndLine, &sym.Signature, &exported); err != nil {
			return nil, err
		}
		sym.Kind = SymbolKind(kind)
		sym.Exported = exported != 0
		result = append(result, &sym)
	}
	return result, rows.Err()
}

// GetCallers returns references that call a given function/method name.
func (s *Store) GetCallers(name string) ([]Reference, error) {
	rows, err := s.db.Query(
		`SELECT r.id, r.source_id, r.target_name, r.target_id, r.kind, r.file_path, r.line, r.confidence
		 FROM refs r
		 WHERE r.target_name = ? AND r.kind = ?
		 ORDER BY r.file_path, r.line`, name, int(RefCalls))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Reference
	for rows.Next() {
		var ref Reference
		var kind int
		if err := rows.Scan(&ref.ID, &ref.SourceID, &ref.TargetName, &ref.TargetID,
			&kind, &ref.FilePath, &ref.Line, &ref.Confidence); err != nil {
			return nil, err
		}
		ref.Kind = RefKind(kind)
		result = append(result, ref)
	}
	return result, rows.Err()
}

// GetCallees returns all function names called within a symbol.
func (s *Store) GetCallees(symID string) ([]Reference, error) {
	rows, err := s.db.Query(
		`SELECT r.id, r.source_id, r.target_name, r.target_id, r.kind, r.file_path, r.line, r.confidence
		 FROM refs r
		 WHERE r.source_id = ? AND r.kind = ?
		 ORDER BY r.line`, symID, int(RefCalls))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Reference
	for rows.Next() {
		var ref Reference
		var kind int
		if err := rows.Scan(&ref.ID, &ref.SourceID, &ref.TargetName, &ref.TargetID,
			&kind, &ref.FilePath, &ref.Line, &ref.Confidence); err != nil {
			return nil, err
		}
		ref.Kind = RefKind(kind)
		result = append(result, ref)
	}
	return result, rows.Err()
}

// GraphStats returns symbol and reference counts via independent subqueries
// to avoid a cross-join that would be catastrophically slow on large graphs.
func (s *Store) GraphStats() (symbols, refs int, err error) {
	err = s.db.QueryRow(
		"SELECT (SELECT COUNT(*) FROM symbols), (SELECT COUNT(*) FROM refs)",
	).Scan(&symbols, &refs)
	return
}

// GetFileRefs returns references of a specific kind for a file.
// Pass kind=-1 to return all references (used by tests).
func (s *Store) GetFileRefs(relPath string, kind RefKind) ([]Reference, error) {
	var rows *sql.Rows
	var err error
	if kind >= 0 {
		rows, err = s.db.Query(
			`SELECT id, source_id, target_name, target_id, kind, file_path, line, confidence
			 FROM refs WHERE file_path = ? AND kind = ?`, relPath, int(kind))
	} else {
		rows, err = s.db.Query(
			`SELECT id, source_id, target_name, target_id, kind, file_path, line, confidence
			 FROM refs WHERE file_path = ?`, relPath)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Reference
	for rows.Next() {
		var ref Reference
		var k int
		if err := rows.Scan(&ref.ID, &ref.SourceID, &ref.TargetName, &ref.TargetID,
			&k, &ref.FilePath, &ref.Line, &ref.Confidence); err != nil {
			return nil, err
		}
		ref.Kind = RefKind(k)
		result = append(result, ref)
	}
	return result, rows.Err()
}

// matchesFilter checks if a relative path matches a path filter prefix.
func matchesFilter(relPath, filter string) bool {
	if filter == "" {
		return true
	}
	relPath = filepath.ToSlash(relPath)
	filter = filepath.ToSlash(filter)
	return len(relPath) >= len(filter) && relPath[:len(filter)] == filter
}

// filterRefsByPath filters references by a path filter prefix.
func filterRefsByPath(refs []Reference, pathFilter string) []Reference {
	var out []Reference
	for _, r := range refs {
		if matchesFilter(r.FilePath, pathFilter) {
			out = append(out, r)
		}
	}
	return out
}

// GraphNeedsReindex checks the graph format version.
func (s *Store) GraphNeedsReindex() bool {
	raw := s.getMeta(nil, "graph_format_version")
	if raw == "" {
		return false
	}
	if raw == "0" {
		return false
	}
	if raw != strconv.Itoa(GraphFormatVersion) {
		return true
	}
	return false
}


