package native

import (
	"encoding/json"
	"kvm/app/engine"
	"kvm/core/capture"
	"kvm/core/identity"
	"kvm/core/streamtrace"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// Explicitly opt-in: receives video in memory, sends no input or clipboard.
func TestLiveVideoDiagnostic(t *testing.T) {
	server, target, password := os.Getenv("NANODESK_DIAG_SERVER"), os.Getenv("NANODESK_DIAG_TARGET"), os.Getenv("NANODESK_DIAG_PASSWORD")
	if server == "" || target == "" || password == "" {
		t.Skip("live diagnostic is opt-in")
	}
	defer streamtrace.Close()
	duration := 35 * time.Second
	if raw := os.Getenv("NANODESK_DIAG_SECONDS"); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 1 || seconds > 3600 {
			t.Fatal("invalid diagnostic duration")
		}
		duration = time.Duration(seconds) * time.Second
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	d := testDecoderCreate()
	if d == nil {
		t.Fatal("decoder unavailable")
	}
	defer testDecoderClose(d)
	state, err := identity.LoadPath(filepath.Join(t.TempDir(), "state.bin"))
	if err != nil {
		t.Fatal(err)
	}
	state.Settings.Server = server
	if configPath := os.Getenv("NANODESK_DIAG_CONFIG"); configPath != "" {
		existing, loadErr := identity.LoadPath(configPath)
		if loadErr != nil {
			t.Fatal("load existing CA configuration:", loadErr)
		}
		state.Settings.CAFile = existing.Settings.CAFile
		t.Logf("existing CA configured=%v", state.Settings.CAFile != "")
	}
	state.Settings.Clipboard = false
	state.Settings.RelayOnly = os.Getenv("NANODESK_DIAG_RELAY") == "1"
	state.Settings.RelayTLSOnly = os.Getenv("NANODESK_DIAG_TLS") == "1"
	visible := os.Getenv("NANODESK_DIAG_GUI") == "1"
	var sink *VideoSink
	services := engine.Services{}
	if visible {
		sink = NewVideoSink()
		services.VideoSink = sink
	}
	e, err := engine.NewWithServices(state, services)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.Start()
	start := time.Now()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	report := time.NewTicker(5 * time.Second)
	defer report.Stop()
	var sid string
	var uiTick <-chan time.Time
	if visible {
		if !Init() || !testShowWindow() {
			t.Fatal("native UI unavailable")
		}
		uiTimer := time.NewTicker(8 * time.Millisecond)
		defer uiTimer.Stop()
		uiTick = uiTimer.C
		defer func() {
			if sid != "" {
				Close(sid)
			}
		}()
	}
	var frames, decoded, errors, bytes int
	var last time.Time
	var maxGap time.Duration
	var stallSamples int
	t.Logf("started_utc=%s duration=%v", start.UTC().Format(time.RFC3339), duration)
	command := func(kind string) {
		if sid != "" {
			b, _ := json.Marshal(map[string]string{"type": kind})
			if err := e.Command(sid, b); err != nil {
				t.Log("command", err)
			}
		}
	}
	updateStats := func() {
		if sink != nil {
			stats := sink.Stats(sid)
			frames = int(stats.Accepted)
			decoded = int(stats.Decoded)
			errors = int(stats.Errors)
			bytes = int(stats.Bytes)
			last = stats.LastDecoded
			maxGap = stats.MaxDecodeGap
		}
	}
	for {
		select {
		case <-uiTick:
			for i := 0; i < 16; i++ {
				_, n := Poll()
				if n == 0 {
					break
				}
			}
		case <-timer.C:
			updateStats()
			t.Logf("SUMMARY received=%d decoded=%d errors=%d bytes=%d last_frame_age=%v", frames, decoded, errors, bytes, time.Since(last))
			t.Logf("SUMMARY max_interframe_gap=%v stalled_5s_samples=%d network=%v", maxGap, stallSamples, e.VideoDiagnostics(sid))
			if frames == 0 {
				t.Error("no video received")
			}
			if decoded == 0 || errors > 0 || maxGap > 4*time.Second || (!last.IsZero() && time.Since(last) > 4*time.Second) || stallSamples > 0 {
				t.Error("video decode or continuity check failed; inspect counters")
			}
			return
		case <-report.C:
			updateStats()
			t.Logf("%v received=%d decoded=%d errors=%d bytes=%d", time.Since(start).Round(time.Second), frames, decoded, errors, bytes)
			t.Logf("network=%v", e.VideoDiagnostics(sid))
			if visible && sid != "" {
				paint, present := testRenderCounts(sid)
				t.Logf("render paint=%d present_attempts=%d", paint, present)
				if frames > 0 && paint == 0 {
					t.Fatal("decoded frames have no native paints; rendering measurement is invalid")
				}
			}
			if !last.IsZero() {
				t.Logf("last_frame_age=%v", time.Since(last))
			}
			if last.IsZero() || time.Since(last) > 4*time.Second {
				if !last.IsZero() {
					stallSamples++
				}
				command("request_keyframe")
			}
		case ev := <-e.Events:
			switch ev.Kind {
			case "id":
				if err := e.Connect(target); err != nil {
					t.Fatal(err)
				}
			case "ready":
				sid = ev.Session
				if visible {
					Open(sid, "Stream diagnostic")
				}
				b, _ := json.Marshal(map[string]string{"type": "auth_login", "password": password})
				if err := e.Command(sid, b); err != nil {
					t.Fatal(err)
				}
				t.Log("ready; authentication sent")
			case "control":
				var m struct {
					Type     string            `json:"type"`
					Message  string            `json:"message"`
					Role     int               `json:"role"`
					Monitors []capture.Monitor `json:"monitors"`
					Current  int               `json:"current"`
					Reused   bool              `json:"reused"`
				}
				if json.Unmarshal(ev.Data, &m) == nil && visible {
					if m.Type == "source_frame_state" {
						SourceState(sid, m.Reused)
						stage := "source.fresh"
						if m.Reused {
							stage = "source.reused"
						}
						streamtrace.Emit(streamtrace.Event{Stage: stage})
					}
					if m.Type == "auth_success" {
						Authenticated(sid, m.Role)
					}
					if m.Type == "init" {
						ClearMonitors(sid)
						for i, mon := range m.Monitors {
							Monitor(sid, mon.Index, i+1, mon.Width, mon.Height, mon.IsPrimary, mon.Index == m.Current)
						}
					}
				}
				if json.Unmarshal(ev.Data, &m) == nil && m.Type != "ping" && m.Type != "members_update" && m.Type != "control_state" {
					t.Log("control", m.Type, m.Message)
				}
			case "status", "peer_status", "signal_error", "connect_error", "closed":
				t.Log(ev.Kind)
			}
		case frame := <-e.Frames:
			frames++
			bytes += len(frame.Data)
			if !last.IsZero() {
				maxGap = max(maxGap, time.Since(last))
			}
			last = time.Now()
			streamtrace.Emit(streamtrace.Event{Stage: "viewer.queue_age", Object: frame.TraceObject, Frame: frame.TraceFrame, DurationUS: time.Since(frame.TraceAt).Microseconds(), Value: int64(len(e.Frames))})
			var result, w, h int
			if visible {
				result = FrameTraced(sid, frame.Data, frame.TraceObject, frame.TraceFrame)
			} else {
				result, w, h = testDecoderFeedTraced(d, frame.Data, frame.TraceObject, frame.TraceFrame)
			}
			if result > 0 {
				decoded++
			}
			if result < 0 {
				errors++
				command("request_keyframe")
			}
			if frames <= 3 {
				if visible {
					t.Logf("frame=%d bytes=%d result=%d native_window=true", frames, len(frame.Data), result)
				} else {
					t.Logf("frame=%d bytes=%d result=%d dimensions=%dx%d", frames, len(frame.Data), result, w, h)
				}
			}
		}
	}
}
