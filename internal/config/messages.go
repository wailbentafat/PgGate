package config

const (
	// Backend messages
	Authentification = 'R'
	ReadyForQuery    = 'Z'
	ErrorResponse    = 'E'
	NoticeResponse   = 'N'
	Notification     = 'A'
	ParameterStatus  = 'S'
	RowDescription   = 'T'
	DataRow          = 'D'
	CommandComplete  = 'C'
	EmptyQuery       = 'I'
	BackendKeyData   = 'K'
	CopyData         = 'd'
	CopyDone         = 'c'
	CopyInResponse   = 'G'
	CopyOutResponse  = 'H'
	CopyBothResponse = 'W'

	// Frontend messages
	ParseMessage     = 'P'
	BindMessage      = 'B'
	ExecuteMessage   = 'E'
	DescribeMessage  = 'D'
	SyncMessage      = 'S'
	FlushMessage     = 'H'
	CloseMessage     = 'C'
	QueryMessage     = 'Q'
	TerminateMessage = 'X'
	PasswordMessage  = 'p'
	CopyFail         = 'f'

	// Max message size: 64MB. Prevents OOM from malicious length headers.
	MaxMessageSize = 64 * 1024 * 1024
)
