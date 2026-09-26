package db

import (
	"strings"
	"testing"
)

func TestAddColumnIfMissingRejectsNonAllowlistedIdentifiers(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	cases := []struct {
		table  string
		column string
		decl   string
	}{
		{table: "users; DROP TABLE queue;--", column: "x", decl: "TEXT"},
		{table: "queue", column: "unknown", decl: "TEXT"},
		{table: "queue", column: "account_id", decl: "INTEGER"},
	}
	for _, tc := range cases {
		err := database.addColumnIfMissing(tc.table, tc.column, tc.decl)
		if err == nil || !strings.Contains(err.Error(), "unsupported migration identifier") {
			t.Fatalf("addColumnIfMissing(%q, %q, %q) error = %v", tc.table, tc.column, tc.decl, err)
		}
	}
}
