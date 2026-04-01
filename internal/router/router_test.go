package router

import (
	"testing"
)

func TestRouter_Route(t *testing.T) {
	r := NewRouter()

	tests := []struct {
		name          string
		query         string
		inTransaction bool
		expected      Destination
	}{
		// Basic reads → Replica
		{
			name:     "Basic SELECT",
			query:    "SELECT * FROM users",
			expected: Replica,
		},
		{
			name:     "SHOW command",
			query:    "SHOW max_connections",
			expected: Replica,
		},
		{
			name:     "Mixed case query",
			query:    "select * FROM users",
			expected: Replica,
		},
		{
			name:     "Query with leading whitespace",
			query:    "   SELECT 1",
			expected: Replica,
		},

		// Basic writes → Primary
		{
			name:     "Basic INSERT",
			query:    "INSERT INTO users (name) VALUES ('alice')",
			expected: Primary,
		},
		{
			name:     "Basic UPDATE",
			query:    "UPDATE users SET name = 'bob'",
			expected: Primary,
		},
		{
			name:     "Basic DELETE",
			query:    "DELETE FROM users WHERE id = 1",
			expected: Primary,
		},

		// DDL → Primary
		{
			name:     "CREATE TABLE",
			query:    "CREATE TABLE t (id int)",
			expected: Primary,
		},
		{
			name:     "DROP TABLE",
			query:    "DROP TABLE users",
			expected: Primary,
		},
		{
			name:     "ALTER TABLE",
			query:    "ALTER TABLE users ADD COLUMN age int",
			expected: Primary,
		},
		{
			name:     "TRUNCATE",
			query:    "TRUNCATE users",
			expected: Primary,
		},

		// Transactions
		{
			name:          "SELECT in transaction",
			query:         "SELECT * FROM users",
			inTransaction: true,
			expected:      Primary,
		},

		// SELECT FOR UPDATE → Primary
		{
			name:     "SELECT FOR UPDATE",
			query:    "SELECT * FROM users FOR UPDATE",
			expected: Primary,
		},
		{
			name:     "SELECT FOR SHARE",
			query:    "SELECT * FROM users FOR SHARE",
			expected: Primary,
		},

		// CTEs
		{
			name:     "CTE read-only",
			query:    "WITH active_users AS (SELECT * FROM users WHERE active = true) SELECT * FROM active_users",
			expected: Replica,
		},
		{
			name:     "CTE with DELETE RETURNING",
			query:    "WITH moved_users AS (DELETE FROM users_temp RETURNING *) INSERT INTO users_active SELECT * FROM moved_users",
			expected: Primary,
		},

		// Edge cases that string matching gets WRONG
		{
			name:     "Comment before SELECT",
			query:    "/* admin query */ SELECT * FROM users",
			expected: Replica,
		},
		{
			name:     "Comment before INSERT",
			query:    "/* log */ INSERT INTO audit (msg) VALUES ('test')",
			expected: Primary,
		},
		{
			name:     "Parenthesized SELECT",
			query:    "(SELECT 1)",
			expected: Replica,
		},
		{
			name:     "EXPLAIN SELECT is read-only",
			query:    "EXPLAIN SELECT * FROM users",
			expected: Replica,
		},
		{
			name:     "EXPLAIN ANALYZE INSERT is write",
			query:    "EXPLAIN ANALYZE INSERT INTO users (name) VALUES ('test')",
			expected: Primary,
		},
		{
			name:     "CTE false positive - SELECT with DELETE in string literal",
			query:    "WITH t AS (SELECT 'DELETE' AS word) SELECT * FROM t",
			expected: Replica,
		},
		{
			name:     "SET command",
			query:    "SET search_path TO myschema",
			expected: Primary,
		},

		// Unparseable → Primary (safe default)
		{
			name:     "Garbage query",
			query:    "THIS IS NOT SQL",
			expected: Primary,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Route(tt.query, tt.inTransaction); got != tt.expected {
				t.Errorf("Router.Route(%q) = %v, want %v", tt.query, got, tt.expected)
			}
		})
	}
}

func TestIsTransactionStart(t *testing.T) {
	tests := []struct {
		query    string
		expected bool
	}{
		{"BEGIN", true},
		{"START TRANSACTION", true},
		{"SELECT 1", false},
		{"  begin  ", true},
		{"/* comment */ BEGIN", true},
	}

	for _, tt := range tests {
		if got := IsTransactionStart(tt.query); got != tt.expected {
			t.Errorf("IsTransactionStart(%q) = %v, want %v", tt.query, got, tt.expected)
		}
	}
}

func TestIsTransactionEnd(t *testing.T) {
	tests := []struct {
		query    string
		expected bool
	}{
		{"COMMIT", true},
		{"ROLLBACK", true},
		{"SELECT 1", false},
		{"/* comment */ COMMIT", true},
	}

	for _, tt := range tests {
		if got := IsTransactionEnd(tt.query); got != tt.expected {
			t.Errorf("IsTransactionEnd(%q) = %v, want %v", tt.query, got, tt.expected)
		}
	}
}

func TestIsSessionModification(t *testing.T) {
	tests := []struct {
		query    string
		expected bool
	}{
		{"SET search_path TO myschema", true},
		{"RESET ALL", true},
		{"SELECT 1", false},
		{"/* comment */ SET timezone = 'UTC'", true},
	}

	for _, tt := range tests {
		if got := IsSessionModification(tt.query); got != tt.expected {
			t.Errorf("IsSessionModification(%q) = %v, want %v", tt.query, got, tt.expected)
		}
	}
}
