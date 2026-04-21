package main

import "testing"

func TestRewriteQuery(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "now() replaced",
			input: "UPDATE users SET updated_at = now() WHERE user_id = $1",
			want:  "UPDATE users SET updated_at = datetime('now') WHERE user_id = $1",
		},
		{
			name:  "multiple now() calls",
			input: "INSERT INTO t (a, b) VALUES (now(), now())",
			want:  "INSERT INTO t (a, b) VALUES (datetime('now'), datetime('now'))",
		},
		{
			name:  "no now() — unchanged",
			input: "SELECT * FROM users WHERE user_id = $1",
			want:  "SELECT * FROM users WHERE user_id = $1",
		},
		{
			name:  "DEFAULT now() in DDL",
			input: "created_at TEXT NOT NULL DEFAULT now()",
			want:  "created_at TEXT NOT NULL DEFAULT datetime('now')",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "now() at start",
			input: "now() AND now()",
			want:  "datetime('now') AND datetime('now')",
		},
		{
			name:  "preserves $1 parameters",
			input: "SELECT $1, $2, now()",
			want:  "SELECT $1, $2, datetime('now')",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteQuery(tt.input)
			if got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}
