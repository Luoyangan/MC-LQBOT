// Package admin provides a web-based admin panel for LQBOT.
// Features: password authentication, session management, dashboard with stats,
// recent logs, command list, 7-day trend chart, restart, active entities,
// system resources, SSE live stream, DB info, CSV export.
package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Luoyangan/LQBOT/internal/contract"
	framelog "github.com/Luoyangan/LQBOT/internal/log"
	"github.com/Luoyangan/LQBOT/internal/scheduler"
	"github.com/Luoyangan/LQBOT/internal/storage"
)

const sessionTTL = 24 * time.Hour
const passwordStorageKey = "admin_password_hash"

// VersionInfo holds build metadata for display on the dashboard.
type VersionInfo struct {
	App     string
	Version string
	Commit  string
	Date    string
}

// BotStatus holds live status of bot components for the dashboard.
type BotStatus struct {
	Running   bool   // bot event loop is active
	Adapter   bool   // WebSocket / Webhook connected
	Database  string // database driver name ("sqlite", "mysql"), empty when unavailable
	HTTPServe bool   // HTTP server is running
}

// dashData is the data model passed to the dashboard template.
type dashData struct {
	Title          string
	AppName        string
	Version        string
	Commit         string
	BuildDate      string
	Uptime         string
	Today          *storage.DailyRecord
	TotalUsers     int64
	TotalGroups    int64
	TotalChannels  int64
	TotalChanUsers int64

	// Dynamic status
	BotRunning   bool
	BotAdapter   bool
	BotDatabase  string
	BotHTTPServe bool

	// Recent logs (feature 1)
	RecentLogs []storage.LogEntry

	// Commands (feature 2)
	Commands []cmdInfo

	// 7-day trend (feature 3)
	TrendDaysCount int
	TrendDays      []string
	TrendIncoming  []int64
	TrendOutgoing  []int64
	TrendCmds      []int64

	// Active entities (feature 5)
	ActiveUsers    []storage.UserRecord
	ActiveGroups   []storage.GroupRecord
	ActiveChannels []storage.ChannelRecord

	// System resources (feature 6)
	SysMemUsed    uint64
	SysMemTotal   uint64
	SysGoroutines int
	SysDiskUsed   float64
	SysDiskTotal  float64

	// Per-disk info
	Disks []diskInfo

	// Network I/O (total since boot, in bytes)
	NetRX uint64
	NetTX uint64

	// Scheduler tasks
	SchedulerTasks []scheduler.TaskInfo

	// DB info (feature 8)
	DBPath       string
	DBFileSize   int64
	DBLogCount   int64
	DBUserCount  int64
	DBGroupCount int64
	DBChanCount  int64
}

// diskInfo holds usage for a single disk/mount point.
type diskInfo struct {
	MountPoint string  `json:"mp"`
	UsedGB     float64 `json:"u"`
	TotalGB    float64 `json:"t"`
	Percent    float64 `json:"p"`
}

func relativeTime(t time.Time) string {
	d := time.Since(t)
	abs := t.Format("01-02 15:04:05")
	if d < 10*time.Second {
		return abs + " (刚刚)"
	}
	if d < 60*time.Second {
		return fmt.Sprintf("%s (%d秒前)", abs, int(d.Seconds()))
	}
	if d < 60*time.Minute {
		return fmt.Sprintf("%s (%d分钟前)", abs, int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%s (%d小时前)", abs, int(d.Hours()))
	}
	if d < 30*24*time.Hour {
		return fmt.Sprintf("%s (%d天前)", abs, int(d.Hours()/24))
	}
	return abs
}

type cmdInfo struct {
	Name        string
	Description string
	Permission  string
	Aliases     string
	Usage       string
}

// Admin manages the web admin panel.
type Admin struct {
	store       *storage.Storage
	logger      *framelog.Logger
	sessions    map[string]time.Time
	sessionsMu  sync.RWMutex
	passwordSet bool
	version     VersionInfo
	startedAt   time.Time

	statusMu sync.RWMutex
	status   BotStatus

	commands []contract.Command

	// Scheduler reference for task display
	scheduler *scheduler.Scheduler

	// SSE clients
	sseClients   map[string]chan string
	sseClientsMu sync.RWMutex

	// Trend chart days (set from URL param, used by SSE)
	trendDays int

	// Network rate tracking
	lastNetRX   uint64
	lastNetTX   uint64
	lastNetTime time.Time
	totalNetRX  uint64
	totalNetTX  uint64

	// Restart function
	restartFn func() error

	// Shutdown function
	shutdownFn func()
}

// SetBotStatus updates the live component status displayed on the dashboard.
func (a *Admin) SetBotStatus(s BotStatus) {
	a.statusMu.Lock()
	a.status = s
	a.statusMu.Unlock()
}

// SetCommands sets the list of registered commands for the dashboard.
func (a *Admin) SetCommands(cmds []contract.Command) {
	a.commands = cmds
}

// PublishEvent pushes a live event to all SSE clients.
func (a *Admin) PublishEvent(eventType, summary string) {
	msg := fmt.Sprintf(`{"t":%q,"msg":%q,"time":%q}`, eventType, summary, time.Now().Format("15:04:05"))
	a.sseClientsMu.RLock()
	defer a.sseClientsMu.RUnlock()
	for _, ch := range a.sseClients {
		select {
		case ch <- msg:
		default:
		}
	}
}

// New creates a new Admin instance and registers routes on the given mux.
func New(store *storage.Storage, logger *framelog.Logger, mux *http.ServeMux, ver VersionInfo, restartFn func() error, shutdownFn func(), schedulers ...*scheduler.Scheduler) *Admin {
	if store == nil {
		return nil
	}

	var hash string
	if err := store.Get(passwordStorageKey, &hash); err == nil && hash != "" {
	}

	a := &Admin{
		store:       store,
		logger:      logger,
		sessions:    make(map[string]time.Time),
		passwordSet: hash != "",
		version:     ver,
		startedAt:   time.Now(),
		sseClients:  make(map[string]chan string),
		scheduler:   nil,
		restartFn:   restartFn,
		shutdownFn:  shutdownFn,
	}

	if len(schedulers) > 0 {
		a.scheduler = schedulers[0]
	}

	mux.HandleFunc("/admin", a.handleAdmin)
	mux.HandleFunc("/admin/", a.authMiddleware(a.handleDashboard))
	mux.HandleFunc("/admin/logout", a.handleLogout)
	mux.HandleFunc("/admin/sse", a.handleSSE)
	mux.HandleFunc("/admin/restart", a.authMiddleware(a.handleRestart))
	mux.HandleFunc("/admin/shutdown", a.authMiddleware(a.handleShutdown))
	mux.HandleFunc("/admin/export/logs", a.authMiddleware(a.handleExportLogs))
	mux.HandleFunc("/admin/export/daily", a.authMiddleware(a.handleExportDaily))

	logger.Info("admin panel enabled", "password_set", a.passwordSet)
	return a
}

// --- password ---

func (a *Admin) setPassword(password string) error {
	h := sha256.Sum256([]byte(password))
	hash := hex.EncodeToString(h[:])
	if err := a.store.Set(passwordStorageKey, hash); err != nil {
		return fmt.Errorf("save password hash: %w", err)
	}
	a.passwordSet = true
	return nil
}

func (a *Admin) verifyPassword(password string) bool {
	var stored string
	if err := a.store.Get(passwordStorageKey, &stored); err != nil {
		return false
	}
	h := sha256.Sum256([]byte(password))
	return hex.EncodeToString(h[:]) == stored
}

// --- session ---

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (a *Admin) createSession() (string, error) {
	token, err := generateToken()
	if err != nil {
		return "", err
	}
	a.sessionsMu.Lock()
	a.sessions[token] = time.Now().Add(sessionTTL)
	a.sessionsMu.Unlock()
	return token, nil
}

func (a *Admin) validateSession(token string) bool {
	a.sessionsMu.RLock()
	expiry, ok := a.sessions[token]
	a.sessionsMu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		a.sessionsMu.Lock()
		delete(a.sessions, token)
		a.sessionsMu.Unlock()
		return false
	}
	return true
}

func (a *Admin) destroySession(token string) {
	a.sessionsMu.Lock()
	delete(a.sessions, token)
	a.sessionsMu.Unlock()
}

func getSessionToken(r *http.Request) string {
	if c, err := r.Cookie("admin_session"); err == nil {
		return c.Value
	}
	return ""
}

// --- middleware ---

func (a *Admin) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("admin_session")
		if err != nil || !a.validateSession(cookie.Value) {
			http.Redirect(w, r, "/admin", http.StatusFound)
			return
		}
		next(w, r)
	}
}

// --- handlers ---

func (a *Admin) handleAdmin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.serveLoginPage(w, r)
	case http.MethodPost:
		a.handleLogin(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *Admin) serveLoginPage(w http.ResponseWriter, r *http.Request) {
	if t := getSessionToken(r); t != "" && a.validateSession(t) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if a.passwordSet {
		renderLogin(w, "")
	} else {
		renderSetup(w, "")
	}
}

func (a *Admin) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	password := r.FormValue("password")

	if !a.passwordSet {
		if password == "" {
			renderSetup(w, "密码不能为空")
			return
		}
		if confirm := r.FormValue("confirm_password"); password != confirm {
			renderSetup(w, "两次输入的密码不一致")
			return
		}
		if err := a.setPassword(password); err != nil {
			a.logger.Error("failed to set admin password", "error", err)
			renderSetup(w, "设置密码失败，请重试")
			return
		}
		a.logger.Info("admin password has been set")
		token, err := a.createSession()
		if err != nil {
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		setSessionCookie(w, token)
		http.Redirect(w, r, "/admin/", http.StatusFound)
		return
	}

	if !a.verifyPassword(password) {
		renderLogin(w, "密码错误")
		return
	}
	token, err := a.createSession()
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	setSessionCookie(w, token)
	http.Redirect(w, r, "/admin/", http.StatusFound)
}

func (a *Admin) handleLogout(w http.ResponseWriter, r *http.Request) {
	if t := getSessionToken(r); t != "" {
		a.destroySession(t)
	}
	http.SetCookie(w, &http.Cookie{Name: "admin_session", Value: "", Path: "/", Expires: time.Unix(0, 0)})
	http.Redirect(w, r, "/admin", http.StatusFound)
}

// --- Dashboard ---

func (a *Admin) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	a.statusMu.RLock()
	status := a.status
	a.statusMu.RUnlock()

	data := dashData{
		AppName:      a.version.App,
		Version:      a.version.Version,
		Commit:       a.version.Commit,
		BuildDate:    a.version.Date,
		Uptime:       formatDuration(time.Since(a.startedAt)),
		BotRunning:   status.Running,
		BotAdapter:   status.Adapter,
		BotDatabase:  status.Database,
		BotHTTPServe: status.HTTPServe,
	}

	// Today's stats
	today := time.Now().Format("2006-01-02")
	records, err := a.store.QueryDailyRecords(today, today, 1, 0)
	if err == nil && len(records) > 0 {
		data.Today = &records[0]
	}

	// Totals
	if n, err := a.store.CountUsers(); err == nil {
		data.TotalUsers = n
	}
	if n, err := a.store.CountGroups(); err == nil {
		data.TotalGroups = n
	}
	if n, err := a.store.CountChannels(); err == nil {
		data.TotalChannels = n
	}
	if n, err := a.store.CountChannelUsers(); err == nil {
		data.TotalChanUsers = n
	}

	// 1. Recent logs (latest 100)
	if logs, err := a.store.QueryLogs("", "", "", "", "", "", "", 100, 0); err == nil {
		data.RecentLogs = logs
	}

	// 2. Commands
	for _, c := range a.commands {
		perm := c.Permission
		if perm == "" {
			perm = "public"
		}
		aliases := strings.Join(c.Aliases, ", ")
		ci := cmdInfo{
			Name:        c.Name,
			Description: c.Description,
			Permission:  perm,
			Aliases:     aliases,
			Usage:       c.Usage,
		}
		data.Commands = append(data.Commands, ci)
	}
	sort.Slice(data.Commands, func(i, j int) bool {
		return data.Commands[i].Name < data.Commands[j].Name
	})

	// 3. Trend chart (configurable days)
	trendDays := 7
	if d := r.URL.Query().Get("days"); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n >= 1 && n <= 365 {
			trendDays = n
		}
	}
	a.trendDays = trendDays
	data.TrendDaysCount = trendDays
	start := time.Now().AddDate(0, 0, -(trendDays - 1)).Format("2006-01-02")
	if trend, err := a.store.QueryDailyRecords(start, today, trendDays, 0); err == nil {
		// Reverse so oldest first
		for i, j := 0, len(trend)-1; i < j; i, j = i+1, j-1 {
			trend[i], trend[j] = trend[j], trend[i]
		}
		// Build trend data arrays
		for _, r := range trend {
			dateDisplay := r.Date
			if len(dateDisplay) > 5 {
				dateDisplay = dateDisplay[5:] // "01-02"
			}
			data.TrendDays = append(data.TrendDays, dateDisplay)
			data.TrendIncoming = append(data.TrendIncoming, r.C2CIncomingMsg+r.GroupIncomingMsg+r.ChannelIncomingMsg)
			data.TrendOutgoing = append(data.TrendOutgoing, r.C2COutgoingMsg+r.GroupOutgoingMsg+r.ChannelOutgoingMsg)
			data.TrendCmds = append(data.TrendCmds, r.TotalCommands)
		}
	}

	// 5. Active entities
	if users, err := a.store.QueryUsers(8, 0); err == nil {
		data.ActiveUsers = users
	}
	if groups, err := a.store.QueryGroups(8, 0); err == nil {
		data.ActiveGroups = groups
	}
	if channels, err := a.store.QueryChannels(8, 0); err == nil {
		data.ActiveChannels = channels
	}

	// 5b. Scheduler tasks
	if a.scheduler != nil {
		data.SchedulerTasks = a.scheduler.Tasks()
	}

	// 6. System resources
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	data.SysMemUsed = m.Alloc
	data.SysMemTotal = m.Sys
	data.SysGoroutines = runtime.NumGoroutine()
	disks := getAllDiskInfo()
	data.Disks = disks
	var totalUsed, totalCap float64
	for _, d := range disks {
		totalUsed += d.UsedGB
		totalCap += d.TotalGB
	}
	data.SysDiskUsed = totalUsed
	data.SysDiskTotal = totalCap
	netRX, netTX := getNetworkIO()
	data.NetRX = netRX
	data.NetTX = netTX

	// 8. DB info
	dbPath := a.store.DSN()
	data.DBPath = dbPath
	if a.store.Driver() == "sqlite" {
		if fi, err := os.Stat(dbPath); err == nil {
			data.DBFileSize = fi.Size()
		}
	}
	if n, err := a.store.CountLogs(); err == nil {
		data.DBLogCount = n
	}
	if n, err := a.store.CountUsers(); err == nil {
		data.DBUserCount = n
	}
	if n, err := a.store.CountGroups(); err == nil {
		data.DBGroupCount = n
	}
	if n, err := a.store.CountChannels(); err == nil {
		data.DBChanCount = n
	}

	renderDashboard(w, data)
}

// --- SSE ---

// resourceSnapshot holds real-time system resource values pushed via SSE.
type resourceSnapshot struct {
	SysMem      string     `json:"mem"`
	SysCPU      string     `json:"scpu"`
	ProcCPU     string     `json:"pcpu"`
	ProcMem     string     `json:"pmem"`
	Gor         string     `json:"gor"`
	Disk        string     `json:"disk"`
	Disks       []diskInfo `json:"disks,omitempty"`
	Net         string     `json:"net"`
	NetUp       string     `json:"netup"`
	NetTotal    string     `json:"nett"`
	Uptime      string     `json:"ut"`
	BotRunning  bool       `json:"br"`
	BotAdapter  bool       `json:"ba"`
	BotDatabase string     `json:"bd"`
}

func (a *Admin) collectSnapshot() (snap resourceSnapshot) {
	defer func() {
		if r := recover(); r != nil {
			if a.logger != nil {
				a.logger.Error("collectSnapshot recovered from panic", "panic", fmt.Sprintf("%v", r))
			} else {
				fmt.Fprintf(os.Stderr, "collectSnapshot panic: %v\n", r)
			}
		}
	}()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	sysMemUsed, sysMemTotal := getSystemMemoryGB()
	disks := getAllDiskInfo()
	var totalUsed, totalCap float64
	for _, d := range disks {
		totalUsed += d.UsedGB
		totalCap += d.TotalGB
	}
	// Network rate from delta
	now := time.Now()
	netRX, netTX := getNetworkIO()
	var netStr string
	var netUpStr string
	if a.lastNetTime.IsZero() {
		a.totalNetRX = netRX
		a.totalNetTX = netTX
		netStr = formatBytes(netRX) + " / " + formatBytes(netTX)
		netUpStr = ""
	} else {
		elapsed := now.Sub(a.lastNetTime).Seconds()
		if elapsed > 0 {
			// Handle uint32 counter wrapping
			deltaRX := netRX - a.lastNetRX
			if netRX < a.lastNetRX {
				deltaRX = (uint64(math.MaxUint32) - a.lastNetRX) + netRX + 1
			}
			deltaTX := netTX - a.lastNetTX
			if netTX < a.lastNetTX {
				deltaTX = (uint64(math.MaxUint32) - a.lastNetTX) + netTX + 1
			}
			a.totalNetRX += deltaRX
			a.totalNetTX += deltaTX
			rxRate := float64(deltaRX) / elapsed
			txRate := float64(deltaTX) / elapsed
			netStr = "↓ " + formatRate(rxRate)
			netUpStr = "↑ " + formatRate(txRate)
		} else {
			netStr = "0"
			netUpStr = ""
		}
	}
	a.lastNetRX = netRX
	a.lastNetTX = netTX
	a.lastNetTime = now

	a.statusMu.RLock()
	s := a.status
	a.statusMu.RUnlock()
	return resourceSnapshot{
		SysMem:      fmt.Sprintf("%.1f / %.1f GB", sysMemUsed, sysMemTotal),
		SysCPU:      fmt.Sprintf("%.1f%% (%d核)", getSystemCPUUsage(), runtime.NumCPU()),
		ProcCPU:     fmt.Sprintf("%.1f%%", getProcessCPUUsage()),
		ProcMem:     fmt.Sprintf("%.1f MB", float64(m.Alloc)/1024/1024),
		Gor:         fmt.Sprintf("%d", runtime.NumGoroutine()),
		Disk:        fmt.Sprintf("%.1f / %.1f GB", totalUsed, totalCap),
		Disks:       disks,
		Net:         netStr,
		NetUp:       netUpStr,
		NetTotal:    formatBytes(a.totalNetRX) + " / " + formatBytes(a.totalNetTX),
		Uptime:      formatDuration(time.Since(a.startedAt)),
		BotRunning:  s.Running,
		BotAdapter:  s.Adapter,
		BotDatabase: s.Database,
	}
}

// todayStats holds today's statistics pushed via SSE.
type todayStats struct {
	C2CIn    string `json:"c2c_in"`
	C2COut   string `json:"c2c_out"`
	GroupIn  string `json:"grp_in"`
	GroupOut string `json:"grp_out"`
	ChanIn   string `json:"ch_in"`
	ChanOut  string `json:"ch_out"`
	Cmds     string `json:"cmds"`
	Interact string `json:"interact"`
	C2CUsers string `json:"c2c_u"`
	GroupAct string `json:"grp_a"`
	ChanAct  string `json:"ch_a"`
}

func (a *Admin) collectTodayStats() todayStats {
	today := time.Now().Format("2006-01-02")
	records, err := a.store.QueryDailyRecords(today, today, 1, 0)
	if err != nil || len(records) == 0 {
		return todayStats{}
	}
	t := records[0]
	return todayStats{
		C2CIn:    fmt.Sprintf("%d", t.C2CIncomingMsg),
		C2COut:   fmt.Sprintf("%d", t.C2COutgoingMsg),
		GroupIn:  fmt.Sprintf("%d", t.GroupIncomingMsg),
		GroupOut: fmt.Sprintf("%d", t.GroupOutgoingMsg),
		ChanIn:   fmt.Sprintf("%d", t.ChannelIncomingMsg),
		ChanOut:  fmt.Sprintf("%d", t.ChannelOutgoingMsg),
		Cmds:     fmt.Sprintf("%d", t.TotalCommands),
		Interact: fmt.Sprintf("%d", t.TotalInteractions),
		C2CUsers: fmt.Sprintf("%d", t.C2CIncomingUsers),
		GroupAct: fmt.Sprintf("%d", t.GroupActiveCount),
		ChanAct:  fmt.Sprintf("%d", t.ChannelActiveCount),
	}
}

// trendData holds 7-day trend chart data pushed via SSE.
type trendData struct {
	Days     []string `json:"days"`
	Incoming []int64  `json:"inc"`
	Outgoing []int64  `json:"out"`
	Cmds     []int64  `json:"cmd"`
	Count    int      `json:"cnt"`
}

func (a *Admin) collectTrendData(days int) trendData {
	days = max(days, 1)
	today := time.Now().Format("2006-01-02")
	start := time.Now().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	records, err := a.store.QueryDailyRecords(start, today, days, 0)
	if err != nil {
		return trendData{}
	}
	// Reverse so oldest first
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}
	td := trendData{Count: days}
	for _, r := range records {
		dateDisplay := r.Date
		if len(dateDisplay) > 5 {
			dateDisplay = dateDisplay[5:] // "01-02"
		}
		td.Days = append(td.Days, dateDisplay)
		td.Incoming = append(td.Incoming, r.C2CIncomingMsg+r.GroupIncomingMsg+r.ChannelIncomingMsg)
		td.Outgoing = append(td.Outgoing, r.C2COutgoingMsg+r.GroupOutgoingMsg+r.ChannelOutgoingMsg)
		td.Cmds = append(td.Cmds, r.TotalCommands)
	}
	return td
}

// dbSnapshot holds database info pushed via SSE.
type dbSnapshot struct {
	FileSize   string `json:"fs"`
	LogCount   string `json:"logs"`
	UserCount  string `json:"users"`
	GroupCount string `json:"grps"`
	ChanCount  string `json:"chans"`
}

func (a *Admin) collectDBInfo() dbSnapshot {
	var sn dbSnapshot
	dbPath := a.store.DSN()
	if a.store.Driver() == "sqlite" {
		if fi, err := os.Stat(dbPath); err == nil {
			sn.FileSize = fmt.Sprintf("%.1f KB", float64(fi.Size())/1024)
		}
	} else {
		sn.FileSize = "—"
	}
	if n, err := a.store.CountLogs(); err == nil {
		sn.LogCount = fmt.Sprintf("%d", n)
	}
	if n, err := a.store.CountUsers(); err == nil {
		sn.UserCount = fmt.Sprintf("%d", n)
	}
	if n, err := a.store.CountGroups(); err == nil {
		sn.GroupCount = fmt.Sprintf("%d", n)
	}
	if n, err := a.store.CountChannels(); err == nil {
		sn.ChanCount = fmt.Sprintf("%d", n)
	}
	return sn
}

// entitiesSnapshot holds active entities pushed via SSE.
type entitiesSnapshot struct {
	Users    []entityItem `json:"users,omitempty"`
	Groups   []entityItem `json:"groups,omitempty"`
	Channels []entityItem `json:"channels,omitempty"`
}

type entityItem struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Msg   int64  `json:"msg"`
	Scene string `json:"scene,omitempty"`
	Last  string `json:"last"`
}

func (a *Admin) collectEntities() entitiesSnapshot {
	users, err := a.store.QueryUsers(8, 0)
	var es entitiesSnapshot
	if err == nil {
		for _, u := range users {
			es.Users = append(es.Users, entityItem{
				ID: u.UserID, Name: u.Username, Msg: u.MessageCount, Scene: u.Scene, Last: relativeTime(u.LastSeenAt),
			})
		}
	}
	groups, err := a.store.QueryGroups(8, 0)
	if err == nil {
		for _, g := range groups {
			es.Groups = append(es.Groups, entityItem{
				ID: g.GroupID, Msg: g.MessageCount, Last: relativeTime(g.LastSeenAt),
			})
		}
	}
	chans, err := a.store.QueryChannels(8, 0)
	if err == nil {
		for _, c := range chans {
			es.Channels = append(es.Channels, entityItem{
				ID: c.ChannelID, Msg: c.MessageCount, Last: relativeTime(c.LastSeenAt),
			})
		}
	}
	return es
}

func (a *Admin) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := make(chan string, 16)

	// Register client
	a.sseClientsMu.Lock()
	id := fmt.Sprintf("sse_%d", time.Now().UnixNano())
	a.sseClients[id] = ch
	a.sseClientsMu.Unlock()

	// Cleanup on disconnect
	defer func() {
		a.sseClientsMu.Lock()
		delete(a.sseClients, id)
		close(ch)
		a.sseClientsMu.Unlock()
	}()

	// Send initial connection event
	fmt.Fprintf(w, "data: {\"t\":\"connected\",\"msg\":\"SSE connected\",\"time\":%q}\n\n", time.Now().Format("15:04:05"))
	// Send initial resource snapshot
	rs := a.collectSnapshot()
	rsJSON, _ := json.Marshal(rs)
	fmt.Fprintf(w, "data: {\"t\":\"resource\",\"d\":%s}\n\n", rsJSON)
	// Send initial today stats & DB info
	ts := a.collectTodayStats()
	tsJSON, _ := json.Marshal(ts)
	fmt.Fprintf(w, "data: {\"t\":\"today\",\"d\":%s}\n\n", tsJSON)
	td := a.collectTrendData(a.trendDays)
	tdJSON, _ := json.Marshal(td)
	fmt.Fprintf(w, "data: {\"t\":\"trend\",\"d\":%s}\n\n", tdJSON)
	di := a.collectDBInfo()
	diJSON, _ := json.Marshal(di)
	fmt.Fprintf(w, "data: {\"t\":\"db\",\"d\":%s}\n\n", diJSON)
	// Send initial entities snapshot
	es := a.collectEntities()
	esJSON, _ := json.Marshal(es)
	fmt.Fprintf(w, "data: {\"t\":\"entities\",\"d\":%s}\n\n", esJSON)
	flusher.Flush()

	// Track last pushed log ID for incremental log pushing
	// Initialize to current max log ID so only truly new logs are pushed
	var lastLogID uint
	if latest, err := a.store.QueryLogs("", "", "", "", "", "", "", 1, 0); err == nil && len(latest) > 0 {
		lastLogID = latest[0].ID
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	// Cached previous values — only push on actual change
	var prevResource, prevToday, prevTrend, prevDB, prevEntities string

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Resource snapshot (comparison kept simple — JSON string compare)
			rs := a.collectSnapshot()
			rsJSON, _ := json.Marshal(rs)
			rsStr := string(rsJSON)
			if rsStr != prevResource {
				prevResource = rsStr
				fmt.Fprintf(w, "data: {\"t\":\"resource\",\"d\":%s}\n\n", rsJSON)
			}

			// Today's stats snapshot — push only when numbers change
			ts := a.collectTodayStats()
			tsJSON, _ := json.Marshal(ts)
			tsStr := string(tsJSON)
			if tsStr != prevToday {
				prevToday = tsStr
				fmt.Fprintf(w, "data: {\"t\":\"today\",\"d\":%s}\n\n", tsJSON)
			}

			// Trend chart snapshot
			td := a.collectTrendData(a.trendDays)
			tdJSON, _ := json.Marshal(td)
			tdStr := string(tdJSON)
			if tdStr != prevTrend {
				prevTrend = tdStr
				fmt.Fprintf(w, "data: {\"t\":\"trend\",\"d\":%s}\n\n", tdJSON)
			}

			// DB info snapshot
			di := a.collectDBInfo()
			diJSON, _ := json.Marshal(di)
			diStr := string(diJSON)
			if diStr != prevDB {
				prevDB = diStr
				fmt.Fprintf(w, "data: {\"t\":\"db\",\"d\":%s}\n\n", diJSON)
			}

			// Entities snapshot
			es := a.collectEntities()
			esJSON, _ := json.Marshal(es)
			esStr := string(esJSON)
			if esStr != prevEntities {
				prevEntities = esStr
				fmt.Fprintf(w, "data: {\"t\":\"entities\",\"d\":%s}\n\n", esJSON)
			}

			// New logs since last push (query up to 10 at a time)
			if newLogs, err := a.store.QueryLogsSince(lastLogID, 10); err == nil && len(newLogs) > 0 {
				lastLogID = newLogs[len(newLogs)-1].ID
				type logItem struct {
					ID     uint   `json:"id"`
					Level  string `json:"level"`
					Time   string `json:"time"`
					Msg    string `json:"msg"`
					Source string `json:"src"`
					Event  string `json:"evt"`
				}
				items := make([]logItem, len(newLogs))
				for i, l := range newLogs {
					items[i] = logItem{
						ID:     l.ID,
						Level:  l.Level,
						Time:   l.CreatedAt.Format("15:04:05"),
						Msg:    l.Message,
						Source: l.Source,
						Event:  l.EventType,
					}
				}
				logsJSON, _ := json.Marshal(items)
				fmt.Fprintf(w, "data: {\"t\":\"logs\",\"d\":%s}\n\n", logsJSON)
			}

			flusher.Flush()
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

// --- Restart ---

func (a *Admin) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.logger.Warn("admin restart requested")
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"restarting"}`))

	// Run in goroutine so HTTP response can be sent
	go func() {
		time.Sleep(500 * time.Millisecond)
		if a.restartFn != nil {
			_ = a.restartFn()
		}
	}()
}

// --- Shutdown ---

func (a *Admin) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.logger.Warn("admin shutdown requested")
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"shutting_down"}`))

	// Run in goroutine so HTTP response can be sent
	go func() {
		time.Sleep(500 * time.Millisecond)
		if a.shutdownFn != nil {
			a.shutdownFn()
		}
	}()
}

// --- CSV Export ---

func (a *Admin) handleExportLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := a.store.QueryLogs("", "", "", "", "", "", "", 10000, 0)
	if err != nil {
		http.Error(w, "Query failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=logs_%s.csv", time.Now().Format("20060102")))
	w.Write([]byte("\xef\xbb\xbf")) // BOM for Excel

	// Header
	w.Write([]byte("id,level,message,source,event_type,author_id,created_at\n"))
	for _, l := range logs {
		msg := escapeCSV(l.Message)
		line := fmt.Sprintf("%d,%s,%s,%s,%s,%s,%s\n",
			l.ID, l.Level, msg, l.Source, l.EventType, l.AuthorID, l.CreatedAt.Format("2006-01-02 15:04:05"))
		w.Write([]byte(line))
	}
}

func (a *Admin) handleExportDaily(w http.ResponseWriter, r *http.Request) {
	records, err := a.store.QueryDailyRecords("", "", 366, 0)
	if err != nil {
		http.Error(w, "Query failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=daily_%s.csv", time.Now().Format("20060102")))
	w.Write([]byte("\xef\xbb\xbf"))

	w.Write([]byte("date,c2c_in,c2c_out,group_in,group_out,channel_in,channel_out,commands,interactions\n"))
	for _, r := range records {
		line := fmt.Sprintf("%s,%d,%d,%d,%d,%d,%d,%d,%d\n",
			r.Date, r.C2CIncomingMsg, r.C2COutgoingMsg,
			r.GroupIncomingMsg, r.GroupOutgoingMsg,
			r.ChannelIncomingMsg, r.ChannelOutgoingMsg,
			r.TotalCommands, r.TotalInteractions)
		w.Write([]byte(line))
	}
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: "admin_session", Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(sessionTTL.Seconds()),
	})
}

func escapeCSV(s string) string {
	s = strings.ReplaceAll(s, `"`, `""`)
	if strings.ContainsAny(s, ",\"\n") {
		s = `"` + s + `"`
	}
	return s
}

// --- HTML / CSS ---

var css = strings.ReplaceAll(`*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif;background:#fff;color:#1a1a1a;line-height:1.6;min-height:100vh}
a{color:#1a1a1a;text-decoration:none} a:hover{opacity:0.7}
.container{max-width:960px;margin:0 auto;padding:40px 24px}
.container-wide{max-width:1200px}
.card{background:#fff;border:1px solid #e5e5e5;border-radius:8px;padding:32px;max-width:400px;margin:80px auto}
.card h1{font-size:20px;font-weight:600;margin-bottom:8px;letter-spacing:-0.01em}
.card p{font-size:14px;color:#666;margin-bottom:24px}
.form-group{margin-bottom:16px}
.form-group label{display:block;font-size:13px;font-weight:500;color:#333;margin-bottom:4px}
.form-group input{width:100%;padding:10px 12px;font-size:14px;border:1px solid #d4d4d4;border-radius:6px;outline:none;transition:border-color .2s;background:#fff}
.form-group input:focus{border-color:#1a1a1a}
.btn{display:inline-block;width:100%;padding:10px 16px;font-size:14px;font-weight:500;color:#fff;background:#1a1a1a;border:none;border-radius:6px;cursor:pointer;transition:opacity .2s}
.btn-sm{display:inline-block;padding:6px 14px;font-size:12px;font-weight:500;color:#fff;background:#1a1a1a;border:none;border-radius:4px;cursor:pointer;transition:opacity .2s}
.btn-sm-outline{display:inline-block;padding:5px 13px;font-size:12px;font-weight:500;color:#1a1a1a;background:#fff;border:1px solid #d4d4d4;border-radius:4px;cursor:pointer;transition:all .2s;text-decoration:none}
.btn-sm-outline:hover{background:#f5f5f5}
.btn-sm-danger{display:inline-block;padding:6px 14px;font-size:12px;font-weight:500;color:#fff;background:#e53e3e;border:none;border-radius:4px;cursor:pointer;transition:opacity .2s}
.btn:hover,.btn-sm:hover,.btn-sm-danger:hover{opacity:0.85}
.error{font-size:13px;color:#e53e3e;margin-bottom:16px;padding:8px 12px;background:#fff5f5;border:1px solid #fed7d7;border-radius:4px}
.success{font-size:13px;color:#16a34a;margin-bottom:16px;padding:8px 12px;background:#f0fdf4;border:1px solid #bbf7d0;border-radius:4px}
.nav{display:flex;align-items:center;justify-content:space-between;padding:16px 0;border-bottom:1px solid #e5e5e5;margin-bottom:32px}
.nav h2{font-size:18px;font-weight:600}
.nav .nav-links{display:flex;gap:16px;align-items:center}
.nav .nav-links a,.nav .nav-links span{font-size:13px;color:#666}
.nav .nav-links a:hover{color:#1a1a1a}
.section{margin-bottom:32px;overflow:hidden}
.section h3{font-size:15px;font-weight:600;margin-bottom:12px;color:#333}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:12px}
.stat-card{padding:16px;border:1px solid #e5e5e5;border-radius:8px}
.stat-card .num{font-size:24px;font-weight:600;margin-bottom:2px}
.stat-card .lbl{font-size:12px;color:#888}
.stat-card .sub{font-size:11px;color:#aaa;margin-top:2px}
.tbl{width:100%;border-collapse:collapse;font-size:13px}
.tbl td{padding:10px 0;border-bottom:1px solid #f0f0f0}
.tbl td:last-child{text-align:right;font-weight:500;color:#333}
.tbl tr:last-child td{border-bottom:none}
.info-line{font-size:13px;padding:8px 0;border-bottom:1px solid #f0f0f0;display:flex;justify-content:space-between}
.info-line:last-child{border-bottom:none}
.info-line .k{color:#888} .info-line .v{font-weight:500}
.footer{text-align:center;padding:32px 0;font-size:12px;color:#999}
/* Log table */
.log-table{width:100%;border-collapse:collapse;font-size:12px;font-family:SFMono-Regular,Consolas,"Liberation Mono",Menlo,monospace}
.log-table th{text-align:left;padding:8px 6px;background:#fafafa;border-bottom:1px solid #e5e5e5;font-weight:600;color:#555}
.log-table td{padding:6px;border-bottom:1px solid #f0f0f0;vertical-align:top;color:#333}
.log-table tr:hover td{background:#fafafa}
.log-level{display:inline-block;padding:1px 6px;border-radius:3px;font-size:11px;font-weight:600}
.log-lv-info{background:#e8f4f8;color:#0369a1}
.log-lv-warn{background:#fef3c7;color:#b45309}
.log-lv-error{background:#fee2e2;color:#b91c1c}
.log-lv-debug{background:#f0f0f0;color:#666}
.log-lv-trace{background:#f5f5f5;color:#999}
/* Log scroll container */
.log-scroll{max-height:600px;overflow-y:auto}
/* Command list */
.cmd-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:8px}
.cmd-card{padding:12px;border:1px solid #e5e5e5;border-radius:6px}
.cmd-card .cmd-name{font-size:13px;font-weight:600}
.cmd-card .cmd-desc{font-size:11px;color:#888;margin-top:2px}
.cmd-card .cmd-usage{font-size:11px;color:#666;margin-top:2px;font-family:SFMono-Regular,Consolas,"Liberation Mono",Menlo,monospace}
.cmd-card .cmd-perm{font-size:10px;color:#aaa;margin-top:2px}
/* Bar chart */
.chart-wrap{display:flex;align-items:flex-end;gap:8px;height:120px;padding:16px 0 4px;border-bottom:1px solid #e5e5e5}
.chart-bar{flex:1;display:flex;flex-direction:column;align-items:center;gap:4px;min-width:0}
.chart-bar-group{display:flex;align-items:flex-end;gap:2px;height:100%}
.chart-bar-inner{width:10px;border-radius:2px 2px 0 0;transition:height .3s;min-height:2px}
.chart-bar-inner.incoming{background:#1a1a1a}
.chart-bar-inner.outgoing{background:#d4d4d4}
.chart-bar-inner.cmds{background:#999}
.chart-label{font-size:10px;color:#888;white-space:nowrap}
.chart-legend{display:flex;gap:16px;font-size:12px;color:#555;margin-bottom:4px}
.chart-legend span::before{content:"";display:inline-block;width:10px;height:10px;border-radius:2px;margin-right:4px;vertical-align:middle}
.chart-legend .leg-incoming::before{background:#1a1a1a}
.chart-legend .leg-outgoing::before{background:#d4d4d4}
.chart-legend .leg-cmds::before{background:#999}
/* Active entities */
.entity-scroll{overflow-y:auto;max-height:230px}
.entity-tbl{width:100%;border-collapse:collapse;font-size:12px}
.entity-tbl th{text-align:left;padding:6px;border-bottom:1px solid #e5e5e5;font-weight:600;color:#555}
.entity-tbl td{padding:6px;border-bottom:1px solid #f0f0f0;color:#333}
.entity-tbl tr:hover td{background:#fafafa}
.entity-grid{display:grid;grid-template-columns:1fr;gap:12px}
/* System resources */
.res-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(140px,1fr));gap:10px;align-items:start}
.res-card{padding:14px;border:1px solid #e5e5e5;border-radius:6px;text-align:center}
.res-card .res-val{font-size:18px;font-weight:600}
.res-card .res-lbl{font-size:11px;color:#888;margin-top:2px}
/* Disk list inside res-card */
.dc{padding-bottom:8px}
.dl{margin-top:6px;font-size:10px;text-align:left;max-height:100px;overflow-y:auto}
.dr{display:flex;align-items:center;gap:4px;padding:1px 0;overflow:hidden}
.dn{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;flex-shrink:1;min-width:0;color:#555;font-weight:500}
.ds{flex-shrink:0;white-space:nowrap;text-align:right;color:#888;margin-left:auto}
/* Sub-value inside res-card */
.res-subval{font-size:9px;color:#aaa;margin-top:-2px;margin-bottom:2px;line-height:1.3}
#net-up{font-size:14px;color:#888;margin-bottom:1px}
/* Actions bar */
.actions{display:flex;gap:8px;flex-wrap:wrap;margin-bottom:16px}
/* Tab system for logs/commands/entities */
.tab-bar{display:flex;gap:0;border-bottom:1px solid #e5e5e5;margin-bottom:12px}
.tab-btn{padding:8px 16px;font-size:13px;border:none;background:none;cursor:pointer;color:#888;border-bottom:2px solid transparent;transition:all .2s}
.tab-btn:hover{color:#333}
.tab-btn.active{color:#1a1a1a;border-bottom-color:#1a1a1a;font-weight:500}
.tab-panel{display:none}
.tab-panel.active{display:block}
/* SSE status mini-widget */
#sse-status{font-size:11px;color:#999;cursor:pointer}
#sse-status.connected{color:#16a34a}
/* Trend mini-cards row */
.trend-mini{display:grid;grid-template-columns:repeat(3,1fr);gap:8px;margin-top:8px}
.trend-mini-item{text-align:center;padding:8px;border:1px solid #f0f0f0;border-radius:4px}
.trend-mini-item .tm-val{font-size:14px;font-weight:600}
.trend-mini-item .tm-lbl{font-size:10px;color:#888}
/* Restart confirm overlay */
.modal-overlay{display:none;position:fixed;top:0;left:0;width:100%;height:100%;background:rgba(0,0,0,0.3);z-index:100;justify-content:center;align-items:center}
.modal-overlay.show{display:flex}
.modal-box{background:#fff;padding:24px;border-radius:8px;max-width:320px;text-align:center}
.modal-box h4{font-size:15px;margin-bottom:8px}
.modal-box p{font-size:13px;color:#666;margin-bottom:16px}
.modal-actions{display:flex;gap:8px;justify-content:center}
.modal-actions .btn-sm{width:auto}
/* Responsive */
@media(max-width:640px){.entity-grid{grid-template-columns:1fr}.trend-mini{grid-template-columns:1fr}.container{padding:24px 16px}}`, "\n", "")

// --- Login / Setup pages ---

func writeHTMLHead(w io.Writer, title string) {
	w.Write([]byte("<!DOCTYPE html>\n<html lang=\"zh-CN\">\n<head>\n<meta charset=\"UTF-8\">\n<meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\">\n<title>" + title + "</title>\n<style>" + css + "</style>\n</head>\n<body>\n<div class=\"container\">\n"))
}

func writeHTMLTail(w io.Writer) {
	w.Write([]byte("\n</div>\n</body>\n</html>"))
}

func renderLogin(w io.Writer, errMsg string) {
	writeHTMLHead(w, "登录 - LQBOT")
	w.Write([]byte("<div class=\"card\">\n  <h1>登录管理后台</h1>\n  <p>请输入管理员密码以继续</p>\n"))
	if errMsg != "" {
		w.Write([]byte("  <div class=\"error\">"))
		template.HTMLEscape(w, []byte(errMsg))
		w.Write([]byte("</div>\n"))
	}
	w.Write([]byte("  <form method=\"post\" action=\"/admin\">\n    <div class=\"form-group\">\n      <label for=\"password\">密码</label>\n      <input type=\"password\" id=\"password\" name=\"password\" placeholder=\"输入密码\" autofocus required>\n    </div>\n    <button type=\"submit\" class=\"btn\">登录</button>\n  </form>\n</div>"))
	writeHTMLTail(w)
}

func renderSetup(w io.Writer, errMsg string) {
	writeHTMLHead(w, "初始化密码 - LQBOT")
	w.Write([]byte("<div class=\"card\">\n  <h1>初始化管理员密码</h1>\n  <p>首次使用，请设置管理员密码</p>\n"))
	if errMsg != "" {
		w.Write([]byte("  <div class=\"error\">"))
		template.HTMLEscape(w, []byte(errMsg))
		w.Write([]byte("</div>\n"))
	}
	w.Write([]byte("  <form method=\"post\" action=\"/admin\">\n    <div class=\"form-group\">\n      <label for=\"password\">设置密码</label>\n      <input type=\"password\" id=\"password\" name=\"password\" placeholder=\"输入密码\" autofocus required>\n    </div>\n    <div class=\"form-group\">\n      <label for=\"confirm_password\">确认密码</label>\n      <input type=\"password\" id=\"confirm_password\" name=\"confirm_password\" placeholder=\"再次输入密码\" required>\n    </div>\n    <button type=\"submit\" class=\"btn\">设置密码并登录</button>\n  </form>\n</div>"))
	writeHTMLTail(w)
}

// --- Dashboard ---

func renderDashboard(w io.Writer, d dashData) {
	writeHTMLHead(w, "管理后台 - LQBOT")

	w.Write([]byte("<div class=\"container container-wide\">\n"))

	// Nav
	w.Write([]byte("<div class=\"nav\">\n  <h2>管理后台</h2>\n  <div class=\"nav-links\">\n    <button class=\"btn-sm-outline\" id=\"sse-btn\" onclick=\"toggleSSE()\" style=\"font-size:11px;padding:2px 8px;margin-right:8px\">关闭实时</button>\n    <span id=\"sse-status\">● 已连接</span>\n    <span>" + d.AppName + " v" + d.Version + "</span>\n    <a href=\"/admin/logout\">退出登录</a>\n  </div>\n</div>\n"))

	// Actions bar
	w.Write([]byte("<div class=\"actions\">\n"))
	w.Write([]byte("  <button class=\"btn-sm-outline\" onclick=\"document.querySelector('.modal-overlay').classList.add('show')\">重启</button>\n"))
	w.Write([]byte("  <button class=\"btn-sm-outline\" onclick=\"document.querySelector('.shutdown-overlay').classList.add('show')\" style=\"color:#e53e3e;border-color:#e53e3e\">关闭</button>\n"))
	w.Write([]byte("  <a href=\"/admin/export/logs\" class=\"btn-sm-outline\">导出日志</a>\n"))
	w.Write([]byte("  <a href=\"/admin/export/daily\" class=\"btn-sm-outline\">导出日统计</a>\n"))
	w.Write([]byte("</div>\n"))

	// Restart confirmation modal
	w.Write([]byte("<div class=\"modal-overlay\" onclick=\"if(event.target===this)this.classList.remove('show')\">\n  <div class=\"modal-box\">\n    <h4>确认重启</h4>\n    <p>重启后将断开所有连接，确认要重启吗？</p>\n    <div class=\"modal-actions\">\n      <form method=\"post\" action=\"/admin/restart\">\n        <button type=\"submit\" class=\"btn-sm-danger\">确认重启</button>\n      </form>\n      <button class=\"btn-sm-outline\" onclick=\"document.querySelector('.modal-overlay').classList.remove('show')\">取消</button>\n    </div>\n  </div>\n</div>\n"))

	// Shutdown confirmation modal
	w.Write([]byte("<div class=\"modal-overlay shutdown-overlay\" onclick=\"if(event.target===this)this.classList.remove('show')\">\n  <div class=\"modal-box\">\n    <h4>确认关闭</h4>\n    <p>关闭后机器人将停止运行，确认要关闭吗？</p>\n    <div class=\"modal-actions\">\n      <form method=\"post\" action=\"/admin/shutdown\">\n        <button type=\"submit\" class=\"btn-sm-danger\">确认关闭</button>\n      </form>\n      <button class=\"btn-sm-outline\" onclick=\"document.querySelector('.shutdown-overlay').classList.remove('show')\">取消</button>\n    </div>\n  </div>\n</div>\n"))

	// ---- 1. System Resources ----
	w.Write([]byte("<div class=\"section\">\n  <h3>系统资源</h3>\n"))
	w.Write([]byte("<div class=\"res-grid\">\n"))
	resMem := fmt.Sprintf("%.1f MB", float64(d.SysMemUsed)/1024/1024)
	resGor := fmt.Sprintf("%d", d.SysGoroutines)
	sysMemUsed, sysMemTotal := getSystemMemoryGB()
	resSysMem := fmt.Sprintf("%.1f / %.1f GB", sysMemUsed, sysMemTotal)
	cpuPct := getSystemCPUUsage()
	procCpuPct := getProcessCPUUsage()
	writeResCard(w, fmt.Sprintf("%.1f%% (%d核)", cpuPct, runtime.NumCPU()), "CPU")
	writeResCard(w, resSysMem, "内存")
	writeResCard(w, fmt.Sprintf("%.1f%%", procCpuPct), "进程CPU")
	writeResCard(w, resMem, "进程内存")
	writeResCard(w, resGor, "Goroutine")
	// Multi-disk card
	w.Write([]byte("<div class=\"res-card dc\" id=\"disk-card\">\n"))
	w.Write([]byte(fmt.Sprintf("  <div class=\"res-val\" id=\"disk-total\">%.1f / %.1f GB</div>\n", d.SysDiskUsed, d.SysDiskTotal)))
	w.Write([]byte("  <div class=\"res-lbl\">磁盘</div>\n"))
	if len(d.Disks) > 0 {
		w.Write([]byte("  <div class=\"dl\" id=\"disk-list\">\n"))
		for _, disk := range d.Disks {
			w.Write([]byte(fmt.Sprintf("    <div class=\"dr\" data-mount=%[1]q><span class=\"dn\" title=%[1]q>%[1]s</span><span class=\"ds\">%.1f / %.1f GB</span></div>\n",
				disk.MountPoint, disk.UsedGB, disk.TotalGB)))
		}
		w.Write([]byte("  </div>\n"))
	}
	w.Write([]byte("</div>\n"))
	// Network I/O card
	w.Write([]byte("<div class=\"res-card\">\n"))
	w.Write([]byte(fmt.Sprintf("  <div class=\"res-val\" id=\"net-rate\">%s</div>\n", template.HTMLEscapeString(formatBytes(d.NetRX)+" / "+formatBytes(d.NetTX)))))
	w.Write([]byte(fmt.Sprintf("  <div class=\"res-subval\" id=\"net-up\">%s</div>\n", "")))
	w.Write([]byte(fmt.Sprintf("  <div class=\"res-subval\" id=\"net-total\">总 %s</div>\n", template.HTMLEscapeString(formatBytes(d.NetRX)+" / "+formatBytes(d.NetTX)))))
	w.Write([]byte("  <div class=\"res-lbl\">网络</div>\n"))
	w.Write([]byte("</div>\n"))
	w.Write([]byte("</div>\n"))
	w.Write([]byte("</div>\n"))

	// ---- 2. Today's Stats ----
	w.Write([]byte("<div class=\"section\">\n  <h3>今日消息统计</h3>\n"))
	if d.Today != nil {
		t := d.Today
		w.Write([]byte("  <div class=\"grid\" id=\"today-grid\">\n"))
		writeStatCard(w, fmt.Sprintf("%d", t.C2CIncomingMsg), "C2C 上行", fmt.Sprintf("%d 活跃用户", t.C2CIncomingUsers))
		writeStatCard(w, fmt.Sprintf("%d", t.C2COutgoingMsg), "C2C 下行", "")
		writeStatCard(w, fmt.Sprintf("%d", t.GroupIncomingMsg), "群聊上行", fmt.Sprintf("%d 活跃群", t.GroupActiveCount))
		writeStatCard(w, fmt.Sprintf("%d", t.GroupOutgoingMsg), "群聊下行", "")
		writeStatCard(w, fmt.Sprintf("%d", t.ChannelIncomingMsg), "频道上行", fmt.Sprintf("%d 频道", t.ChannelActiveCount))
		writeStatCard(w, fmt.Sprintf("%d", t.ChannelOutgoingMsg), "频道下行", "")
		writeStatCard(w, fmt.Sprintf("%d", t.TotalCommands), "指令执行", "")
		writeStatCard(w, fmt.Sprintf("%d", t.TotalInteractions), "按钮交互", "")
		w.Write([]byte("  </div>\n"))
	} else {
		w.Write([]byte("  <p style=\"font-size:13px;color:#999\">暂无今日数据</p>\n"))
	}
	w.Write([]byte("</div>\n"))

	// ---- 3. Cumulative Data ----
	w.Write([]byte("<div class=\"section\">\n  <h3>累计数据</h3>\n  <div class=\"grid\">\n"))
	writeStatCard(w, fmt.Sprintf("%d", d.TotalUsers), "群聊/C2C 用户", "")
	writeStatCard(w, fmt.Sprintf("%d", d.TotalChanUsers), "频道用户", "")
	writeStatCard(w, fmt.Sprintf("%d", d.TotalGroups), "群聊", "")
	writeStatCard(w, fmt.Sprintf("%d", d.TotalChannels), "频道", "")
	w.Write([]byte("  </div>\n</div>\n"))

	// ---- 4. 7-day Trend (feature 3) ----
	if len(d.TrendDays) > 0 {
		w.Write([]byte(fmt.Sprintf("<div class=\"section\">\n  <h3>近 %d 日趋势</h3>\n", d.TrendDaysCount)))

		// Date range selector
		w.Write([]byte(fmt.Sprintf("<div style=\"font-size:12px;color:#888;margin-bottom:8px\">范围: %d 天 | ", d.TrendDaysCount)))
		for _, preset := range []int{3, 7, 14, 30} {
			active := ""
			if preset == d.TrendDaysCount {
				active = "font-weight:600;color:#1a1a1a"
			}
			w.Write([]byte(fmt.Sprintf("<a href=\"?days=%d\" style=\"margin-right:8px;%s\">%d天</a>", preset, active, preset)))
		}
		w.Write([]byte("</div>\n"))
		w.Write([]byte("<div class=\"chart-legend\" id=\"trend-legend\"><span class=\"leg-incoming\">上行</span><span class=\"leg-outgoing\">下行</span><span class=\"leg-cmds\">指令</span></div>\n"))
		w.Write([]byte("<div class=\"ctip\" id=\"chart-tt\"></div>\n"))
		w.Write([]byte("<div class=\"chart-wrap\" id=\"trend-chart-wrap\">\n"))

		maxVal := int64(1)
		for _, v := range d.TrendIncoming {
			if v > maxVal {
				maxVal = v
			}
		}
		for _, v := range d.TrendOutgoing {
			if v > maxVal {
				maxVal = v
			}
		}
		for _, v := range d.TrendCmds {
			if v > maxVal {
				maxVal = v
			}
		}

		// Determine label step to avoid crowding
		labelStep := 1
		if len(d.TrendDays) > 14 {
			labelStep = 2
		}
		if len(d.TrendDays) > 28 {
			labelStep = 4
		}

		for i := 0; i < len(d.TrendDays); i++ {
			incVal := int64(0)
			outVal := int64(0)
			cmdVal := int64(0)
			incH := int64(2)
			outH := int64(2)
			cmdH := int64(2)
			if maxVal > 0 {
				if i < len(d.TrendIncoming) {
					incVal = d.TrendIncoming[i]
					incH = incVal * 100 / maxVal
					if incH < 2 {
						incH = 2
					}
				}
				if i < len(d.TrendOutgoing) {
					outVal = d.TrendOutgoing[i]
					outH = outVal * 100 / maxVal
					if outH < 2 {
						outH = 2
					}
				}
				if i < len(d.TrendCmds) {
					cmdVal = d.TrendCmds[i]
					cmdH = cmdVal * 100 / maxVal
					if cmdH < 2 {
						cmdH = 2
					}
				}
			}
			dayLabel := d.TrendDays[i]
			// Only show label on step intervals to avoid crowding
			showLabel := dayLabel
			if labelStep > 1 && i%labelStep != 0 && i != len(d.TrendDays)-1 {
				showLabel = ""
			}
			barTitle := fmt.Sprintf("%s | 上行:%d  下行:%d  指令:%d", dayLabel, incVal, outVal, cmdVal)
			s := fmt.Sprintf("  <div class=\"chart-bar\" title=\"%s\"><div class=\"chart-bar-group\"><div class=\"chart-bar-inner incoming\" title=\"上行:%d\" style=\"height:%dpx\"></div><div class=\"chart-bar-inner outgoing\" title=\"下行:%d\" style=\"height:%dpx\"></div><div class=\"chart-bar-inner cmds\" title=\"指令:%d\" style=\"height:%dpx\"></div></div><div class=\"chart-label\">%s</div></div>\n",
				barTitle, incVal, incH, outVal, outH, cmdVal, cmdH, showLabel)
			w.Write([]byte(s))
		}
		w.Write([]byte("</div>\n"))

		// Trend totals
		totalInc := int64(0)
		totalOut := int64(0)
		totalCmd := int64(0)
		for _, v := range d.TrendIncoming {
			totalInc += v
		}
		for _, v := range d.TrendOutgoing {
			totalOut += v
		}
		for _, v := range d.TrendCmds {
			totalCmd += v
		}
		trendMiniTmpl := fmt.Sprintf("<div class=\"trend-mini\" id=\"trend-mini-cards\"><div class=\"trend-mini-item\"><div class=\"tm-val\">%d</div><div class=\"tm-lbl\">%d日上行总量</div></div><div class=\"trend-mini-item\"><div class=\"tm-val\">%d</div><div class=\"tm-lbl\">%d日下行总量</div></div><div class=\"trend-mini-item\"><div class=\"tm-val\">%d</div><div class=\"tm-lbl\">%d日指令总量</div></div></div>\n",
			totalInc, d.TrendDaysCount, totalOut, d.TrendDaysCount, totalCmd, d.TrendDaysCount)
		w.Write([]byte(trendMiniTmpl))

		w.Write([]byte("</div>\n"))
	}

	// ---- 5. Recent Logs + Commands + Entities (feature 1, 2, 5) ----
	w.Write([]byte("<div class=\"section\">\n  <h3>运行信息</h3>\n"))

	// Tab bar
	w.Write([]byte("<div class=\"tab-bar\">\n  <button class=\"tab-btn active\" onclick=\"switchTab('logs',this)\">近期日志</button>\n  <button class=\"tab-btn\" onclick=\"switchTab('cmds',this)\">指令列表 ("))
	w.Write([]byte(fmt.Sprintf("%d", len(d.Commands))))
	w.Write([]byte(")</button>\n  <button class=\"tab-btn\" onclick=\"switchTab('entities',this)\">活跃实体</button>\n</div>\n"))

	// Tab: Recent Logs
	w.Write([]byte("<div id=\"tab-logs\" class=\"tab-panel active\">\n"))
	w.Write([]byte("<div class=\"log-scroll\">\n"))
	if len(d.RecentLogs) > 0 {
		w.Write([]byte("<table class=\"log-table\">\n<thead><tr><th>级别</th><th>时间</th><th>消息</th><th>事件</th></tr></thead>\n<tbody>\n"))
		for _, l := range d.RecentLogs {
			levelClass := "log-lv-" + l.Level
			msg := template.HTMLEscapeString(l.Message)
			fullMsg := msg
			if len(msg) > 80 {
				msg = msg[:80] + "…"
			}
			timeStr := l.CreatedAt.Format("15:04:05")
			w.Write([]byte(fmt.Sprintf("<tr><td><span class=\"log-level %s\">%s</span></td><td>%s</td><td title=\"%s\">%s</td><td>%s</td></tr>\n",
				levelClass, l.Level, timeStr, fullMsg, msg, l.EventType)))
		}
		w.Write([]byte("</tbody></table>\n"))
	} else {
		w.Write([]byte("<p style=\"font-size:13px;color:#999\">暂无日志</p>\n"))
	}
	w.Write([]byte("</div>\n"))
	w.Write([]byte("</div>\n"))

	// Tab: Commands
	w.Write([]byte("<div id=\"tab-cmds\" class=\"tab-panel\">\n"))
	if len(d.Commands) > 0 {
		w.Write([]byte("<div style=\"max-height:450px;overflow-y:auto\">\n<div class=\"cmd-grid\">\n"))
		for _, c := range d.Commands {
			usage := ""
			if c.Usage != "" && c.Usage != c.Name {
				usage = c.Usage
			}
			aliases := ""
			if c.Aliases != "" {
				aliases = c.Aliases
			}
			w.Write([]byte(fmt.Sprintf("  <div class=\"cmd-card\"><div class=\"cmd-name\">/%s</div><div class=\"cmd-desc\">%s</div><div class=\"cmd-usage\">用法: %s &nbsp; 别名: %s</div><div class=\"cmd-perm\">%s</div></div>\n",
				c.Name, c.Description, usage, aliases, c.Permission)))
		}
		w.Write([]byte("</div>\n</div>\n"))
	} else {
		w.Write([]byte("<p style=\"font-size:13px;color:#999\">暂无注册指令</p>\n"))
	}
	w.Write([]byte("</div>\n"))

	// Tab: Active Entities
	w.Write([]byte("<div id=\"tab-entities\" class=\"tab-panel\">\n"))
	w.Write([]byte("<div class=\"entity-grid\">\n"))

	// Users
	w.Write([]byte("<h4 style=\"font-size:12px;font-weight:600;margin-bottom:6px;color:#555\">最近活跃用户</h4>\n"))
	w.Write([]byte("<div class=\"entity-scroll\" id=\"entities-users\">\n"))
	if len(d.ActiveUsers) > 0 {
		w.Write([]byte("<table class=\"entity-tbl\"><thead><tr><th>用户</th><th>名称</th><th>消息数</th><th>场景</th><th>最近</th></tr></thead><tbody>\n"))
		for _, u := range d.ActiveUsers {
			fullID := template.HTMLEscapeString(u.UserID)
			name := template.HTMLEscapeString(u.Username)
			last := relativeTime(u.LastSeenAt)
			w.Write([]byte(fmt.Sprintf("<tr><td title=\"%s\">%s</td><td>%s</td><td>%d</td><td>%s</td><td>%s</td></tr>\n",
				fullID, fullID, name, u.MessageCount, u.Scene, last)))
		}
		w.Write([]byte("</tbody></table>\n"))
	} else {
		w.Write([]byte("<p style=\"font-size:11px;color:#999\">暂无数据</p>\n"))
	}
	w.Write([]byte("</div>\n"))

	// Groups
	w.Write([]byte("<h4 style=\"font-size:12px;font-weight:600;margin-bottom:6px;color:#555\">最近活跃群聊</h4>\n"))
	w.Write([]byte("<div class=\"entity-scroll\" id=\"entities-groups\">\n"))
	if len(d.ActiveGroups) > 0 {
		w.Write([]byte("<table class=\"entity-tbl\"><thead><tr><th>群</th><th>消息数</th><th>最近</th></tr></thead><tbody>\n"))
		for _, g := range d.ActiveGroups {
			fullGID := template.HTMLEscapeString(g.GroupID)
			last := relativeTime(g.LastSeenAt)
			w.Write([]byte(fmt.Sprintf("<tr><td title=\"%s\">%s</td><td>%d</td><td>%s</td></tr>\n", fullGID, fullGID, g.MessageCount, last)))
		}
		w.Write([]byte("</tbody></table>\n"))
	} else {
		w.Write([]byte("<p style=\"font-size:11px;color:#999\">暂无数据</p>\n"))
	}
	w.Write([]byte("</div>\n"))

	// Channels
	w.Write([]byte("<h4 style=\"font-size:12px;font-weight:600;margin-bottom:6px;color:#555\">最近活跃频道</h4>\n"))
	w.Write([]byte("<div class=\"entity-scroll\" id=\"entities-channels\">\n"))
	if len(d.ActiveChannels) > 0 {
		w.Write([]byte("<table class=\"entity-tbl\"><thead><tr><th>频道</th><th>消息数</th><th>最近</th></tr></thead><tbody>\n"))
		for _, c := range d.ActiveChannels {
			fullCID := template.HTMLEscapeString(c.ChannelID)
			last := relativeTime(c.LastSeenAt)
			w.Write([]byte(fmt.Sprintf("<tr><td title=\"%s\">%s</td><td>%d</td><td>%s</td></tr>\n", fullCID, fullCID, c.MessageCount, last)))
		}
		w.Write([]byte("</tbody></table>\n"))
	} else {
		w.Write([]byte("<p style=\"font-size:11px;color:#999\">暂无数据</p>\n"))
	}
	w.Write([]byte("</div>\n"))

	w.Write([]byte("</div>\n")) // entity-grid
	w.Write([]byte("</div>\n")) // tab
	w.Write([]byte("</div>\n")) // section

	// ---- 7. Scheduled Tasks (feature 7) ----
	w.Write([]byte("<div class=\"section\">\n  <h3>定时任务</h3>\n"))
	if len(d.SchedulerTasks) > 0 {
		w.Write([]byte("  <table class=\"tbl\">\n"))
		w.Write([]byte("    <thead><tr><th>名称</th><th>调度表达式</th></tr></thead>\n<tbody>\n"))
		for _, t := range d.SchedulerTasks {
			w.Write([]byte(fmt.Sprintf("    <tr><td>%s</td><td><code>%s</code></td></tr>\n",
				template.HTMLEscapeString(t.Name),
				template.HTMLEscapeString(t.Spec))))
		}
		w.Write([]byte("  </tbody></table>\n"))
	} else {
		w.Write([]byte("  <p style=\"font-size:13px;color:#999\">暂无定时任务</p>\n"))
	}
	w.Write([]byte("</div>\n"))

	// ---- 8. DB Info (feature 8) ----
	w.Write([]byte("<div class=\"section\">\n  <h3>数据库信息</h3>\n"))
	w.Write([]byte("<div class=\"res-grid\" id=\"db-grid\">\n"))
	dbSize := fmt.Sprintf("%.1f KB", float64(d.DBFileSize)/1024)
	writeResCard(w, dbSize, "DB 文件大小")
	writeResCard(w, fmt.Sprintf("%d", d.DBLogCount), "日志条数")
	writeResCard(w, fmt.Sprintf("%d", d.DBUserCount), "用户记录")
	writeResCard(w, fmt.Sprintf("%d", d.DBGroupCount), "群聊记录")
	writeResCard(w, fmt.Sprintf("%d", d.DBChanCount), "频道记录")
	w.Write([]byte("</div>\n"))
	w.Write([]byte("<p style=\"font-size:11px;color:#999\">路径: "))
	template.HTMLEscape(w, []byte(d.DBPath))
	w.Write([]byte("</p>\n"))
	w.Write([]byte("</div>\n"))

	// ---- 9. System Info ----
	w.Write([]byte("<div class=\"section\">\n  <h3>系统信息</h3>\n  <table class=\"tbl\">\n"))
	writeInfoRow(w, "版本", d.Version)
	writeInfoRow(w, "提交", d.Commit)
	writeInfoRow(w, "构建日期", d.BuildDate)
	w.Write([]byte(fmt.Sprintf("    <tr><td>运行时长</td><td id=\"info-uptime\">%s</td></tr>\n", d.Uptime)))
	w.Write([]byte(fmt.Sprintf("    <tr><td>运行状态</td><td id=\"info-status\">%s</td></tr>\n", statusHTML(d.BotRunning, "正常", "启动中"))))
	w.Write([]byte(fmt.Sprintf("    <tr><td>WebSocket</td><td id=\"info-adapter\">%s</td></tr>\n", statusHTML(d.BotAdapter, "已连接", "未连接"))))
	w.Write([]byte(fmt.Sprintf("    <tr><td>数据库</td><td id=\"info-db\">%s</td></tr>\n", statusDBHTML(d.BotDatabase))))
	w.Write([]byte("  </table>\n</div>\n"))

	// Footer
	w.Write([]byte("<div class=\"footer\">" + d.AppName + " &mdash; &copy; 原生之旅 | mcyszl.top</div>\n"))

	w.Write([]byte("</div>\n")) // container

	// JavaScript for tabs, SSE, restart confirm
	w.Write([]byte("<script>\n"))
	// Tab switching
	w.Write([]byte("function switchTab(name, btn) {\n  document.querySelectorAll('.tab-panel').forEach(p=>p.classList.remove('active'));\n  document.querySelectorAll('.tab-btn').forEach(b=>b.classList.remove('active'));\n  document.getElementById('tab-'+name).classList.add('active');\n  btn.classList.add('active');\n}\n"))
	// SSE — auto-connect on load, resource data updates cards
	w.Write([]byte("var sse=null;var sseBtn=document.getElementById('sse-btn');\n"))
	w.Write([]byte("function statusHTML(ok,okLabel,failLabel){return ok?'<span style=\"color:#16a34a\">\u25CF '+okLabel+'</span>':'<span style=\"color:#999\">\u25CB '+failLabel+'</span>'}\n"))
	w.Write([]byte("function updateResourceCards(d){\n  var nums=document.querySelectorAll('.res-grid .res-val');\n"))
	w.Write([]byte("  if(nums.length>=7){nums[0].textContent=d.mem;nums[1].textContent=d.scpu;nums[2].textContent=d.pcpu;nums[3].textContent=d.pmem;nums[4].textContent=d.gor;nums[5].textContent=d.disk;nums[6].textContent=d.net}\n"))
	w.Write([]byte("  var ut=document.getElementById('info-uptime');if(ut)ut.textContent=d.ut;\n"))
	w.Write([]byte("  var st=document.getElementById('info-status');if(st)st.innerHTML=statusHTML(d.br,'\u6B63\u5E38','\u542F\u52A8\u4E2D');\n"))
	w.Write([]byte("  var ad=document.getElementById('info-adapter');if(ad)ad.innerHTML=statusHTML(d.ba,'\u5DF2\u8FDE\u63A5','\u672A\u8FDE\u63A5');\n"))
	w.Write([]byte("  var db=document.getElementById('info-db');if(db)db.innerHTML=d.bd?'<span style=\"color:#16a34a\">\\u25CF '+d.bd+'</span>':'<span style=\"color:#999\">\\u25CB \\u672A\\u8FDE\\u63A5</span>';\n"))
	w.Write([]byte("  // Update disk list\n  var list=document.getElementById('disk-list');\n"))
	w.Write([]byte("  if(list&&d.disks&&d.disks.length>0){\n    var html='';\n"))
	w.Write([]byte("    for(var i=0;i<d.disks.length;i++){\n      var disk=d.disks[i];\n"))
	w.Write([]byte("      html+='<div class=\"dr\" data-mount=\"'+disk.mp+'\"><span class=\"dn\" title=\"'+disk.mp+'\">'+disk.mp+'</span><span class=\"ds\">'+disk.u.toFixed(1)+' / '+disk.t.toFixed(1)+' GB</span></div>';\n"))
	w.Write([]byte("    }\n    list.innerHTML=html;\n  }\n"))
	w.Write([]byte("  var nt=document.getElementById('net-total');if(nt&&d.nett)nt.textContent='\u603B '+d.nett;\n"))
	w.Write([]byte("  var nu=document.getElementById('net-up');if(nu&&d.netup)nu.textContent=d.netup;\n"))
	w.Write([]byte("}\n"))
	w.Write([]byte("function updateTodayCards(d){\n"))
	w.Write([]byte("  var nums=document.querySelectorAll('#today-grid .num');\n"))
	w.Write([]byte("  if(nums.length>=8){nums[0].textContent=d.c2c_in;nums[1].textContent=d.c2c_out;nums[2].textContent=d.grp_in;nums[3].textContent=d.grp_out;nums[4].textContent=d.ch_in;nums[5].textContent=d.ch_out;nums[6].textContent=d.cmds;nums[7].textContent=d.interact}\n"))
	w.Write([]byte("  var subs=document.querySelectorAll('#today-grid .sub');\n"))
	w.Write([]byte("  if(subs.length>=3){subs[0].textContent=d.c2c_u+'\u6D3B\u8DC3\u7528\u6237';subs[1].textContent=d.grp_a+'\u6D3B\u8DC3\u7FA4';subs[2].textContent=d.ch_a+'\u9891\u9053'}\n"))
	w.Write([]byte("}\n"))
	w.Write([]byte("function updateDBCards(d){\n"))
	w.Write([]byte("  var nums=document.querySelectorAll('#db-grid .res-val');\n"))
	w.Write([]byte("  if(nums.length>=5){nums[0].textContent=d.fs;nums[1].textContent=d.logs;nums[2].textContent=d.users;nums[3].textContent=d.grps;nums[4].textContent=d.chans}\n"))
	w.Write([]byte("}\n"))
	w.Write([]byte("function updateEntities(d){\n"))
	w.Write([]byte("  function buildTable(rows,hasScene,label){\n    if(!rows||rows.length===0)return '<p style=\"font-size:11px;color:#999\">\u6682\u65E0\u6570\u636E</p>';\n    var html='<table class=\"entity-tbl\"><thead><tr><th>'+label+'</th>'+(hasScene?'<th>\u540D\u79F0</th>':'')+(hasScene?'<th>\u6D88\u606F\u6570</th><th>\u573A\u666F</th><th>\u6700\u8FD1</th>':'<th>\u6D88\u606F\u6570</th><th>\u6700\u8FD1</th>')+'</tr></thead><tbody>';\n"))
	w.Write([]byte("    for(var i=0;i<rows.length;i++){\n      var r=rows[i];\n"))
	w.Write([]byte("      if(hasScene)html+='<tr><td>'+r.id+'</td><td>'+(r.name||'')+'</td><td>'+r.msg+'</td><td>'+r.scene+'</td><td>'+r.last+'</td></tr>';\n"))
	w.Write([]byte("      else html+='<tr><td>'+r.id+'</td><td>'+r.msg+'</td><td>'+r.last+'</td></tr>';\n"))
	w.Write([]byte("    }\n    html+='</tbody></table>';\n    return html;\n  }\n"))
	w.Write([]byte("  var ue=document.getElementById('entities-users');if(ue)ue.innerHTML=buildTable(d.users,true,'\u7528\u6237');\n"))
	w.Write([]byte("  var ge=document.getElementById('entities-groups');if(ge)ge.innerHTML=buildTable(d.groups,false,'\u7FA4');\n"))
	w.Write([]byte("  var ce=document.getElementById('entities-channels');if(ce)ce.innerHTML=buildTable(d.channels,false,'\u9891\u9053');\n"))
	w.Write([]byte("}\n"))
	w.Write([]byte("function updateTrendChart(d){\n"))
	w.Write([]byte("  var wrap=document.getElementById('trend-chart-wrap');if(!wrap)return;\n"))
	w.Write([]byte("  var days=d.days||[];var inc=d.inc||[];var out=d.out||[];var cmd=d.cmd||[];\n"))
	w.Write([]byte("  var maxVal=1;\n"))
	w.Write([]byte("  for(var i=0;i<inc.length;i++){if(inc[i]>maxVal)maxVal=inc[i]}\n"))
	w.Write([]byte("  for(var i=0;i<out.length;i++){if(out[i]>maxVal)maxVal=out[i]}\n"))
	w.Write([]byte("  for(var i=0;i<cmd.length;i++){if(cmd[i]>maxVal)maxVal=cmd[i]}\n"))
	w.Write([]byte("  var labelStep=1;if(days.length>14)labelStep=2;if(days.length>28)labelStep=4;\n"))
	w.Write([]byte("  var html='';\n"))
	w.Write([]byte("  for(var i=0;i<days.length;i++){\n"))
	w.Write([]byte("    var incV=inc[i]||0;var outV=out[i]||0;var cmdV=cmd[i]||0;\n"))
	w.Write([]byte("    var incH=2;var outH=2;var cmdH=2;\n"))
	w.Write([]byte("    if(maxVal>0){\n"))
	w.Write([]byte("      incH=Math.max(2,Math.round(incV*100/maxVal));\n"))
	w.Write([]byte("      outH=Math.max(2,Math.round(outV*100/maxVal));\n"))
	w.Write([]byte("      cmdH=Math.max(2,Math.round(cmdV*100/maxVal));\n"))
	w.Write([]byte("    }\n"))
	w.Write([]byte("    var label=days[i];\n"))
	w.Write([]byte("    if(labelStep>1&&i%labelStep!==0&&i!==days.length-1)label='';\n"))
	w.Write([]byte("    var title=days[i]+' | 上行:'+incV+' 下行:'+outV+' 指令:'+cmdV;\n"))
	w.Write([]byte("    html+='<div class=\"chart-bar\" title=\"'+title+'\"><div class=\"chart-bar-group\"><div class=\"chart-bar-inner incoming\" title=\"上行:'+incV+'\" style=\"height:'+incH+'px\"></div><div class=\"chart-bar-inner outgoing\" title=\"下行:'+outV+'\" style=\"height:'+outH+'px\"></div><div class=\"chart-bar-inner cmds\" title=\"指令:'+cmdV+'\" style=\"height:'+cmdH+'px\"></div></div><div class=\"chart-label\">'+label+'</div></div>';\n"))
	w.Write([]byte("  }\n"))
	w.Write([]byte("  wrap.innerHTML=html;\n"))
	w.Write([]byte("  // Update mini totals\n"))
	w.Write([]byte("  var mini=document.getElementById('trend-mini-cards');\n"))
	w.Write([]byte("  if(mini){\n"))
	w.Write([]byte("    var totalInc=0,totalOut=0,totalCmd=0;\n"))
	w.Write([]byte("    for(var i=0;i<inc.length;i++)totalInc+=inc[i];\n"))
	w.Write([]byte("    for(var i=0;i<out.length;i++)totalOut+=out[i];\n"))
	w.Write([]byte("    for(var i=0;i<cmd.length;i++)totalCmd+=cmd[i];\n"))
	w.Write([]byte("    var items=mini.querySelectorAll('.tm-val');\n"))
	w.Write([]byte("    if(items.length>=3){items[0].textContent=totalInc;items[1].textContent=totalOut;items[2].textContent=totalCmd}\n"))
	w.Write([]byte("    var lbls=mini.querySelectorAll('.tm-lbl');\n"))
	w.Write([]byte("    if(lbls.length>=3){lbls[0].textContent=d.cnt+'\u65E5\u4E0A\u884C\u603B\u91CF';lbls[1].textContent=d.cnt+'\u65E5\u4E0B\u884C\u603B\u91CF';lbls[2].textContent=d.cnt+'\u65E5\u6307\u4EE4\u603B\u91CF'}\n"))
	w.Write([]byte("  }\n"))
	w.Write([]byte("}\n"))
	w.Write([]byte("function connectSSE(){\n"))
	w.Write([]byte("  sse=new EventSource('/admin/sse');\n"))
	w.Write([]byte("  function appendLogRows(logs){\n"))
	w.Write([]byte("    var tbody=document.querySelector('.log-table tbody');\n"))
	w.Write([]byte("    if(!tbody)return;\n"))
	w.Write([]byte("    for(var i=0;i<logs.length;i++){\n"))
	w.Write([]byte("      var l=logs[i];var tr=document.createElement('tr');\n"))
	w.Write([]byte("      var lvClass='log-lv-'+l.level;\n"))
	w.Write([]byte("      var msg=l.msg;var fullMsg=msg;\n"))
	w.Write([]byte("      if(msg.length>80)msg=msg.substring(0,80)+'\u2026';\n"))
	w.Write([]byte("      tr.innerHTML='<td><span class=\"log-level '+lvClass+'\">'+l.level+'</span></td><td>'+l.time+'</td><td title=\"'+fullMsg.replace(/\"/g,'&quot;')+'\">'+msg+'</td><td>'+(l.src||'')+'</td><td>'+(l.evt||'')+'</td>';\n"))
	w.Write([]byte("      tbody.insertBefore(tr,tbody.firstChild);\n"))
	w.Write([]byte("    }\n"))
	w.Write([]byte("    while(tbody.children.length>100)tbody.removeChild(tbody.lastChild);\n"))
	w.Write([]byte("  }\n"))
	w.Write([]byte("  sse.onmessage=function(e){\n    try{var d=JSON.parse(e.data);\n"))
	w.Write([]byte("      if(d.t==='resource'&&d.d)updateResourceCards(d.d);\n"))
	w.Write([]byte("      if(d.t==='today'&&d.d)updateTodayCards(d.d);\n"))
	w.Write([]byte("      if(d.t==='trend'&&d.d)updateTrendChart(d.d);\n"))
	w.Write([]byte("      if(d.t==='db'&&d.d)updateDBCards(d.d);\n"))
	w.Write([]byte("      if(d.t==='entities'&&d.d)updateEntities(d.d);\n"))
	w.Write([]byte("      if(d.t==='logs'&&d.d)appendLogRows(d.d);\n"))
	w.Write([]byte("    }catch(e2){}\n  };\n"))
	w.Write([]byte("  sse.onopen=function(){document.getElementById('sse-status').className='connected'}\n"))
	w.Write([]byte("  sse.onerror=function(){document.getElementById('sse-status').textContent='\u25CF \u5DF2\u65AD\u5F00';document.getElementById('sse-status').className=''}\n"))
	w.Write([]byte("}\n"))
	w.Write([]byte("function toggleSSE(){\n  if(sse){sse.close();sse=null;sseBtn.textContent='\u5F00\u542F\u5B9E\u65F6';document.getElementById('sse-status').textContent='\u25CF \u5DF2\u65AD\u5F00';document.getElementById('sse-status').className='';return}\n"))
	w.Write([]byte("  connectSSE();sseBtn.textContent='\u5173\u95ED\u5B9E\u65F6';document.getElementById('sse-status').textContent='\u25CF \u5DF2\u8FDE\u63A5';document.getElementById('sse-status').className='connected'\n"))
	w.Write([]byte("}\n"))
	w.Write([]byte("connectSSE(); // auto-start\n"))
	w.Write([]byte("</script>\n"))

	writeHTMLTail(w)
}

func writeStatCard(w io.Writer, num, label, sub string) {
	w.Write([]byte("    <div class=\"stat-card\">\n      <div class=\"num\">"))
	template.HTMLEscape(w, []byte(num))
	w.Write([]byte("</div>\n      <div class=\"lbl\">"))
	template.HTMLEscape(w, []byte(label))
	w.Write([]byte("</div>\n"))
	if sub != "" {
		w.Write([]byte("      <div class=\"sub\">"))
		template.HTMLEscape(w, []byte(sub))
		w.Write([]byte("</div>\n"))
	}
	w.Write([]byte("    </div>\n"))
}

func writeInfoRow(w io.Writer, key, value string) {
	w.Write([]byte("    <tr><td>"))
	template.HTMLEscape(w, []byte(key))
	w.Write([]byte("</td><td>"))
	w.Write([]byte(value))
	w.Write([]byte("</td></tr>\n"))
}

func writeResCard(w io.Writer, val, label string) {
	w.Write([]byte("<div class=\"res-card\"><div class=\"res-val\">"))
	template.HTMLEscape(w, []byte(val))
	w.Write([]byte("</div><div class=\"res-lbl\">"))
	template.HTMLEscape(w, []byte(label))
	w.Write([]byte("</div></div>\n"))
}

// formatBytes converts a byte count to a human-readable string.
func formatBytes(b uint64) string {
	switch {
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB", float64(b)/1024/1024/1024)
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/1024/1024)
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// formatRate converts bytes per second to a human-readable string.
func formatRate(bps float64) string {
	switch {
	case bps >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB/s", bps/1024/1024/1024)
	case bps >= 1024*1024:
		return fmt.Sprintf("%.1f MB/s", bps/1024/1024)
	case bps >= 1024:
		return fmt.Sprintf("%.1f KB/s", bps/1024)
	default:
		return fmt.Sprintf("%.0f B/s", bps)
	}
}

// statusHTML returns a green ● if ok, or a gray ○ if not.
func statusHTML(ok bool, okLabel, failLabel string) string {
	if ok {
		return "<span style=\"color:#16a34a\">● " + okLabel + "</span>"
	}
	return "<span style=\"color:#999\">○ " + failLabel + "</span>"
}

func statusDBHTML(driver string) string {
	if driver == "" {
		return "<span style=\"color:#999\">○ 未连接</span>"
	}
	return "<span style=\"color:#16a34a\">● " + driver + "</span>"
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60
	if days > 0 {
		return fmt.Sprintf("%d 天 %d 小时 %d 分 %d 秒", days, hours, mins, secs)
	}
	if hours > 0 {
		return fmt.Sprintf("%d 小时 %d 分 %d 秒", hours, mins, secs)
	}
	if mins > 0 {
		return fmt.Sprintf("%d 分 %d 秒", mins, secs)
	}
	return fmt.Sprintf("%d 秒", secs)
}
