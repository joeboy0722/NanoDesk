// Package protocol defines the versioned native signaling envelope. Desktop
// credentials and desktop content never travel through this protocol.
package protocol

type ICE struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}
type Signal struct {
	Type      string `json:"type"`
	Version   int    `json:"version,omitempty"`
	ID        string `json:"id,omitempty"`
	Target    string `json:"target,omitempty"`
	Session   string `json:"session,omitempty"`
	Nonce     []byte `json:"nonce,omitempty"`
	Public    []byte `json:"public,omitempty"`
	Signature []byte `json:"signature,omitempty"`
	Caller    bool   `json:"caller,omitempty"`
	IP        string `json:"ip,omitempty"`
	SDP       string `json:"sdp,omitempty"`
	ICE       []ICE  `json:"ice,omitempty"`
	Error     string `json:"error,omitempty"`
}

func Proof(nonce []byte) []byte { return append([]byte("NanoDesk registration v1\x00"), nonce...) }
