package dialect

import "testing"

func TestNewAliases(t *testing.T) {
	cases := []struct {
		input string
		want  string // expected Name()
	}{
		{"", "sqlite"},
		{"sqlite", "sqlite"},
		{"SQLite", "sqlite"},
		{"sqlite3", "sqlite"},
		{"mysql", "mysql"},
		{"MariaDB", "mysql"},
		{"postgres", "postgres"},
		{"postgresql", "postgres"},
		{"pg", "postgres"},
		{"  Postgres  ", "postgres"},
	}
	for _, c := range cases {
		d, err := New(c.input)
		if err != nil {
			t.Fatalf("New(%q): %v", c.input, err)
		}
		if got := d.Name(); got != c.want {
			t.Errorf("New(%q).Name() = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestNewUnknown(t *testing.T) {
	if _, err := New("oracle"); err == nil {
		t.Error("expected error for unknown dialect, got nil")
	}
}

func TestQuote(t *testing.T) {
	cases := []struct {
		dialect Dialect
		ident   string
		want    string
	}{
		{SQLite{}, "id", "`id`"},
		{SQLite{}, "group", "`group`"},
		{MySQL{}, "user", "`user`"},
		{Postgres{}, "id", `"id"`},
		{Postgres{}, "group", `"group"`},
	}
	for _, c := range cases {
		if got := c.dialect.Quote(c.ident); got != c.want {
			t.Errorf("%s.Quote(%q) = %q, want %q", c.dialect.Name(), c.ident, got, c.want)
		}
	}
}

func TestPlaceholder(t *testing.T) {
	cases := []struct {
		dialect Dialect
		pos     int
		want    string
	}{
		{SQLite{}, 1, "?"},
		{SQLite{}, 5, "?"},
		{MySQL{}, 3, "?"},
		{Postgres{}, 1, "$1"},
		{Postgres{}, 4, "$4"},
		{Postgres{}, 0, "$1"}, // clamped
	}
	for _, c := range cases {
		if got := c.dialect.Placeholder(c.pos); got != c.want {
			t.Errorf("%s.Placeholder(%d) = %q, want %q", c.dialect.Name(), c.pos, got, c.want)
		}
	}
}

func TestPlaceholderList(t *testing.T) {
	cases := []struct {
		dialect  Dialect
		start, n int
		want     string
	}{
		{SQLite{}, 1, 0, ""},
		{SQLite{}, 1, 1, "?"},
		{SQLite{}, 1, 3, "?,?,?"},
		{MySQL{}, 1, 4, "?,?,?,?"},
		{Postgres{}, 1, 3, "$1,$2,$3"},
		{Postgres{}, 4, 3, "$4,$5,$6"},
		{Postgres{}, 1, 0, ""},
	}
	for _, c := range cases {
		got := c.dialect.PlaceholderList(c.start, c.n)
		if got != c.want {
			t.Errorf("%s.PlaceholderList(%d,%d) = %q, want %q", c.dialect.Name(), c.start, c.n, got, c.want)
		}
	}
}

func TestMapTypeCommon(t *testing.T) {
	type want struct {
		Name      string
		NeedsTime bool
		NeedsSQL  bool
	}
	cases := []struct {
		dialect  Dialect
		sqlType  string
		nullable bool
		want     want
	}{
		{SQLite{}, "TEXT", false, want{Name: "string"}},
		{MySQL{}, "VARCHAR", false, want{Name: "string"}},
		{Postgres{}, "TEXT", false, want{Name: "string"}},

		{SQLite{}, "INTEGER", false, want{Name: "int64"}},
		{MySQL{}, "BIGINT", false, want{Name: "int64"}},
		{Postgres{}, "BIGSERIAL", false, want{Name: "int64"}},

		// sqlite/pg spell it BOOLEAN, mysql BOOL
		{SQLite{}, "BOOLEAN", false, want{Name: "bool"}},
		{MySQL{}, "BOOL", false, want{Name: "bool"}},
		{Postgres{}, "BOOLEAN", false, want{Name: "bool"}},

		{SQLite{}, "DATETIME", false, want{Name: "time.Time", NeedsTime: true}},
		{MySQL{}, "TIMESTAMP", false, want{Name: "time.Time", NeedsTime: true}},
		{Postgres{}, "TIMESTAMPTZ", false, want{Name: "time.Time", NeedsTime: true}},

		{SQLite{}, "DATETIME", true, want{Name: "sql.NullTime", NeedsSQL: true}},
		{Postgres{}, "TIMESTAMPTZ", true, want{Name: "sql.NullTime", NeedsSQL: true}},

		{SQLite{}, "BLOB", false, want{Name: "[]byte"}},
		{MySQL{}, "MEDIUMBLOB", false, want{Name: "[]byte"}},
		{Postgres{}, "BYTEA", false, want{Name: "[]byte"}},

		{Postgres{}, "JSONB", false, want{Name: "[]byte"}},
		{Postgres{}, "UUID", false, want{Name: "string"}},

		{SQLite{}, "WEIRDTYPE", false, want{Name: "string"}},
	}
	for _, c := range cases {
		got := c.dialect.MapType(c.sqlType, c.nullable)
		if got.Name != c.want.Name || got.NeedsTime != c.want.NeedsTime || got.NeedsSQL != c.want.NeedsSQL {
			t.Errorf("%s.MapType(%q, nullable=%v) = %+v, want %+v",
				c.dialect.Name(), c.sqlType, c.nullable, got, c.want)
		}
	}
}

func TestDriverName(t *testing.T) {
	cases := map[string]string{
		"sqlite":   "sqlite3",
		"mysql":    "mysql",
		"postgres": "pgx",
	}
	for name, want := range cases {
		d := MustNew(name)
		if got := d.DriverName(); got != want {
			t.Errorf("%s DriverName = %q, want %q", name, got, want)
		}
	}
}
