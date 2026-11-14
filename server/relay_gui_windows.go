package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"unsafe"

	"kvm/core/relay"
)

const (
	relayDisableID         = 2120
	relayListID            = 2100
	relayAddGroupID        = 2101
	relayAddEndpointID     = 2102
	relayRemoveID          = 2103
	relayEnabledID         = 2104
	relayTotalID           = 2110
	relaySessionID         = 2111
	relayNameID            = 2112
	relaySourcesID         = 2113
	relayCapacityID        = 2114
	relayDefaultID         = 2115
	relayBindID            = 2116
	relayPublicID          = 2117
	relayPortsID           = 2118
	relayEndpointEnabledID = 2119
)

type relayEditor struct {
	stats                                      uintptr
	lastStats                                  string
	config                                     relay.Config
	group, endpoint                            int
	list, enabled, defaultCheck, endpointCheck uintptr
	controls                                   []uintptr
}

func (g *serverGUI) buildRelayEditor() {
	r := &g.relayEditor
	r.group, r.endpoint = -1, -1
	r.enabled = g.settingControl("BUTTON", "啟用共用埠中繼池", 3|0x10000, relayEnabledID, 3, 194, 104, 240, 30)
	r.list = g.settingControl("LISTBOX", "", 1|0x10000|0x00200000, relayListID, 3, 194, 146, 680, 100)
	r.controls = append(r.controls, r.enabled, r.list)
	for i, button := range []struct {
		id   int
		text string
	}{{relayAddGroupID, "新增端點組"}, {relayAddEndpointID, "新增組內端點"}, {relayRemoveID, "移除端點"}, {relayDisableID, "立即停用"}} {
		r.controls = append(r.controls, g.settingControl("BUTTON", button.text, 0x10000, button.id, 3, 194+i*170, 254, 160, 30))
	}
	for i, field := range []struct {
		id    int
		label string
	}{
		{relayTotalID, "總容量 Mbit/s"}, {relaySessionID, "每端容量預留 Mbit/s"}, {relayNameID, "端點組名稱"}, {relaySourcesID, "來源 CIDR（逗號分隔）"}, {relayCapacityID, "端點組容量 Mbit/s"},
		{relayBindID, "綁定本機 IPv4／IPv6"}, {relayPublicID, "公布 IP／主機名稱"}, {relayPortsID, "UDP 埠（清單或範圍）"},
	} {
		g.row(3, field.id, 300+i*48, field.label, false, false)
		r.controls = append(r.controls, g.edits[field.id])
	}
	r.defaultCheck = g.settingControl("BUTTON", "此組為預設端點組", 3|0x10000, relayDefaultID, 3, 194, 694, 300, 30)
	r.endpointCheck = g.settingControl("BUTTON", "啟用此端點", 3|0x10000, relayEndpointEnabledID, 3, 510, 694, 300, 30)
	r.controls = append(r.controls, r.defaultCheck, r.endpointCheck)
	g.settingControl("STATIC", "端點即時負載", 0, 0, 3, 194, 742, 680, 26)
	r.stats = g.settingControl("EDIT", "", 0x800|4|0x40|0x00200000, 0, 3, 194, 778, 680, 170)

}
func relayChecked(h uintptr) bool { return win("SendMessageW", h, 0xf0, 0, 0) == 1 }
func relayCheck(h uintptr, value bool) {
	var n uintptr
	if value {
		n = 1
	}
	win("SendMessageW", h, 0xf1, n, 0)
}
func relayID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (g *serverGUI) fillRelayEditor(settings serverSettings) {
	r := &g.relayEditor
	r.config = relay.Config{TotalMbps: 100, SessionMbps: 5}
	if settings.RelayPool != nil {
		data, _ := json.Marshal(settings.RelayPool)
		_ = json.Unmarshal(data, &r.config)
	}
	r.group, r.endpoint = -1, -1
	relayCheck(r.enabled, settings.RelayPool != nil)
	setText(g.edits[relayTotalID], strconv.FormatInt(r.config.TotalMbps, 10))
	setText(g.edits[relaySessionID], strconv.FormatInt(r.config.SessionMbps, 10))
	g.relayList()
}
func (g *serverGUI) relayList() {
	r := &g.relayEditor
	win("SendMessageW", r.list, 0x184, 0, 0)
	selected, index := -1, 0
	for gi, group := range r.config.Groups {
		for ei, endpoint := range group.Endpoints {
			name := wide(fmt.Sprintf("%s — %s → %s", group.Name, endpoint.BindIP, endpoint.PublicHost))
			win("SendMessageW", r.list, 0x180, 0, uintptr(unsafe.Pointer(name)))
			runtime.KeepAlive(name)
			if gi == r.group && ei == r.endpoint {
				selected = index
			}
			index++
		}
	}
	if selected >= 0 {
		win("SendMessageW", r.list, 0x186, uintptr(selected), 0)
	}
	g.loadRelayRow()
}
func (g *serverGUI) loadRelayRow() {
	r := &g.relayEditor
	g.loading = true
	defer func() { g.loading = false }()
	for _, id := range []int{relayNameID, relaySourcesID, relayCapacityID, relayBindID, relayPublicID, relayPortsID} {
		setText(g.edits[id], "")
	}
	relayCheck(r.defaultCheck, false)
	relayCheck(r.endpointCheck, false)
	if r.group < 0 {
		return
	}
	group := r.config.Groups[r.group]
	endpoint := group.Endpoints[r.endpoint]
	setText(g.edits[relayNameID], group.Name)
	setText(g.edits[relaySourcesID], strings.Join(group.Sources, ", "))
	setText(g.edits[relayCapacityID], strconv.FormatInt(group.CapacityMbps, 10))
	setText(g.edits[relayBindID], endpoint.BindIP)
	setText(g.edits[relayPublicID], endpoint.PublicHost)
	var ports []string
	for _, p := range endpoint.Ports {
		ports = append(ports, strconv.Itoa(int(p)))
	}
	setText(g.edits[relayPortsID], strings.Join(ports, ","))
	relayCheck(r.defaultCheck, group.Default)
	relayCheck(r.endpointCheck, endpoint.Enabled)
}
func parseRelayPorts(raw string) ([]uint16, error) {
	var ports []uint16
	for _, item := range strings.Split(raw, ",") {
		part := strings.Split(strings.TrimSpace(item), "-")
		if len(part) > 2 {
			return nil, fmt.Errorf("UDP 埠請填清單或範圍，例如 3478,3480-3482")
		}
		start, err := strconv.Atoi(strings.TrimSpace(part[0]))
		if err != nil || start < 1 || start > 65535 {
			return nil, fmt.Errorf("UDP 埠需介於 1 至 65535")
		}
		end := start
		if len(part) == 2 {
			end, err = strconv.Atoi(strings.TrimSpace(part[1]))
		}
		if err != nil || end < start || end > 65535 || len(ports)+end-start+1 > 128 {
			return nil, fmt.Errorf("端點最多可設定 128 個 UDP 埠")
		}
		for p := start; p <= end; p++ {
			ports = append(ports, uint16(p))
		}
	}
	return ports, nil
}
func (g *serverGUI) storeRelayRow() error {
	r := &g.relayEditor
	readNumber := func(id int) (int64, error) {
		value, err := strconv.ParseInt(strings.TrimSpace(getText(g.edits[id])), 10, 64)
		if err != nil || value <= 0 {
			return 0, fmt.Errorf("中繼容量請填正整數")
		}
		return value, nil
	}
	total, err := readNumber(relayTotalID)
	if err != nil {
		return err
	}
	session, err := readNumber(relaySessionID)
	if err != nil {
		return err
	}
	r.config.TotalMbps, r.config.SessionMbps = total, session
	if r.group < 0 {
		return nil
	}
	capacity, err := readNumber(relayCapacityID)
	if err != nil {
		return err
	}
	ports, err := parseRelayPorts(getText(g.edits[relayPortsID]))
	if err != nil {
		return err
	}
	group := &r.config.Groups[r.group]
	group.Name = strings.TrimSpace(getText(g.edits[relayNameID]))
	group.CapacityMbps = capacity
	group.Sources = nil
	for _, source := range strings.Split(getText(g.edits[relaySourcesID]), ",") {
		if source = strings.TrimSpace(source); source != "" {
			group.Sources = append(group.Sources, source)
		}
	}
	group.Default = relayChecked(r.defaultCheck)
	if group.Default {
		for i := range r.config.Groups {
			if i != r.group {
				r.config.Groups[i].Default = false
			}
		}
	}
	endpoint := &group.Endpoints[r.endpoint]
	endpoint.BindIP = strings.TrimSpace(getText(g.edits[relayBindID]))
	endpoint.PublicHost = strings.TrimSpace(getText(g.edits[relayPublicID]))
	endpoint.Ports, endpoint.Enabled = ports, relayChecked(r.endpointCheck)
	return nil
}
func (g *serverGUI) relayCommand(id, notification int) {
	r := &g.relayEditor
	if id == relayListID && notification != 1 {
		return
	}
	if err := g.storeRelayRow(); err != nil {
		showMessage(g.hwnd, err.Error(), 0x10)
		g.relayList()
		return
	}
	switch id {
	case relayAddGroupID:
		r.config.Groups = append(r.config.Groups, relay.GroupConfig{ID: relayID(), Name: "新端點組", CapacityMbps: 100, Default: len(r.config.Groups) == 0})
		r.group, r.endpoint = len(r.config.Groups)-1, -1
		fallthrough
	case relayAddEndpointID:
		if r.group < 0 {
			return
		}
		group := &r.config.Groups[r.group]
		group.Endpoints = append(group.Endpoints, relay.EndpointConfig{ID: relayID(), BindIP: "127.0.0.1", PublicHost: "127.0.0.1", Ports: []uint16{3478}, Enabled: true})
		r.endpoint = len(group.Endpoints) - 1
	case relayRemoveID:
		if r.group < 0 {
			return
		}
		group := &r.config.Groups[r.group]
		group.Endpoints = append(group.Endpoints[:r.endpoint], group.Endpoints[r.endpoint+1:]...)
		if len(group.Endpoints) == 0 {
			r.config.Groups = append(r.config.Groups[:r.group], r.config.Groups[r.group+1:]...)
		}
		r.group, r.endpoint = -1, -1
	case relayListID:
		selected := int(win("SendMessageW", r.list, 0x188, 0, 0))
		index := 0
		for gi, group := range r.config.Groups {
			for ei := range group.Endpoints {
				if index == selected {
					r.group, r.endpoint = gi, ei
				}
				index++
			}
		}
	}
	if id != relayListID {
		g.dirty = true
	}
	g.relayList()
}

func (g *serverGUI) refreshRelayStats(snapshot serverSnapshot) {
	var lines []string
	for _, socket := range snapshot.RelaySockets {
		state := "接受新連線"
		if socket.Draining {
			state = "排空中"
		}
		lines = append(lines, fmt.Sprintf("%s  %s\r\n  %s  預留 %d  流量 %.2f Mbit/s  %.0f 封包/s\r\n  佇列 %d/%d  丟棄 %d  改派路徑 %d",
			socket.EndpointID, socket.Binding, state, socket.Reservations, socket.BytesPerSecond*8/1e6,
			socket.PacketsPerSecond, socket.QueuePackets, socket.QueueCapacity, socket.DroppedPackets, socket.ReassignedRoutes))
	}
	if len(lines) == 0 {
		lines = append(lines, "目前沒有運作中的共用埠中繼端點。")
	}
	text := strings.Join(lines, "\r\n\r\n")
	if text != g.relayEditor.lastStats {
		setText(g.relayEditor.stats, text)
		g.relayEditor.lastStats = text
	}
}

func (g *serverGUI) disableRelayEndpoint() {
	r := &g.relayEditor
	if g.busy || r.group < 0 {
		return
	}
	if g.dirty {
		showMessage(g.hwnd, "請先儲存端點設定，再執行立即停用。", 0x40)
		return
	}
	id := r.config.Groups[r.group].Endpoints[r.endpoint].ID
	g.busy = true
	g.refresh()
	go func() {
		settings, err := g.controller.DisableRelayEndpoint(g.path, id)
		g.post(guiResult{kind: "saved", settings: settings, err: err})
	}()
}
