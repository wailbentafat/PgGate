package pool

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// pgSimpleQuery sends a simple query via the PostgreSQL wire protocol
// on a raw TCP connection and returns the first row's first column value.
// This is used for health checks without needing a full PG driver.
func pgSimpleQuery(conn net.Conn, query string, timeout time.Duration) (string, error) {
	conn.SetDeadline(time.Now().Add(timeout))
	defer conn.SetDeadline(time.Time{})

	// Build Query message: 'Q' + int32(len) + query + '\0'
	qBytes := []byte(query)
	msgLen := 4 + len(qBytes) + 1
	msg := make([]byte, 1+4+len(qBytes)+1)
	msg[0] = 'Q'
	binary.BigEndian.PutUint32(msg[1:5], uint32(msgLen))
	copy(msg[5:], qBytes)
	msg[len(msg)-1] = 0

	if _, err := conn.Write(msg); err != nil {
		return "", fmt.Errorf("write query: %w", err)
	}

	// Read responses until ReadyForQuery
	headerBuf := make([]byte, 5)
	var result string

	for {
		if _, err := io.ReadFull(conn, headerBuf); err != nil {
			return "", fmt.Errorf("read header: %w", err)
		}
		msgType := headerBuf[0]
		length := int32(binary.BigEndian.Uint32(headerBuf[1:5]))

		body := make([]byte, length-4)
		if len(body) > 0 {
			if _, err := io.ReadFull(conn, body); err != nil {
				return "", fmt.Errorf("read body: %w", err)
			}
		}

		switch msgType {
		case 'D': // DataRow
			// DataRow: int16(field_count) + for each: int32(len) + bytes
			if len(body) < 2 {
				continue
			}
			fieldCount := int(binary.BigEndian.Uint16(body[:2]))
			if fieldCount < 1 {
				continue
			}
			offset := 2
			if offset+4 > len(body) {
				continue
			}
			colLen := int32(binary.BigEndian.Uint32(body[offset : offset+4]))
			offset += 4
			if colLen > 0 && offset+int(colLen) <= len(body) {
				result = string(body[offset : offset+int(colLen)])
			}
		case 'E': // ErrorResponse
			return "", fmt.Errorf("query error: %s", extractPGError(body))
		case 'Z': // ReadyForQuery
			return result, nil
		}
		// Skip RowDescription ('T'), CommandComplete ('C'), etc.
	}
}

func extractPGError(body []byte) string {
	var msg string
	for i := 0; i < len(body); {
		fieldType := body[i]
		i++
		if fieldType == 0 {
			break
		}
		end := i
		for end < len(body) && body[end] != 0 {
			end++
		}
		if fieldType == 'M' {
			msg = string(body[i:end])
		}
		i = end + 1
	}
	return msg
}

// QueryReplicaLagBytes connects to a replica and returns the replication lag
// in bytes by comparing pg_last_wal_receive_lsn() with pg_last_wal_replay_lsn().
// Returns 0 if the replica is caught up, or -1 if lag cannot be determined.
func QueryReplicaLagBytes(conn net.Conn, timeout time.Duration) (int64, error) {
	result, err := pgSimpleQuery(conn,
		"SELECT CASE WHEN pg_last_wal_receive_lsn() IS NULL THEN -1 "+
			"ELSE EXTRACT(EPOCH FROM (now() - pg_last_xact_replay_timestamp()))::bigint END",
		timeout,
	)
	if err != nil {
		return -1, err
	}

	lag, err := strconv.ParseInt(strings.TrimSpace(result), 10, 64)
	if err != nil {
		return -1, fmt.Errorf("parse lag value %q: %w", result, err)
	}
	return lag, nil
}
