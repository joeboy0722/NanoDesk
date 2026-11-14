// Package protocol defines the versioned native signaling envelope. Desktop
// credentials and desktop content never travel through this protocol.
package protocol

import "kvm/core/relay"

const Version = 2

type ICE struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}
type RelayPath struct {
	TLS         bool                   `json:"tls,omitempty"`
	Address     string                 `json:"address"`
	Credentials relay.RouteCredentials `json:"credentials"`
}

type Signal struct {
	RelayGeneration uint64            `json:"relay_generation,omitempty"`
	RelayTLS        bool              `json:"relay_tls,omitempty"`
	RelayEnabled    bool              `json:"relay_enabled,omitempty"`
	RelayCandidates []relay.Candidate `json:"relay_candidates,omitempty"`
	RelayReachable  []string          `json:"relay_reachable,omitempty"`
	RelayPath       *RelayPath        `json:"relay_path,omitempty"`
	Type            string            `json:"type"`
	Version         int               `json:"version,omitempty"`
	ID              string            `json:"id,omitempty"`
	Target          string            `json:"target,omitempty"`
	Session         string            `json:"session,omitempty"`
	Nonce           []byte            `json:"nonce,omitempty"`
	Public          []byte            `json:"public,omitempty"`
	Signature       []byte            `json:"signature,omitempty"`
	Caller          bool              `json:"caller,omitempty"`
	IP              string            `json:"ip,omitempty"`
	SDP             string            `json:"sdp,omitempty"`
	ICE             []ICE             `json:"ice,omitempty"`
	Error           string            `json:"error,omitempty"`
}

func Proof(nonce []byte) []byte { return append([]byte("NanoDesk registration v2\x00"), nonce...) }
