package runner

import (
	"strings"
	"testing"
)

func TestValidateTableForCodegen(t *testing.T) {
	cases := []struct {
		name      string
		tbl       sqlTable
		wantOK    bool
		wantInErr string
	}{
		{
			name: "ok: single PK with payload",
			tbl: sqlTable{
				Name:       "user",
				PKSqlNames: []string{"id"},
				Columns: []sqlColumn{
					{Name: "id", IsPrimary: true},
					{Name: "email"},
				},
			},
			wantOK: true,
		},
		{
			name: "fail: no PK",
			tbl: sqlTable{
				Name:    "stateless",
				Columns: []sqlColumn{{Name: "foo"}},
			},
			wantInErr: "no primary key",
		},
		{
			name: "fail: composite PK",
			tbl: sqlTable{
				Name:       "membership",
				PKSqlNames: []string{"user_id", "role_id"},
				Columns: []sqlColumn{
					{Name: "user_id", IsPrimary: true},
					{Name: "role_id", IsPrimary: true},
					{Name: "joined_at"},
				},
			},
			wantInErr: "composite primary key",
		},
		{
			name: "fail: no updatable columns",
			tbl: sqlTable{
				Name:       "tags_index",
				PKSqlNames: []string{"id"},
				Columns: []sqlColumn{
					{Name: "id", IsPrimary: true},
					{Name: "created_at"},
					{Name: "updated_at"},
					{Name: "deleted_at"},
				},
			},
			wantInErr: "no updatable columns",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateTableForCodegen(c.tbl)
			if c.wantOK {
				if err != nil {
					t.Fatalf("expected ok, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantInErr)
			}
			if !strings.Contains(err.Error(), c.wantInErr) {
				t.Errorf("error %q does not contain %q", err.Error(), c.wantInErr)
			}
		})
	}
}
