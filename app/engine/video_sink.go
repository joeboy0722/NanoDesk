package engine

// VideoSink owns per-session decoding independently of the UI event pump.
type VideoSink interface {
	OpenSession(string) uint64
	Submit(Event) bool
	ResetSession(string, uint64, uint64)
	CloseSession(string, uint64)
	Close()
	SetFeedback(func(string, uint64, uint64, bool))
}
