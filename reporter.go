package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// SystemStats 系统资源统计信息
type SystemStats struct {
	CPUUsage      float64 `json:"cpu_usage"`      // CPU使用率百分比（估算值）
	MemoryUsage   float64 `json:"memory_usage"`   // 内存使用率百分比
	CPUCount      int     `json:"cpu_count"`      // CPU核心数
	MemoryUsageMB float64 `json:"memory_usage_mb"` // 内存使用量(MB)
	MemoryTotalMB float64 `json:"memory_total_mb"` // 内存总量(MB)
	GoroutineCount int    `json:"goroutine_count"` // 协程数量
	GCCount       uint32  `json:"gc_count"`       // GC次数
	GCPauseMs     float64 `json:"gc_pause_ms"`    // GC暂停时间(毫秒)
}

// ReportData 上报数据结构
type ReportData struct {
	Timestamp    time.Time `json:"timestamp"`
	ManagementIP string    `json:"management_ip"` // 本机管理IP标识
	TotalStats   struct {
		BytesSent     int64   `json:"bytes_sent"`
		PacketsSent   int64   `json:"packets_sent"`
		BandwidthMbps float64 `json:"bandwidth_mbps"`
		UptimeSeconds float64 `json:"uptime_seconds"`
	} `json:"total_stats"`
	SourceIPStats map[string]*SourceStats `json:"source_ip_stats"`
	TargetStats   map[string]*TargetStats `json:"target_stats"`
	SystemStats   SystemStats             `json:"system_stats"`
}

// Reporter 监控上报器
type Reporter struct {
	interval     time.Duration
	stats        *Stats
	logger       *logrus.Logger
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	startTime    time.Time
	reportURL    string // 完整的上报URL
	httpClient   *http.Client
	managementIP string // 管理IP
	
	// CPU统计缓存（减少计算开销）
	lastCPUTime   time.Time
	lastCPUStats  SystemStats
	cpuCacheMutex sync.RWMutex
}

// NewReporter 创建新的监控上报器
// :param config: 上报配置
// :param stats: 统计信息
// :param logger: 日志记录器
// :param managementIP: 管理IP
// :return: 监控上报器实例
func NewReporter(config Report, stats *Stats, logger *logrus.Logger, managementIP string) *Reporter {
	ctx, cancel := context.WithCancel(context.Background())

	// 设置默认间隔为10分钟
	interval := time.Duration(config.Interval) * time.Second
	if interval <= 0 {
		interval = 10 * time.Minute
	}

	// 创建HTTP客户端，设置超时时间
	httpClient := &http.Client{
		Timeout: 3 * time.Second, // 3秒超时，快速失败不阻塞打流
	}

	return &Reporter{
		interval:     interval,
		stats:        stats,
		logger:       logger,
		ctx:          ctx,
		cancel:       cancel,
		startTime:    time.Now(),
		reportURL:    config.URL,
		httpClient:   httpClient,
		managementIP: managementIP,
	}
}

// Start 启动监控上报
func (r *Reporter) Start() {
	r.wg.Add(1)
	go r.reportLoop()

	if r.reportURL != "" {
		r.logger.Infof("📊 监控上报器已启动，间隔: %v，URL: %s，管理IP: %s", r.interval, r.reportURL, r.managementIP)
	} else {
		r.logger.Infof("📊 监控上报器已启动，间隔: %v（仅本地日志），管理IP: %s", r.interval, r.managementIP)
	}
}

// Stop 停止监控上报
func (r *Reporter) Stop() {
	r.cancel()
	r.wg.Wait()
	r.logger.Info("📊 监控上报器已停止")
}

// reportLoop 上报循环
func (r *Reporter) reportLoop() {
	defer r.wg.Done()

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.generateReport()
		}
	}
}

// generateReport 生成并输出监控报告
func (r *Reporter) generateReport() {
	r.stats.mu.RLock()
	defer r.stats.mu.RUnlock()

	// 计算运行时间
	uptime := time.Since(r.startTime).Seconds()

	// 计算整体带宽
	var totalBandwidth float64
	if uptime > 0 {
		totalBandwidth = float64(r.stats.bytesSent*8) / (uptime * 1000000)
	}

	// 收集系统资源信息
	systemStats := r.collectSystemStats()

	// 更新每个源IP的带宽统计
	sourceIPStats := make(map[string]*SourceStats)
	for ip, stats := range r.stats.sourceIPStats {
		// 计算该IP的带宽
		var ipBandwidth float64
		if uptime > 0 && stats.BytesSent > 0 {
			ipBandwidth = float64(stats.BytesSent*8) / (uptime * 1000000)
		}

		sourceIPStats[ip] = &SourceStats{
			BytesSent:     stats.BytesSent,
			PacketsSent:   stats.PacketsSent,
			BandwidthMbps: ipBandwidth,
			LastActive:    stats.LastActive,
		}
	}

	// 创建上报数据
	reportData := ReportData{
		Timestamp:    time.Now(),
		ManagementIP: r.managementIP,
		TotalStats: struct {
			BytesSent     int64   `json:"bytes_sent"`
			PacketsSent   int64   `json:"packets_sent"`
			BandwidthMbps float64 `json:"bandwidth_mbps"`
			UptimeSeconds float64 `json:"uptime_seconds"`
		}{
			BytesSent:     r.stats.bytesSent,
			PacketsSent:   r.stats.packetsSent,
			BandwidthMbps: totalBandwidth,
			UptimeSeconds: uptime,
		},
		SourceIPStats: sourceIPStats,
		TargetStats:   r.stats.targetStats,
		SystemStats:   systemStats,
	}

	// 转换为JSON格式
	jsonData, err := json.MarshalIndent(reportData, "", "  ")
	if err != nil {
		r.logger.Errorf("生成监控报告失败: %v", err)
		return
	}

	// 异步发送到远程监控系统（不阻塞主流程）
	if r.reportURL != "" {
		go r.sendToRemoteAsync(jsonData) // 使用goroutine异步发送
	}

	// 输出监控报告
	r.logger.Infof("📈 监控报告:")
	r.logger.Infof("总发送: %s (%s包) | 带宽: %.2f Mbps | 运行: %.1fs",
		formatBytes(reportData.TotalStats.BytesSent),
		formatNumber(reportData.TotalStats.PacketsSent),
		reportData.TotalStats.BandwidthMbps,
		reportData.TotalStats.UptimeSeconds)

	// 输出源IP统计
	for ip, stats := range sourceIPStats {
		r.logger.Infof("源IP [%s]: %s | %.2f Mbps | %s包",
			ip,
			formatBytes(stats.BytesSent),
			stats.BandwidthMbps,
			formatNumber(stats.PacketsSent))
	}

	// 输出系统资源统计
	r.logger.Infof("系统资源: CPU: %.1f%% | 内存: %.1f%% (%.0f/%.0fMB) | 协程: %d | GC: %d次",
		systemStats.CPUUsage,
		systemStats.MemoryUsage,
		systemStats.MemoryUsageMB,
		systemStats.MemoryTotalMB,
		systemStats.GoroutineCount,
		systemStats.GCCount)

	// 可以在这里添加发送到远程监控系统的逻辑
	// 例如: r.sendToRemote(jsonData)
}

// collectSystemStats 收集Linux系统整体资源统计信息
func (r *Reporter) collectSystemStats() SystemStats {
	// 使用缓存减少计算开销（30秒缓存）
	r.cpuCacheMutex.RLock()
	if time.Since(r.lastCPUTime) < 30*time.Second {
		stats := r.lastCPUStats
		r.cpuCacheMutex.RUnlock()
		return stats
	}
	r.cpuCacheMutex.RUnlock()
	
	// 获取系统整体CPU使用率
	cpuUsage := r.getSystemCPUUsage()
	
	// 获取系统整体内存使用率
	memUsage, memTotal, memUsed := r.getSystemMemoryUsage()
	
	// 获取Go进程相关信息
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	
	stats := SystemStats{
		CPUUsage:       cpuUsage,
		MemoryUsage:    memUsage,
		CPUCount:       runtime.NumCPU(),
		MemoryUsageMB:  memUsed,
		MemoryTotalMB:  memTotal,
		GoroutineCount: runtime.NumGoroutine(),
		GCCount:        memStats.NumGC,
		GCPauseMs:      float64(memStats.PauseNs[(memStats.NumGC+255)%256]) / 1000000,
	}
	
	// 更新缓存
	r.cpuCacheMutex.Lock()
	r.lastCPUTime = time.Now()
	r.lastCPUStats = stats
	r.cpuCacheMutex.Unlock()
	
	return stats
}

// getSystemCPUUsage 获取Linux系统整体CPU使用率
func (r *Reporter) getSystemCPUUsage() float64 {
	// 读取/proc/stat获取CPU时间
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		r.logger.Debugf("读取/proc/stat失败: %v", err)
		return 0.0
	}
	
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return 0.0
	}
	
	// 解析第一行: cpu user nice system idle iowait irq softirq steal guest guest_nice
	fields := strings.Fields(lines[0])
	if len(fields) < 5 {
		return 0.0
	}
	
	user, _ := strconv.ParseFloat(fields[1], 64)
	nice, _ := strconv.ParseFloat(fields[2], 64)
	system, _ := strconv.ParseFloat(fields[3], 64)
	idle, _ := strconv.ParseFloat(fields[4], 64)
	iowait := 0.0
	if len(fields) > 5 {
		iowait, _ = strconv.ParseFloat(fields[5], 64)
	}
	
	// 计算CPU使用率
	total := user + nice + system + idle + iowait
	if total == 0 {
		return 0.0
	}
	
	used := user + nice + system + iowait
	cpuUsage := (used / total) * 100.0
	
	return cpuUsage
}

// getSystemMemoryUsage 获取Linux系统整体内存使用率
func (r *Reporter) getSystemMemoryUsage() (usage float64, totalMB float64, usedMB float64) {
	// 读取/proc/meminfo
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		r.logger.Debugf("读取/proc/meminfo失败: %v", err)
		return 0.0, 0.0, 0.0
	}
	
	var memTotal, memFree, memAvailable, buffers, cached float64
	
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		
		switch fields[0] {
		case "MemTotal:":
			memTotal = value
		case "MemFree:":
			memFree = value
		case "MemAvailable:":
			memAvailable = value
		case "Buffers:":
			buffers = value
		case "Cached:":
			cached = value
		}
	}
	
	if memTotal == 0 {
		return 0.0, 0.0, 0.0
	}
	
	// 计算内存使用率
	// 如果有MemAvailable，使用它；否则用MemFree + Buffers + Cached
	var memUsed float64
	if memAvailable > 0 {
		memUsed = memTotal - memAvailable
	} else {
		memUsed = memTotal - memFree - buffers - cached
	}
	
	usage = (memUsed / memTotal) * 100.0
	totalMB = memTotal / 1024.0  // KB转MB
	usedMB = memUsed / 1024.0    // KB转MB
	
	return usage, totalMB, usedMB
}

// sendToRemote 发送数据到远程监控系统
// sendToRemoteAsync 异步发送数据到远程监控系统（不阻塞主线程）
func (r *Reporter) sendToRemoteAsync(data []byte) {
	// 设置超时时间，防止无限等待
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	// 创建请求
	req, err := http.NewRequestWithContext(ctx, "POST", r.reportURL, bytes.NewBuffer(data))
	if err != nil {
		r.logger.Debugf("创建监控请求失败: %v", err)
		return
	}
	
	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "UDP-Shooter/1.0")
	
	// 发送请求（带超时）
	resp, err := r.httpClient.Do(req)
	if err != nil {
		// 只记录debug日志，不影响主功能
		r.logger.Debugf("监控数据推送失败: %v", err)
		return
	}
	defer resp.Body.Close()
	
	// 检查响应状态
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		r.logger.Debugf("监控数据推送成功: %s", r.reportURL)
	} else {
		r.logger.Debugf("监控数据推送状态码: %d", resp.StatusCode)
	}
}
