package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gameagent/runtime/internal/definition"
	"gameagent/runtime/internal/session"
	"gameagent/runtime/internal/textsearch"
	"gameagent/runtime/internal/tokenestimate"

	_ "modernc.org/sqlite"
)

const (
	MaxFileBytes    = 2 << 20
	MaxEntries      = 512
	MaxQueryChars   = 256
	MaxQueryTerms   = 32
	MaxEntryTokens  = 512
	AutomaticLimit  = 3
	AutomaticTokens = 768
	ToolLimit       = 3
	ToolTokens      = 1024
	SearchTimeout   = 500 * time.Millisecond
)

type Config struct {
	Enabled bool `json:"enabled"`
}

type Catalog struct {
	SchemaVersion string  `json:"schema_version"`
	GameID        string  `json:"game_id"`
	SourceVersion string  `json:"source_version"`
	Entries       []Entry `json:"entries"`
}

type Visibility struct {
	Kind          string   `json:"kind"`
	DefinitionIDs []string `json:"definition_ids,omitempty"`
}

type Entry struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Aliases    []string   `json:"aliases"`
	Visibility Visibility `json:"visibility"`
	Text       string     `json:"text"`
}

type ReadScope struct {
	Owner        session.AgentSessionKey
	DefinitionID string
	DisplayName  string
	ContentHash  string
}

type Snippet struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

type Result struct {
	Status        string    `json:"status"`
	SourceVersion string    `json:"source_version"`
	ContentHash   string    `json:"content_hash"`
	Entries       []Snippet `json:"entries"`
	Truncated     bool      `json:"truncated"`
}

// Service owns one immutable catalog and its derived, instance-local SQLite index.
type Service struct {
	db      *sql.DB
	gameID  string
	version string
	hash    string
	catalog Catalog
}

func Load(ctx context.Context, root, gameID string, definitions definition.Catalog) (*Service, error) {
	data, err := readFile(root, gameID)
	if err != nil {
		return nil, err
	}
	return Open(ctx, data, gameID, definitions)
}

func ValidateFile(root, gameID string, definitions definition.Catalog) error {
	data, err := readFile(root, gameID)
	if err != nil {
		return err
	}
	_, err = Parse(data, gameID, definitions)
	return err
}

func readFile(root, gameID string) ([]byte, error) {
	f, err := os.Open(filepath.Join(root, gameID, "knowledge", "catalog.json"))
	if err != nil {
		return nil, fmt.Errorf("open knowledge catalog: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	return data, nil
}

func Parse(data []byte, gameID string, definitions definition.Catalog) (Catalog, error) {
	var c Catalog
	if len(data) > MaxFileBytes {
		return c, fmt.Errorf("knowledge catalog exceeds %d bytes", MaxFileBytes)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("parse knowledge catalog: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("knowledge catalog must contain one JSON object")
	}
	if c.SchemaVersion != "v1" || c.GameID != gameID || strings.TrimSpace(c.SourceVersion) == "" || utf8.RuneCountInString(c.SourceVersion) > 64 {
		return c, fmt.Errorf("invalid knowledge schema, game or source version")
	}
	if len(c.Entries) == 0 || len(c.Entries) > MaxEntries {
		return c, fmt.Errorf("knowledge catalog requires 1..%d entries", MaxEntries)
	}
	seen := map[string]bool{}
	for i := range c.Entries {
		e := &c.Entries[i]
		if strings.TrimSpace(e.ID) == "" || utf8.RuneCountInString(e.ID) > 128 || seen[e.ID] || strings.TrimSpace(e.Title) == "" || utf8.RuneCountInString(e.Title) > 128 || strings.TrimSpace(e.Text) == "" || tokenestimate.EstimateText(e.Text) > MaxEntryTokens || len(e.Aliases) > 32 {
			return c, fmt.Errorf("invalid or duplicate knowledge entry %q", e.ID)
		}
		seen[e.ID] = true
		for _, alias := range e.Aliases {
			if strings.TrimSpace(alias) == "" || utf8.RuneCountInString(alias) > 128 {
				return c, fmt.Errorf("empty alias in %q", e.ID)
			}
		}
		switch e.Visibility.Kind {
		case "public":
			if len(e.Visibility.DefinitionIDs) != 0 {
				return c, fmt.Errorf("public knowledge %q has restricted definitions", e.ID)
			}
		case "definitions":
			if len(e.Visibility.DefinitionIDs) == 0 {
				return c, fmt.Errorf("restricted knowledge %q needs definitions", e.ID)
			}
			for j, id := range e.Visibility.DefinitionIDs {
				id = strings.TrimSpace(id)
				if _, ok := definitions.FindAgent(gameID, id); !ok {
					return c, fmt.Errorf("unknown definition %q in knowledge %q", id, e.ID)
				}
				e.Visibility.DefinitionIDs[j] = id
			}
		default:
			return c, fmt.Errorf("invalid visibility in knowledge %q", e.ID)
		}
	}
	return c, nil
}

func Open(ctx context.Context, data []byte, gameID string, definitions definition.Catalog) (*Service, error) {
	c, err := Parse(data, gameID, definitions)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	return openCatalog(ctx, c, hex.EncodeToString(hash[:]))
}

// Reopen restores the same validated content snapshot during instance rollback.
func (s *Service) Reopen(ctx context.Context) (*Service, error) {
	return openCatalog(ctx, s.catalog, s.hash)
}

func openCatalog(ctx context.Context, c Catalog, hash string) (*Service, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = buildIndex(ctx, db, c); err != nil {
		db.Close()
		return nil, fmt.Errorf("build knowledge index: %w", err)
	}
	return &Service{db: db, gameID: c.GameID, version: c.SourceVersion, hash: hash, catalog: c}, nil
}

func buildIndex(ctx context.Context, db *sql.DB, c Catalog) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE entries(id TEXT PRIMARY KEY, body TEXT NOT NULL, public INTEGER NOT NULL);
CREATE TABLE visibility(entry_id TEXT, definition_id TEXT, PRIMARY KEY(entry_id, definition_id));
CREATE TABLE terms(entry_id TEXT, field TEXT, term TEXT, PRIMARY KEY(entry_id, field, term));
CREATE INDEX term_lookup ON terms(term, entry_id);`); err != nil {
		return err
	}
	for _, e := range c.Entries {
		data, _ := json.Marshal(e)
		if _, err = tx.ExecContext(ctx, "INSERT INTO entries VALUES(?,?,?)", e.ID, string(data), e.Visibility.Kind == "public"); err != nil {
			return err
		}
		for _, id := range e.Visibility.DefinitionIDs {
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO visibility VALUES(?,?)", e.ID, id); err != nil {
				return err
			}
		}
		for field, value := range map[string]string{"title": e.Title, "aliases": strings.Join(e.Aliases, " "), "text": e.Text} {
			for _, term := range textsearch.Terms(value, utf8.RuneCountInString(value), MaxFileBytes) {
				if _, err = tx.ExecContext(ctx, "INSERT INTO terms VALUES(?,?,?)", e.ID, field, term); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

func (s *Service) Close() error { return s.db.Close() }

func (s *Service) Scope(owner session.AgentSessionKey, definitionID, name string) ReadScope {
	return ReadScope{Owner: owner, DefinitionID: strings.TrimSpace(definitionID), DisplayName: strings.TrimSpace(name), ContentHash: s.hash}
}

func (s *Service) EmptyResult() Result {
	return Result{Status: "no_match", SourceVersion: s.version, ContentHash: s.hash, Entries: []Snippet{}}
}

func (s *Service) Search(ctx context.Context, scope ReadScope, query string) (Result, error) {
	result := s.EmptyResult()
	if scope.Owner.GameID != s.gameID || scope.Owner.WorldID == "" || scope.Owner.EntityID == "" || scope.ContentHash != s.hash {
		return result, fmt.Errorf("knowledge scope mismatch")
	}
	if query == "" || utf8.RuneCountInString(query) > MaxQueryChars {
		return result, fmt.Errorf("invalid knowledge query")
	}
	ctx, cancel := context.WithTimeout(ctx, SearchTimeout)
	defer cancel()
	terms := textsearch.Terms(query, MaxQueryChars, MaxQueryTerms)
	excluded := map[string]bool{}
	for _, t := range textsearch.Terms(scope.DisplayName+" "+scope.DefinitionID, MaxQueryChars, MaxQueryTerms) {
		excluded[t] = true
	}
	var active []string
	for _, t := range terms {
		if !excluded[t] {
			active = append(active, t)
		}
	}
	if len(active) == 0 {
		return result, ctx.Err()
	}
	args := []any{scope.DefinitionID}
	for _, term := range active {
		args = append(args, term)
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(active)), ",")
	rows, err := s.db.QueryContext(ctx, `SELECT e.body,
COUNT(DISTINCT CASE WHEN t.field='title' THEN t.term END),
COUNT(DISTINCT CASE WHEN t.field='text' THEN t.term END)
FROM entries e JOIN terms t ON t.entry_id=e.id
WHERE (e.public=1 OR EXISTS(SELECT 1 FROM visibility v WHERE v.entry_id=e.id AND v.definition_id=?))
AND t.term IN (`+marks+`) GROUP BY e.id ORDER BY e.id LIMIT 512`, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	type hit struct {
		snippet Snippet
		score   int
	}
	var hits []hit
	for rows.Next() {
		var raw string
		var titleTerms, bodyTerms int
		if err = rows.Scan(&raw, &titleTerms, &bodyTerms); err != nil {
			return result, err
		}
		var e Entry
		if err = json.Unmarshal([]byte(raw), &e); err != nil {
			return result, err
		}
		aliases := map[string]bool{}
		for _, alias := range e.Aliases {
			alias = strings.ToLower(strings.TrimSpace(alias))
			if !excluded[alias] && aliasMatches(strings.ToLower(query), alias) {
				aliases[alias] = true
			}
		}
		if len(aliases) == 0 && titleTerms == 0 && bodyTerms < 2 {
			continue
		}
		hits = append(hits, hit{Snippet{e.ID, e.Title, e.Text}, len(aliases)*8 + titleTerms*4 + bodyTerms})
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].snippet.ID < hits[j].snippet.ID
	})
	for _, h := range hits {
		result.Entries = append(result.Entries, h.snippet)
	}
	if len(result.Entries) > 0 {
		result.Status = "ok"
	}
	return result, nil
}

func aliasMatches(query, alias string) bool {
	if strings.ContainsFunc(alias, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
		return strings.Contains(query, alias)
	}
	for offset := 0; offset <= len(query)-len(alias); {
		i := strings.Index(query[offset:], alias)
		if i < 0 {
			return false
		}
		i += offset
		before, after := false, false
		if i > 0 {
			r, _ := utf8.DecodeLastRuneInString(query[:i])
			before = unicode.Is(unicode.Latin, r) || unicode.IsDigit(r)
		}
		if i+len(alias) < len(query) {
			r, _ := utf8.DecodeRuneInString(query[i+len(alias):])
			after = unicode.Is(unicode.Latin, r) || unicode.IsDigit(r)
		}
		if !before && !after {
			return true
		}
		offset = i + len(alias)
	}
	return false
}
