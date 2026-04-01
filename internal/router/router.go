package router

import (
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
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

	result, err := pg_query.Parse(query)
	if err != nil {
		return Primary // unparseable → safe default
	}

	for _, stmt := range result.Stmts {
		if isWriteStatement(stmt.Stmt) {
			return Primary
		}
	}
	return Replica
}

func isWriteStatement(node *pg_query.Node) bool {
	switch n := node.Node.(type) {
	case *pg_query.Node_SelectStmt:
		sel := n.SelectStmt

		// SELECT FOR UPDATE/SHARE → Primary
		if len(sel.LockingClause) > 0 {
			return true
		}

		// Check CTEs for writes
		if sel.WithClause != nil {
			for _, cte := range sel.WithClause.GetCtes() {
				cteExpr := cte.GetCommonTableExpr()
				if cteExpr != nil && cteExpr.Ctequery != nil {
					if isWriteStatement(cteExpr.Ctequery) {
						return true
					}
				}
			}
		}

		return false

	case *pg_query.Node_InsertStmt,
		*pg_query.Node_UpdateStmt,
		*pg_query.Node_DeleteStmt,
		*pg_query.Node_CreateStmt,
		*pg_query.Node_DropStmt,
		*pg_query.Node_AlterTableStmt,
		*pg_query.Node_TransactionStmt,
		*pg_query.Node_TruncateStmt,
		*pg_query.Node_GrantStmt,
		*pg_query.Node_CallStmt,
		*pg_query.Node_DoStmt:
		return true

	case *pg_query.Node_ExplainStmt:
		// EXPLAIN on a write is still a write (EXPLAIN ANALYZE INSERT ...)
		if n.ExplainStmt.Query != nil {
			return isWriteStatement(n.ExplainStmt.Query)
		}
		return false

	case *pg_query.Node_VariableSetStmt:
		return true // SET → session modification, route to primary

	case *pg_query.Node_VariableShowStmt:
		return false // SHOW → read-only

	default:
		return true // unknown → Primary (safe)
	}
}

func IsSessionModification(query string) bool {
	result, err := pg_query.Parse(query)
	if err != nil {
		// Fallback to string matching for unparseable queries
		query = strings.TrimSpace(strings.ToUpper(query))
		return strings.HasPrefix(query, "SET") || strings.HasPrefix(query, "RESET")
	}

	for _, stmt := range result.Stmts {
		switch stmt.Stmt.Node.(type) {
		case *pg_query.Node_VariableSetStmt:
			return true
		}
	}
	return false
}

func IsTransactionStart(query string) bool {
	result, err := pg_query.Parse(query)
	if err != nil {
		query = strings.TrimSpace(strings.ToUpper(query))
		return strings.HasPrefix(query, "BEGIN") || strings.HasPrefix(query, "START TRANSACTION")
	}

	for _, stmt := range result.Stmts {
		if ts, ok := stmt.Stmt.Node.(*pg_query.Node_TransactionStmt); ok {
			// BEGIN or START TRANSACTION
			return ts.TransactionStmt.Kind == pg_query.TransactionStmtKind_TRANS_STMT_BEGIN ||
				ts.TransactionStmt.Kind == pg_query.TransactionStmtKind_TRANS_STMT_START
		}
	}
	return false
}

func IsTransactionEnd(query string) bool {
	result, err := pg_query.Parse(query)
	if err != nil {
		query = strings.TrimSpace(strings.ToUpper(query))
		return strings.HasPrefix(query, "COMMIT") || strings.HasPrefix(query, "ROLLBACK") || strings.HasPrefix(query, "ABORT")
	}

	for _, stmt := range result.Stmts {
		if ts, ok := stmt.Stmt.Node.(*pg_query.Node_TransactionStmt); ok {
			return ts.TransactionStmt.Kind == pg_query.TransactionStmtKind_TRANS_STMT_COMMIT ||
				ts.TransactionStmt.Kind == pg_query.TransactionStmtKind_TRANS_STMT_ROLLBACK
		}
	}
	return false
}
