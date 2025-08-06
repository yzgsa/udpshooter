package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/sirupsen/logrus"
	"net/http"
	"runtime"
	"sync"
	"time"
)

// SystemStats 系统资源统计信息
type SystemStats struct {
	CPUUsage       float64 `json:"cpu_usage"`        // CPU使用率百分比
	MemoryUsage    float64 `json:"memory_usage"`     // 内存使用率百分比
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
	reportURL    string      // 完整的上报URL
	httpClient   *http.Client
	managementIP string      // 管理IP
	cpuMonitor   *CPUMonitor // CPU监控器
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
		Timeout: 30 * time.Second,
	}

	// 创建CPU监控器
	cpuMonitor := NewCPUMonitor()

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
		cpuMonitor:   cpuMonitor,
	}
}

// Start 启动监控上报
func (r *Reporter) Start() {
	// 启动CPU监控
	r.cpuMonitor.Start()
	
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
	// 停止CPU监控
	r.cpuMonitor.Stop()
	
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

	// 发送到远程监控系统
	if r.reportURL != "" {
		r.sendToRemote(jsonData)
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
	r.logger.Infof("系统资源: CPU: %.1f%% | 内存: %.1f%%",
		systemStats.CPUUsage,
		systemStats.MemoryUsage)

	// 可以在这里添加发送到远程监控系统的逻辑
	// 例如: r.sendToRemote(jsonData)
}

// collectSystemStats 收集系统资源统计信息
func (r *Reporter) collectSystemStats() SystemStats {
	return SystemStats{
		CPUUsage:    r.cpuMonitor.GetCPUUsage(),
		MemoryUsage: r.cpuMonitor.GetMemoryUsage(),
	}
}

// sendToRemote 发送数据到远程监控系统
func (r *Reporter) sendToRemote(data []byte) {
	// 创建HTTP请求
	req, err := http.NewRequestWithContext(r.ctx, "POST", r.reportURL, bytes.NewBuffer(data))
	if err != nil {
		r.logger.Errorf("创建HTTP请求失败: %v", err)
		return
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "UDP-Shooter/1.0")

	// 发送请求
	resp, err := r.httpClient.Do(req)
	if err != nil {
		r.logger.Errorf("发送监控数据失败: %v", err)
		return
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		r.logger.Debugf("监控数据发送成功: %s (状态码: %d)", r.reportURL, resp.StatusCode)
	} else {
		r.logger.Warnf("监控数据发送异常: %s (状态码: %d)", r.reportURL, resp.StatusCode)
	}
}
