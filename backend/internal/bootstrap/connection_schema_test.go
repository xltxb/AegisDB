package bootstrap

import (
	"encoding/json"
	"net/http"
	"testing"
)

// US#11: the terminal tree shows an instance's databases → tables. The schema is
// served by the gateway (simulated, like the executor) rather than hardcoded in
// the UI. GET /connections/:id/schema returns databases each carrying tables.
func TestConnectionSchema_ReturnsDatabasesWithTables(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prodConn := app.connIDByEnv(token, "prod")

	r := app.do(http.MethodGet, "/api/v1/connections/"+itoa(prodConn)+"/schema", token, nil)
	eq(t, r.Code, 0, "connection schema response code")

	var data struct {
		ConnectionID int64 `json:"connectionId"`
		Databases    []struct {
			Name   string `json:"name"`
			Tables []struct {
				Name string `json:"name"`
			} `json:"tables"`
		} `json:"databases"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("decode schema: %v", err)
	}

	eq(t, data.ConnectionID, prodConn, "echoed connection id")
	if len(data.Databases) == 0 {
		t.Fatal("expected at least one database in the schema")
	}
	db := data.Databases[0]
	if db.Name == "" {
		t.Error("database name should not be empty")
	}
	if len(db.Tables) == 0 {
		t.Fatalf("expected database %q to expose tables", db.Name)
	}
	for _, tb := range db.Tables {
		if tb.Name == "" {
			t.Error("table name should not be empty")
		}
	}
}

// Each connection exposes its OWN schema — two different instances must not
// return the identical table set (proves it is per-connection data, not a shared
// hardcoded blob).
func TestConnectionSchema_IsPerConnection(t *testing.T) {
	app := newTestApp(t)
	token := app.login("linwei@vela.io", "vela123")
	prod := app.connIDByEnv(token, "prod")
	dev := app.connIDByEnv(token, "dev")

	first := func(id int64) string {
		r := app.do(http.MethodGet, "/api/v1/connections/"+itoa(id)+"/schema", token, nil)
		eq(t, r.Code, 0, "schema response code")
		var d struct {
			Databases []struct {
				Name string `json:"name"`
			} `json:"databases"`
		}
		if err := json.Unmarshal(r.Data, &d); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(d.Databases) == 0 {
			t.Fatalf("connection %d has no databases", id)
		}
		return d.Databases[0].Name
	}

	if first(prod) == first(dev) {
		t.Error("expected prod and dev connections to expose distinct schemas")
	}
}
