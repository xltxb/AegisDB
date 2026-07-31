package gateway

import (
	"regexp"
	"strings"

	"velagateway/pkg/sqlutil"
)

// A Dialect teaches the risk engine how to read one family of command language.
//
// Every layer of the gate — capability matrix, high-risk dictionary, strict-mode
// full-scope guard, read/write routing — is expressed in terms of a VERB and a
// CAPABILITY. Those concepts are universal; extracting them is not. SQL yields
// them from a leading keyword, MongoDB from a method chain, and applying the SQL
// rules to a Mongo command produces nonsense with a dangerous bias: `ParseVerb`
// finds the leading word "db", an unrecognised verb maps to the READ capability,
// and `db.orders.drop()` is judged a harmless query.
//
// So the extraction is pluggable and the policy is not: whatever the dialect,
// the verdict still comes from the same three layers.
type Dialect interface {
	// Name identifies the dialect ("sql", "mongo").
	Name() string
	// Split separates a pasted batch into individually-judged commands.
	Split(cmd string) []string
	// Verb returns the canonical operation in upper case, or "" when none can be
	// read. A command built from several operations resolves to its most
	// dangerous one.
	Verb(cmd string) string
	// Capability maps a verb onto a capability-matrix dimension
	// (select|write|ddl|grant).
	Capability(verb string) string
	// IsRead reports a non-mutating command, for execution routing and audit
	// classification.
	IsRead(cmd string) bool
	// UnscopedMutation reports a write that matches every document/row — the
	// thing strict mode exists to catch.
	UnscopedMutation(cmd string) bool
}

// DialectFor picks the dialect for an engine label. Anything that is not
// recognised as MongoDB keeps the SQL dialect, so this is purely additive.
func DialectFor(engine string) Dialect {
	if engineFamily(engine) == familyMongo {
		return mongoDialect{}
	}
	return sqlDialect{}
}

// ---------------------------------------------------------------- SQL

// sqlDialect is the original behaviour, unchanged, expressed through the
// interface so SQL and MongoDB are judged by the same engine code.
type sqlDialect struct{}

func (sqlDialect) Name() string                    { return "sql" }
func (sqlDialect) Split(cmd string) []string       { return sqlutil.SplitStatements(cmd) }
func (sqlDialect) Verb(cmd string) string          { return ParseVerb(cmd) }
func (sqlDialect) Capability(verb string) string   { return MapVerbToCapability(verb) }
func (sqlDialect) IsRead(cmd string) bool          { return IsRead(cmd) }
func (sqlDialect) UnscopedMutation(c string) bool  { return NoWhere(c) }

// ---------------------------------------------------------------- MongoDB

type mongoDialect struct{}

func (mongoDialect) Name() string { return "mongo" }

// mongoMethodRe finds every `.method(` in a command chain, plus the bare shell
// verbs (`use`, `show`).
var (
	mongoMethodRe = regexp.MustCompile(`\.\s*([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	mongoShellRe  = regexp.MustCompile(`^\s*(use|show)\b`)
)

// mongoCapability maps a method to a capability dimension. Anything absent is
// deliberately NOT a read — see Capability.
var mongoCapability = map[string]string{
	// reads
	"FIND": "select", "FINDONE": "select", "AGGREGATE": "select", "COUNT": "select",
	"COUNTDOCUMENTS": "select", "ESTIMATEDDOCUMENTCOUNT": "select", "DISTINCT": "select",
	"EXPLAIN": "select", "STATS": "select", "GETINDEXES": "select", "LISTINDEXES": "select",
	"LISTCOLLECTIONS": "select", "GETCOLLECTIONNAMES": "select", "SHOW": "select", "USE": "select",
	// writes
	"INSERT": "write", "INSERTONE": "write", "INSERTMANY": "write",
	"UPDATE": "write", "UPDATEONE": "write", "UPDATEMANY": "write", "REPLACEONE": "write",
	"DELETE": "write", "DELETEONE": "write", "DELETEMANY": "write", "REMOVE": "write",
	"SAVE": "write", "BULKWRITE": "write", "FINDANDMODIFY": "write",
	"FINDONEANDUPDATE": "write", "FINDONEANDDELETE": "write", "FINDONEANDREPLACE": "write",
	// structure
	"DROP": "ddl", "DROPDATABASE": "ddl", "CREATECOLLECTION": "ddl", "CREATEVIEW": "ddl",
	"CREATEINDEX": "ddl", "CREATEINDEXES": "ddl", "DROPINDEX": "ddl", "DROPINDEXES": "ddl",
	"RENAMECOLLECTION": "ddl", "CONVERTTOCAPPED": "ddl", "REINDEX": "ddl", "COMPACT": "ddl",
	"SHARDCOLLECTION": "ddl", "DROPINDEXNAME": "ddl",
	// privileges
	"CREATEUSER": "grant", "DROPUSER": "grant", "UPDATEUSER": "grant",
	"GRANTROLESTOUSER": "grant", "REVOKEROLESFROMUSER": "grant",
	"CREATEROLE": "grant", "DROPROLE": "grant", "GRANTPRIVILEGESTOROLE": "grant",
	// navigation helpers — they perform nothing on their own, so they must not
	// raise the verdict of the chain they appear in
	"GETCOLLECTION": "select", "GETSIBLINGDB": "select", "GETDB": "select",
	"LIMIT": "select", "SKIP": "select", "SORT": "select", "PRETTY": "select",
	"TOARRAY": "select", "HINT": "select", "PROJECT": "select",
}

// mongoRank orders capabilities by how much gating they demand, so a chain is
// judged by its most dangerous link rather than by whichever method came first:
// `db.getCollection("orders").drop()` opens with a lookup helper.
var mongoRank = map[string]int{"select": 0, "write": 1, "grant": 2, "ddl": 3}

func (m mongoDialect) Verb(cmd string) string {
	if sh := mongoShellRe.FindStringSubmatch(cmd); sh != nil {
		return strings.ToUpper(sh[1])
	}
	methods := mongoMethodRe.FindAllStringSubmatch(cmd, -1)
	if len(methods) == 0 {
		return ""
	}
	best, bestRank := "", -1
	for _, mm := range methods {
		v := strings.ToUpper(mm[1])
		r, known := mongoRank[mongoCapability[v]]
		if !known {
			// An unrecognised method outranks everything: it cannot be shown to be
			// safe, and this dialect exists precisely so unknown operations are not
			// assumed harmless.
			return v
		}
		if r > bestRank {
			best, bestRank = v, r
		}
	}
	return best
}

func (mongoDialect) Capability(verb string) string {
	if c, ok := mongoCapability[strings.ToUpper(strings.TrimSpace(verb))]; ok {
		return c
	}
	// Unknown method: gate it as a write at least. Mapping it to the read
	// dimension is the exact failure this dialect prevents.
	return "write"
}

func (m mongoDialect) IsRead(cmd string) bool {
	v := m.Verb(cmd)
	if v == "" {
		return false
	}
	return m.Capability(v) == "select"
}

// mongoMultiWriteRe matches the multi-document write methods together with their
// first argument, so the filter can be inspected.
var mongoMultiWriteRe = regexp.MustCompile(`(?i)\.\s*(deleteMany|updateMany|remove|delete)\s*\(([^,)]*)`)

func (mongoDialect) UnscopedMutation(cmd string) bool {
	mm := mongoMultiWriteRe.FindStringSubmatch(cmd)
	if mm == nil {
		return false
	}
	// An absent or empty filter matches every document — the Mongo equivalent of
	// a DELETE with no WHERE clause.
	filter := strings.TrimSpace(mm[2])
	filter = strings.TrimSpace(strings.Trim(filter, "{}"))
	return filter == ""
}

// mongoDialect.Split separates pasted commands on top-level `;` and newlines,
// ignoring separators inside strings, documents and arrays.
func (mongoDialect) Split(cmd string) []string {
	var out []string
	var b strings.Builder
	depth := 0
	var quote byte
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			out = append(out, s)
		}
		b.Reset()
	}
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if quote != 0 {
			b.WriteByte(c)
			if c == '\\' && i+1 < len(cmd) { // escaped char inside a string
				i++
				b.WriteByte(cmd[i])
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
			b.WriteByte(c)
		case '{', '[', '(':
			depth++
			b.WriteByte(c)
		case '}', ']', ')':
			if depth > 0 {
				depth--
			}
			b.WriteByte(c)
		case ';', '\n':
			if depth == 0 {
				flush()
			} else {
				b.WriteByte(' ')
			}
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return out
}
