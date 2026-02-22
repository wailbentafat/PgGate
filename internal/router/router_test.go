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
		{
			name:          "Basic SELECT",
			query:         "SELECT * FROM users",
			inTransaction: false,
			expected:      Replica,
		},
		{
			name:          "Basic INSERT",
			query:         "INSERT INTO users (name) VALUES ('alice')",
			inTransaction: false,
			expected:      Primary,
		},
		{
			name:          "SELECT in transaction",
			query:         "SELECT * FROM users",
			inTransaction: true,
			expected:      Primary,
		},
		{
			name:          "SELECT FOR UPDATE",
			query:         "SELECT * FROM users FOR UPDATE",
			inTransaction: false,
			expected:      Primary,
		},
		{
			name:          "CTE SELECT",
			query:         "WITH active_users AS (SELECT * FROM users WHERE active = true) SELECT * FROM active_users",
			inTransaction: false,
			expected:      Replica,
		},
		{
			name:          "CTE INSERT",
			query:         "WITH moved_users AS (DELETE FROM users_temp RETURNING *) INSERT INTO users_active SELECT * FROM moved_users",
			inTransaction: false,
			expected:      Primary,
		},
		{
			name:          "SHOW command",
			query:         "SHOW max_connections",
			inTransaction: false,
			expected:      Replica,
		},
		{
			name:          "Mixed case query",
			query:         "select * FROM users",
			inTransaction: false,
			expected:      Replica,
		},
		{
			name:          "Query with leading whitespace",
			query:         "   SELECT 1",
			inTransaction: false,
			expected:      Replica,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Route(tt.query, tt.inTransaction); got != tt.expected {
				t.Errorf("Router.Route() = %v, want %v", got, tt.expected)
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
		{"ABORT", true},
		{"SELECT 1", false},
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
	}

	for _, tt := range tests {
		if got := IsSessionModification(tt.query); got != tt.expected {
			t.Errorf("IsSessionModification(%q) = %v, want %v", tt.query, got, tt.expected)
		}
	}
}
