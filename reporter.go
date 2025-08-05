package main

import (
	"context"
	"encoding/json"
	"runtime"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// SystemStats 系统资源统计信息
type SystemStats struct {
	CPUCount      int     `json:"cpu_count"`
	MemoryUsageMB float64 `json:"memory_usage_mb"`
	MemoryTotalMB float64 `json:"memory_total_mb"`
	GoroutineCount int    `json:"goroutine_count"`
	GCCount       uint32  `json:"gc_count"`
	GCPauseMs     float64 `json:"gc_pause_ms"`
}

// ReportData 上报数据结构
type ReportData struct {
	Timestamp     time.Time                  `json:"timestamp"`
	TotalStats    struct {
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
	interval  time.Duration
	stats     *Stats
	logger    *logrus.Logger
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	startTime time.Time
}

// NewReporter 创建新的监控上报器
// :param config: 上报配置
// :param stats: 统计信息
// :param logger: 日志记录器
// :return: 监控上报器实例
func NewReporter(config Report, stats *Stats, logger *logrus.Logger) *Reporter {
	ctx, cancel := context.WithCancel(context.Background())
	
	// 设置默认间隔为10分钟
	interval := time.Duration(config.Interval) * time.Second
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	
	return &Reporter{
		interval:  interval,
		stats:     stats,
		logger:    logger,
		ctx:       ctx,
		cancel:    cancel,
		startTime: time.Now(),
	}
}

// Start 启动监控上报
func (r *Reporter) Start() {
	r.wg.Add(1)
	go r.reportLoop()
	r.logger.Infof("📊 监控上报器已启动，间隔: %v", r.interval)
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
		Timestamp: time.Now(),
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
	
	// 可以在这里添加发送到远程监控系统的逻辑
	// 例如: r.sendToRemote(jsonData)
	_ = jsonData // 暂时忽略未使用的变量
	
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
	r.logger.Infof("系统资源: CPU核心: %d | 内存: %.1f/%.1f MB | 协程: %d | GC: %d次",
		systemStats.CPUCount,
		systemStats.MemoryUsageMB,
		systemStats.MemoryTotalMB,
		systemStats.GoroutineCount,
		systemStats.GCCount)
	
	// 可以在这里添加发送到远程监控系统的逻辑
	// 例如: r.sendToRemote(jsonData)
}

// collectSystemStats 收集系统资源统计信息
func (r *Reporter) collectSystemStats() SystemStats {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	
	return SystemStats{
		CPUCount:       runtime.NumCPU(),
		MemoryUsageMB:  float64(memStats.Alloc) / 1024 / 1024,
		MemoryTotalMB:  float64(memStats.Sys) / 1024 / 1024,
		GoroutineCount: runtime.NumGoroutine(),
		GCCount:        memStats.NumGC,
		GCPauseMs:      float64(memStats.PauseNs[memStats.NumGC%256]) / 1000000,
	}
}

// sendToRemote 发送数据到远程监控系统（可选实现）
func (r *Reporter) sendToRemote(data []byte) {
	// 这里可以实现发送到远程监控系统的逻辑
	// 例如通过HTTP POST或其他协议发送数据
	r.logger.Debug("发送监控数据到远程系统")
}