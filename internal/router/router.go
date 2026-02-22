package router

import (
	"strings"
)

type Destination int

const (
	Primary Destination = iota
	Replica
)

type Router struct {
}

func NewRouter() *Router {
	return &Router{}
}

func (r *Router) Route(query string, inTransaction bool) Destination {
	if inTransaction {
		return Primary
	}

	query = strings.TrimSpace(strings.ToUpper(query))

	if strings.HasPrefix(query, "INSERT") ||
		strings.HasPrefix(query, "UPDATE") ||
		strings.HasPrefix(query, "DELETE") ||
		strings.HasPrefix(query, "CREATE") ||
		strings.HasPrefix(query, "DROP") ||
		strings.HasPrefix(query, "ALTER") ||
		strings.HasPrefix(query, "BEGIN") ||
		strings.HasPrefix(query, "COMMIT") ||
		strings.HasPrefix(query, "ROLLBACK") {
		return Primary
	}

	if strings.HasPrefix(query, "SELECT") && strings.Contains(query, "FOR UPDATE") {
		return Primary
	}

	if strings.HasPrefix(query, "SELECT") {
		return Replica
	}

	return Primary
}

func IsTransactionStart(query string) bool {
	query = strings.TrimSpace(strings.ToUpper(query))
	return strings.HasPrefix(query, "BEGIN") || strings.HasPrefix(query, "START TRANSACTION")
}

func IsTransactionEnd(query string) bool {
	query = strings.TrimSpace(strings.ToUpper(query))
	return strings.HasPrefix(query, "COMMIT") || strings.HasPrefix(query, "ROLLBACK") || strings.HasPrefix(query, "ABORT")
}
