package gateway

import "testing"

// MongoDB commands are not SQL, so the SQL judgement machinery cannot read them:
// ParseVerb on `db.orders.drop()` finds the leading word "db", an unrecognised
// verb resolves to the READ capability, and a command that destroys a collection
// is waved through as a harmless query. MongoDB therefore needs its own dialect —
// verbs, capability mapping, read/write routing and an unscoped-mutation guard —
// so the same gate produces meaningful verdicts for it.
func TestMongoDialect_Verb(t *testing.T) {
	d := DialectFor("mongodb")
	cases := map[string]string{
		`db.orders.find({status:"new"})`:      "FIND",
		`db.orders.findOne({_id:1})`:          "FINDONE",
		`db.orders.insertOne({a:1})`:          "INSERTONE",
		`db.orders.updateMany({}, {$set:{}})`: "UPDATEMANY",
		`db.orders.deleteMany({})`:            "DELETEMANY",
		`db.orders.drop()`:                    "DROP",
		`db.dropDatabase()`:                   "DROPDATABASE",
		`db.createCollection("x")`:            "CREATECOLLECTION",
		`db.orders.aggregate([{$match:{}}])`:  "AGGREGATE",
		`show collections`:                    "SHOW",
		`use analytics`:                       "USE",
	}
	for cmd, want := range cases {
		if got := d.Verb(cmd); got != want {
			t.Errorf("Verb(%q) = %q, want %q", cmd, got, want)
		}
	}
}

// A chained command must be judged by its most dangerous link, never by the
// first one: `db.getCollection("orders").drop()` opens with a lookup helper and
// ends by destroying the collection.
func TestMongoDialect_ChainIsJudgedByItsMostDangerousLink(t *testing.T) {
	d := DialectFor("mongodb")
	for _, cmd := range []string{
		`db.getCollection("orders").drop()`,
		`db.getSiblingDB("prod").orders.deleteMany({})`,
		`db.orders.find({}).limit(10)`, // read + cursor helper stays a read
	} {
		verb := d.Verb(cmd)
		if verb == "" {
			t.Errorf("Verb(%q) returned nothing", cmd)
		}
	}
	if got := d.Capability(d.Verb(`db.getCollection("orders").drop()`)); got != "ddl" {
		t.Errorf("chained drop → capability %q, want ddl", got)
	}
	if got := d.Capability(d.Verb(`db.orders.find({}).limit(10)`)); got != "select" {
		t.Errorf("find().limit() → capability %q, want select", got)
	}
}

func TestMongoDialect_Capability(t *testing.T) {
	d := DialectFor("mongodb")
	cases := map[string]string{
		"FIND": "select", "FINDONE": "select", "AGGREGATE": "select", "COUNTDOCUMENTS": "select",
		"DISTINCT": "select", "SHOW": "select", "USE": "select",
		"INSERTONE": "write", "INSERTMANY": "write", "UPDATEONE": "write", "UPDATEMANY": "write",
		"DELETEONE": "write", "DELETEMANY": "write", "REPLACEONE": "write", "BULKWRITE": "write",
		"FINDONEANDDELETE": "write", "REMOVE": "write",
		"DROP": "ddl", "DROPDATABASE": "ddl", "CREATECOLLECTION": "ddl", "CREATEINDEX": "ddl",
		"DROPINDEX": "ddl", "RENAMECOLLECTION": "ddl",
		"CREATEUSER": "grant", "GRANTROLESTOUSER": "grant", "DROPUSER": "grant",
	}
	for verb, want := range cases {
		if got := d.Capability(verb); got != want {
			t.Errorf("Capability(%q) = %q, want %q", verb, got, want)
		}
	}
}

// An unknown method must not fall into the read bucket — that is exactly the
// failure this dialect exists to prevent.
func TestMongoDialect_UnknownVerbIsNotRead(t *testing.T) {
	d := DialectFor("mongodb")
	if got := d.Capability("SOMETHINGNEW"); got == "select" {
		t.Errorf("unknown Mongo method mapped to %q; an unrecognised operation must not be treated as a read", got)
	}
	if d.IsRead(`db.orders.somethingNew()`) {
		t.Error("an unrecognised command must not be routed as a read")
	}
}

func TestMongoDialect_IsRead(t *testing.T) {
	d := DialectFor("mongodb")
	reads := []string{`db.orders.find({})`, `db.orders.countDocuments({})`, `show dbs`, `use x`}
	writes := []string{`db.orders.insertOne({})`, `db.orders.drop()`, `db.orders.updateMany({}, {})`}
	for _, c := range reads {
		if !d.IsRead(c) {
			t.Errorf("IsRead(%q) = false, want true", c)
		}
	}
	for _, c := range writes {
		if d.IsRead(c) {
			t.Errorf("IsRead(%q) = true, want false", c)
		}
	}
}

// The strict-mode guard for SQL is "a DELETE/UPDATE with no WHERE". Its MongoDB
// equivalent is a multi-document write whose filter is empty: deleteMany({})
// removes every document in the collection.
func TestMongoDialect_UnscopedMutation(t *testing.T) {
	d := DialectFor("mongodb")
	unscoped := []string{
		`db.orders.deleteMany({})`,
		`db.orders.deleteMany( { } )`,
		`db.orders.deleteMany()`,
		`db.orders.updateMany({}, {$set:{a:1}})`,
		`db.orders.remove({})`,
	}
	scoped := []string{
		`db.orders.deleteMany({status:"old"})`,
		`db.orders.updateMany({tenant:42}, {$set:{a:1}})`,
		`db.orders.deleteOne({})`,  // affects one document by definition
		`db.orders.find({})`,       // a read
		`db.orders.insertOne({})`,  // inserts don't match documents
	}
	for _, c := range unscoped {
		if !d.UnscopedMutation(c) {
			t.Errorf("UnscopedMutation(%q) = false; this rewrites every document", c)
		}
	}
	for _, c := range scoped {
		if d.UnscopedMutation(c) {
			t.Errorf("UnscopedMutation(%q) = true, want false", c)
		}
	}
}

// Several commands may be pasted at once, exactly as with SQL, and each has to be
// judged separately — a benign leading find must not carry a trailing drop.
func TestMongoDialect_Split(t *testing.T) {
	d := DialectFor("mongodb")
	got := d.Split("db.orders.find({a:1});\ndb.orders.drop()")
	if len(got) != 2 {
		t.Fatalf("Split returned %d statements: %#v", len(got), got)
	}
	// A separator inside a string or a document must not split.
	if n := len(d.Split(`db.orders.insertOne({note:"a;b"})`)); n != 1 {
		t.Errorf("a semicolon inside a string split the command into %d", n)
	}
	if n := len(d.Split(`db.orders.updateMany({}, {$set:{a:1}})`)); n != 1 {
		t.Errorf("braces split the command into %d", n)
	}
}

// SQL connections must keep the SQL dialect — this is an addition, not a change.
func TestDialectFor_DefaultsToSQL(t *testing.T) {
	for _, engine := range []string{"mysql", "PolarDB", "postgres", "oracle", "", "clickhouse"} {
		if got := DialectFor(engine).Name(); got != "sql" {
			t.Errorf("DialectFor(%q).Name() = %q, want sql", engine, got)
		}
	}
	if got := DialectFor("MongoDB").Name(); got != "mongo" {
		t.Errorf("DialectFor(MongoDB).Name() = %q, want mongo", got)
	}
}

// The whole point of the dialect is that the ENGINE reaches a sane verdict for a
// Mongo command. Judged as SQL, `db.orders.drop()` parses to the verb "db",
// maps to the read capability and sails through; judged as Mongo it is DDL.
func TestEvaluate_MongoCommandIsJudgedByItsOwnDialect(t *testing.T) {
	store := &fakeStore{caps: map[string]string{"ddl|prod": "deny", "select|prod": "allow"}}
	e := NewRiskEngine(store, true)

	deny := e.EvaluateFor([]int64{1}, "mongodb", "prod", `db.orders.drop()`)
	if deny.Action != ActionDeny {
		t.Errorf("dropping a collection on PROD = %+v; want deny (ddl)", deny)
	}
	read := e.EvaluateFor([]int64{1}, "mongodb", "prod", `db.orders.find({a:1})`)
	if read.Action != ActionAllow {
		t.Errorf("a Mongo find = %+v; want allow (select)", read)
	}
	// Strict mode: an unfiltered multi-document delete is the Mongo equivalent of
	// DELETE without WHERE.
	sweep := e.EvaluateFor([]int64{1}, "mongodb", "dev", `db.orders.deleteMany({})`)
	if sweep.Action == ActionAllow {
		t.Errorf("deleteMany({}) removes every document = %+v; want gated", sweep)
	}
	// The same text on a SQL connection keeps the SQL dialect.
	if got := e.EvaluateFor([]int64{1}, "mysql", "prod", "SELECT 1").Action; got != ActionAllow {
		t.Errorf("SQL connections must be unaffected, got %q", got)
	}
}
