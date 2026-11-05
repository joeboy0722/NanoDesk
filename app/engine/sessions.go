package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type InboundSession struct {
	Session    string
	Device     string
	Role       string
	Controller bool
}

func (e *Engine) InboundSessions() []InboundSession {
	host := e.host.Load()
	if host == nil {
		return nil
	}
	var sessions []InboundSession
	for _, participant := range host.NativeParticipants() {
		if peer, ok := participant.Transport.(*Peer); ok {
			sessions = append(sessions, InboundSession{
				Session: peer.sid, Device: peer.id,
				Role: participant.Role.String(), Controller: participant.Controller,
			})
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Session < sessions[j].Session })
	return sessions
}

func ValidChat(message string) bool {
	return len(message) <= 2048 && utf8.ValidString(message) && strings.TrimSpace(message) != "" &&
		strings.IndexFunc(message, unicode.IsControl) == -1
}

func (p *Peer) allowChat() bool {
	return p.allowChatAt(&p.lastChat)
}

func (p *Peer) allowChatAt(last *time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if now.Sub(*last) < 250*time.Millisecond {
		return false
	}
	*last = now
	return true
}

func (e *Engine) SendChat(sid, message string) error {
	if !ValidChat(message) {
		return fmt.Errorf("請輸入 2048 位元組以內的單行訊息")
	}
	peer := e.get(sid)
	if peer == nil {
		return fmt.Errorf("連線已結束")
	}
	if !peer.caller {
		peer.mu.Lock()
		client := peer.client
		peer.mu.Unlock()
		if host := e.host.Load(); client == nil || host == nil || !host.Authorized(client) {
			return fmt.Errorf("連線已結束或授權已撤銷")
		}
	}
	if !peer.allowChatAt(&peer.lastChatSent) {
		return fmt.Errorf("訊息傳送過快，請稍後再試")
	}
	data, err := json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{"session_chat", message})
	if err != nil {
		return err
	}
	return peer.send(data)
}
