package scenario

type Mode string

const (
	ModeHealthy       Mode = "healthy"
	ModeConnectError  Mode = "connect_error"
	ModeDisconnect    Mode = "disconnect"
	ModeSendError     Mode = "send_error"
	ModeServer405     Mode = "server_405"
	ModeUploadError   Mode = "upload_error"
	ModeBlockSend     Mode = "block_send"
	ModeCommitDropAck Mode = "commit_drop_ack"
	ModeEcho          Mode = "echo"
)

type Behavior struct {
	Mode     Mode
	AutoPair bool
	Echo     bool
}

func (Behavior) isStep()            {}
func Configure(mode Mode) Behavior  { return Behavior{Mode: mode} }
func Healthy() Behavior             { return Configure(ModeHealthy) }
func FailConnection() Behavior      { return Configure(ModeConnectError) }
func RejectSends() Behavior         { return Configure(ModeSendError) }
func LoseAcknowledgement() Behavior { return Configure(ModeCommitDropAck) }
func PauseSends() Behavior          { return Configure(ModeBlockSend) }
