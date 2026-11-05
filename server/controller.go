package main

import (
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

func samePath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

type logBuffer struct {
	mu   sync.Mutex
	text string
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.text += string(p)
	if len(b.text) > 60000 {
		b.text = b.text[len(b.text)-50000:]
		if i := strings.IndexByte(b.text, '\n'); i >= 0 {
			b.text = b.text[i+1:]
		}
	}
	return len(p), nil
}
func (b *logBuffer) Text() string { b.mu.Lock(); defer b.mu.Unlock(); return b.text }

type serverSnapshot struct {
	Phase, Error             string
	Online, Sessions, Relays int
}
type serverController struct {
	mu            sync.Mutex
	phase         string
	lastError     string
	active        *runningServer
	stopRequested bool
}

func (c *serverController) Snapshot() serverSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	phase := c.phase
	if phase == "" {
		phase = "stopped"
	}
	s := serverSnapshot{Phase: phase, Error: c.lastError}
	if c.active != nil {
		h := c.active.hub
		h.mu.Lock()
		s.Online = len(h.online)
		s.Sessions = len(h.links)
		h.mu.Unlock()
		s.Relays = len(c.active.generator.slots)
	}
	return s
}
func (c *serverController) Start(settings serverSettings) error {
	c.mu.Lock()
	if c.phase != "" && c.phase != "stopped" {
		c.mu.Unlock()
		return fmt.Errorf("服務正在啟動、運作或停止中")
	}
	c.phase = "starting"
	c.lastError = ""
	c.stopRequested = false
	c.mu.Unlock()
	go func() {
		service, err := startServer(settings)
		c.mu.Lock()
		if err != nil {
			c.phase = "stopped"
			c.lastError = err.Error()
			c.mu.Unlock()
			log.Printf("啟動失敗：%v", err)
			return
		}
		c.active = service
		stop := c.stopRequested
		c.phase = "running"
		c.mu.Unlock()
		if stop {
			c.Stop()
			return
		}
		if err := <-service.done; err != nil {
			log.Printf("HTTPS 服務中止：%v", err)
			c.mu.Lock()
			if c.active == service {
				c.lastError = err.Error()
			}
			c.mu.Unlock()
			c.Stop()
		}
	}()
	return nil
}
func (c *serverController) Stop() {
	c.mu.Lock()
	if c.phase == "starting" {
		c.stopRequested = true
		c.mu.Unlock()
		return
	}
	if c.phase != "running" {
		c.mu.Unlock()
		return
	}
	service := c.active
	c.phase = "stopping"
	c.mu.Unlock()
	go func() {
		service.Close()
		c.mu.Lock()
		if c.active == service {
			c.active = nil
			c.phase = "stopped"
		}
		c.mu.Unlock()
	}()
}
func (c *serverController) Backup(source, destination string) error {
	c.mu.Lock()
	phase := c.phase
	active := c.active
	c.mu.Unlock()
	if phase == "starting" || phase == "stopping" {
		return fmt.Errorf("請等待服務啟動／停止完成後再備份")
	}
	if active != nil {
		return active.Backup(destination)
	}
	return backupStoppedDatabase(source, destination)
}
