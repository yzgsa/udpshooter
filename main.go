package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Target 目标配置结构体
type Target struct {
	Host string `json:"host"` // 支持IPv4和IPv6
	Port int    `json:"port"`
}

// Schedule 调度配置结构体
type Schedule struct {
	ID           string `json:"id"`
	StartTime    string `json:"start_time"`    // 格式: "HH:MM:SS"
	EndTime      string `json:"end_time"`      // 格式: "HH:MM:SS"
	Repeat       string `json:"repeat"`        // "once", "daily", "weekdays"
	BandwidthMbps int64 `json:"bandwidth_mbps"` // 调度期间的带宽限制，单位 Mbps
	Timezone     string `json:"timezone"`      // 时区，可选
}

// Report 上报配置结构体
type Report struct {
	Interval int    `json:"interval"` // 上报间隔，单位秒，默认600秒(10分钟)
	URL      string `json:"url"`      // 完整的上报URL，例如: "http://localhost:8080/api/udpshooter/monitor/"
}

// Config 配置结构体
type Config struct {
	ConfigMode   string     `json:"config_mode,omitempty"` // "local" 或 "remote"
	ManagementIP string     `json:"management_ip"`         // 本地管理IP，作为本机标识
	Targets      []Target   `json:"targets"`
	Bandwidth    struct {
		MaxBandwidthMbps int64 `json:"max_bandwidth_mbps"`
		MaxBytes         int64 `json:"max_bytes"`
	} `json:"bandwidth"`
	SourceIPs   []string   `json:"source_ips"`
	Packet      struct {
		Size           int    `json:"size"`
		PayloadPattern string `json:"payload_pattern"`
	} `json:"packet"`
	Concurrency struct {
		WorkersPerIP int `json:"workers_per_ip"`
		BufferSize   int `json:"buffer_size"`
	} `json:"concurrency"`
	Logging     struct {
		Level      string `json:"level"`
		File       string `json:"file"`
		MaxSizeMB  int    `json:"max_size_mb"`
		MaxBackups int    `json:"max_backups"`
		MaxAgeDays int    `json:"max_age_days"`
		Compress   bool   `json:"compress"`
	} `json:"logging"`
	Report    Report     `json:"report"`
	Schedules []Schedule `json:"schedules"`
}

// UDPShooter UDP打流器结构体
type UDPShooter struct {
	config           *Config
	logger           *logrus.Logger
	stats            *Stats
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	startTime        time.Time
	totalBytes       int64
	packetPool       *OptimizedPacketPool
	networkOptimizer *NetworkOptimizer
	scheduler        *Scheduler
	reporter         *Reporter
	statsChan        chan StatUpdate // 统计信息更新通道
	currentBandwidth int64           // 当前调度的带宽限制，单位 Mbps
	bandwidthMu      sync.RWMutex    // 带宽配置读写锁
}

// StatUpdate 统计更新信息结构体
type StatUpdate struct {
	SourceIP     string
	BytesSent    int64
	PacketsSent  int64
	TargetAddrs  []*net.UDPAddr
	PacketSize   int
}

// Stats 统计信息结构体
type Stats struct {
	mu               sync.RWMutex
	bytesSent        int64
	packetsSent      int64
	startTime        time.Time
	lastLogTime      time.Time
	bandwidthMbps    float64
	sourceIPStats    map[string]*SourceStats // 每个源IP的统计信息
	targetStats      map[string]*TargetStats // 每个目标的连接状态
}

// SourceStats 源IP统计信息
type SourceStats struct {
	BytesSent     int64   `json:"bytes_sent"`
	PacketsSent   int64   `json:"packets_sent"`
	BandwidthMbps float64 `json:"bandwidth_mbps"`
	LastActive    time.Time `json:"last_active"`
}

// TargetStats 目标统计信息
type TargetStats struct {
	Connected    bool      `json:"connected"`
	BytesSent    int64     `json:"bytes_sent"`
	PacketsSent  int64     `json:"packets_sent"`
	LastPingTime time.Time `json:"last_ping_time"`
	ResponseTime float64   `json:"response_time"` // 毫秒
}

// NewUDPShooter 创建新的UDP打流器实例
// :param config: 配置信息
// :param logger: 日志记录器
// :return: UDP打流器实例
func NewUDPShooter(config *Config, logger *logrus.Logger) *UDPShooter {
	ctx, cancel := context.WithCancel(context.Background())
	
	// 初始化统计信息
	stats := &Stats{
		startTime:     time.Now(),
		lastLogTime:   time.Now(),
		sourceIPStats: make(map[string]*SourceStats),
		targetStats:   make(map[string]*TargetStats),
	}
	
	// 为每个源IP初始化统计信息
	for _, sourceIP := range config.SourceIPs {
		stats.sourceIPStats[sourceIP] = &SourceStats{
			LastActive: time.Now(),
		}
	}
	
	// 为每个目标初始化统计信息
	for _, target := range config.Targets {
		targetKey := fmt.Sprintf("%s:%d", target.Host, target.Port)
		stats.targetStats[targetKey] = &TargetStats{
			Connected: false,
		}
	}
	
	shooter := &UDPShooter{
		config:           config,
		logger:           logger,
		stats:            stats,
		ctx:              ctx,
		cancel:           cancel,
		startTime:        time.Now(),
		packetPool:       NewOptimizedPacketPool(),
		networkOptimizer: NewNetworkOptimizer(),
		statsChan:        make(chan StatUpdate, 10000), // 缓冲大小10000，防止阻塞
		currentBandwidth: config.Bandwidth.MaxBandwidthMbps, // 默认使用配置文件中的带宽
	}
	
	// 初始化调度器
	shooter.scheduler = NewScheduler(config.Schedules, logger)
	
	// 初始化上报器
	shooter.reporter = NewReporter(config.Report, stats, logger, config.ManagementIP)
	
	return shooter
}

// loadConfig 加载配置文件
// :param filename: 配置文件路径
// :return: 配置结构体和错误信息
func loadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %v", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %v", err)
	}

	return &config, nil
}

// setupLogger 设置日志记录器
// :param config: 日志配置
// :return: 日志记录器
func setupLogger(config *Config) *logrus.Logger {
	logger := logrus.New()

	// 设置日志级别
	level, err := logrus.ParseLevel(config.Logging.Level)
	if err != nil {
		level = logrus.InfoLevel
	}
	logger.SetLevel(level)

	// 设置日志格式
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
		ForceColors:     true,
	})

	// 创建多输出writer，同时输出到文件和控制台
	fileWriter := &lumberjack.Logger{
		Filename:   config.Logging.File,
		MaxSize:    config.Logging.MaxSizeMB,
		MaxBackups: config.Logging.MaxBackups,
		MaxAge:     config.Logging.MaxAgeDays,
		Compress:   config.Logging.Compress,
	}

	// 同时输出到文件和控制台
	logger.SetOutput(io.MultiWriter(os.Stdout, fileWriter))

	return logger
}

// createPacket 创建UDP数据包
// :param size: 数据包大小
// :param pattern: 负载模式
// :return: 数据包字节数组
func createPacket(size int, pattern string) []byte {
	packet := make([]byte, size)

	// 填充模式字符串
	patternBytes := []byte(pattern)
	for i := 0; i < size; i++ {
		packet[i] = patternBytes[i%len(patternBytes)]
	}

	return packet
}

// sendPackets 发送数据包的工作协程
// :param sourceIP: 源IP地址
// :param targetAddrs: 目标地址列表
// :param packetTemplate: 数据包模板
// :param rateLimiter: 速率限制器
func (u *UDPShooter) sendPackets(sourceIP string, targetAddrs []*net.UDPAddr, packetTemplate []byte, rateLimiter *RateLimiter) {
	defer u.wg.Done()

	// 为每个目标创建UDP连接
	connections := make([]*net.UDPConn, len(targetAddrs))
	for i, targetAddr := range targetAddrs {
		conn, err := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP(sourceIP)}, targetAddr)
		if err != nil {
			u.logger.Errorf("创建UDP连接失败 [%s] -> %s: %v", sourceIP, targetAddr.String(), err)
			
			// 更新目标连接状态
			targetKey := fmt.Sprintf("%s:%d", targetAddr.IP.String(), targetAddr.Port)
			u.stats.mu.Lock()
			if targetStats, exists := u.stats.targetStats[targetKey]; exists {
				targetStats.Connected = false
			}
			u.stats.mu.Unlock()
			continue
		}

		// 设置发送缓冲区大小
		if err := conn.SetWriteBuffer(u.config.Concurrency.BufferSize); err != nil {
			u.logger.Warnf("设置发送缓冲区失败 [%s] -> %s: %v", sourceIP, targetAddr.String(), err)
		}

		connections[i] = conn
		defer conn.Close()
		
		// 更新目标连接状态
		targetKey := fmt.Sprintf("%s:%d", targetAddr.IP.String(), targetAddr.Port)
		u.stats.mu.Lock()
		if targetStats, exists := u.stats.targetStats[targetKey]; exists {
			targetStats.Connected = true
			targetStats.LastPingTime = time.Now()
		}
		u.stats.mu.Unlock()
	}

	if len(connections) == 0 {
		u.logger.Errorf("没有可用的UDP连接 [%s]", sourceIP)
		return
	}

	u.logger.Infof("📤 开始发送 | 源IP: %s | 目标: %d个", sourceIP, len(connections))

	// 创建批量写入器
	batchWriter := NewBatchWriter(connections, 10)

	// 预分配数据包缓冲区
	packetSize := len(packetTemplate)
	packet := u.packetPool.GetPacket(packetSize)
	defer u.packetPool.PutPacket(packet)

	for {
		select {
		case <-u.ctx.Done():
			return
		default:
			// 速率限制 - 每次发送需要消费 packetSize * targetCount 的令牌
			if rateLimiter != nil {
				// 计算实际发送的总字节数（数据包大小 × 目标数量）
				totalBytes := packetSize * len(connections)
				rateLimiter.WaitBytes(totalBytes)
			}

			// 快速复制数据包模板
			copy(packet, packetTemplate)

			// 批量发送到所有目标
			batchWriter.WriteSingle(packet)

			// 更新统计信息（高性能原子操作）
			// 实际发送的字节数 = 数据包大小 × 目标数量
			actualBytesSent := int64(packetSize * len(connections))
			u.stats.mu.Lock()
			u.stats.bytesSent += actualBytesSent
			u.stats.packetsSent++
			u.stats.mu.Unlock()
			
			// 批量更新详细统计（异步，减少热路径延迟）
			select {
			case u.statsChan <- StatUpdate{
				SourceIP: sourceIP,
				BytesSent: actualBytesSent,
				PacketsSent: 1,
				TargetAddrs: targetAddrs,
				PacketSize: packetSize,
			}:
			default:
				// 如果通道满了就跳过详细统计，不阻塞发送
			}
		}
	}
}

// processStatsUpdates 异步处理统计信息更新
func (u *UDPShooter) processStatsUpdates() {
	defer u.wg.Done()
	
	// 批量处理缓冲
	updateBuffer := make([]StatUpdate, 0, 1000)
	ticker := time.NewTicker(100 * time.Millisecond) // 100ms批量更新一次
	defer ticker.Stop()
	
	for {
		select {
		case <-u.ctx.Done():
			// 在退出前处理剩余的更新
			u.processBatchUpdates(updateBuffer)
			return
			
		case update := <-u.statsChan:
			updateBuffer = append(updateBuffer, update)
			// 如果缓冲满了就立即处理
			if len(updateBuffer) >= 1000 {
				u.processBatchUpdates(updateBuffer)
				updateBuffer = updateBuffer[:0] // 重置缓冲
			}
			
		case <-ticker.C:
			// 定期处理缓冲中的更新
			if len(updateBuffer) > 0 {
				u.processBatchUpdates(updateBuffer)
				updateBuffer = updateBuffer[:0] // 重置缓冲
			}
		}
	}
}

// processBatchUpdates 批量处理统计更新
func (u *UDPShooter) processBatchUpdates(updates []StatUpdate) {
	if len(updates) == 0 {
		return
	}
	
	// 按源IP聚合统计
	sourceStatsMap := make(map[string]*SourceStats)
	targetStatsMap := make(map[string]*TargetStats)
	
	for _, update := range updates {
		// 聚合源IP统计
		if sourceStats, exists := sourceStatsMap[update.SourceIP]; exists {
			sourceStats.BytesSent += update.BytesSent
			sourceStats.PacketsSent += update.PacketsSent
		} else {
			sourceStatsMap[update.SourceIP] = &SourceStats{
				BytesSent:   update.BytesSent,
				PacketsSent: update.PacketsSent,
				LastActive:  time.Now(), // 只在这里调用一次time.Now()
			}
		}
		
		// 聚合目标统计
		for _, targetAddr := range update.TargetAddrs {
			targetKey := fmt.Sprintf("%s:%d", targetAddr.IP.String(), targetAddr.Port)
			if targetStats, exists := targetStatsMap[targetKey]; exists {
				targetStats.BytesSent += int64(update.PacketSize)
				targetStats.PacketsSent += update.PacketsSent
			} else {
				targetStatsMap[targetKey] = &TargetStats{
					BytesSent:    int64(update.PacketSize) * update.PacketsSent,
					PacketsSent: update.PacketsSent,
				}
			}
		}
	}
	
	// 一次性更新所有统计信息
	u.stats.mu.Lock()
	for sourceIP, stats := range sourceStatsMap {
		if existingStats, exists := u.stats.sourceIPStats[sourceIP]; exists {
			existingStats.BytesSent += stats.BytesSent
			existingStats.PacketsSent += stats.PacketsSent
			existingStats.LastActive = stats.LastActive
		}
	}
	
	for targetKey, stats := range targetStatsMap {
		if existingStats, exists := u.stats.targetStats[targetKey]; exists {
			existingStats.BytesSent += stats.BytesSent
			existingStats.PacketsSent += stats.PacketsSent
		}
	}
	u.stats.mu.Unlock()
}

// logStats 记录统计信息
func (u *UDPShooter) logStats() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-u.ctx.Done():
			return
		case <-ticker.C:
			u.stats.mu.Lock()
			elapsed := time.Since(u.stats.startTime).Seconds()
			if elapsed > 0 {
				u.stats.bandwidthMbps = float64(u.stats.bytesSent*8) / (elapsed * 1000000)
			}

			// 格式化字节数显示
			bytesStr := formatBytes(u.stats.bytesSent)
			packetsStr := formatNumber(u.stats.packetsSent)

			u.logger.Infof("📊 统计信息 | 发送: %s (%s包) | 带宽: %.2f Mbps | 运行: %.1fs",
				bytesStr, packetsStr, u.stats.bandwidthMbps, elapsed)

			u.stats.lastLogTime = time.Now()
			u.stats.mu.Unlock()
		}
	}
}

// GetCurrentBandwidth 获取当前有效带宽限制
func (u *UDPShooter) GetCurrentBandwidth() int64 {
	u.bandwidthMu.RLock()
	defer u.bandwidthMu.RUnlock()
	return u.currentBandwidth
}

// SetCurrentBandwidth 设置当前有效带宽限制
func (u *UDPShooter) SetCurrentBandwidth(bandwidth int64) {
	u.bandwidthMu.Lock()
	defer u.bandwidthMu.Unlock()
	u.currentBandwidth = bandwidth
	u.logger.Infof("🔧 带宽限制已更新为: %d Mbps", bandwidth)
}

// formatBytes 格式化字节数显示
// :param bytes: 字节数
// :return: 格式化后的字符串
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// formatNumber 格式化数字显示
// :param num: 数字
// :return: 格式化后的字符串
func formatNumber(num int64) string {
	if num < 1000 {
		return fmt.Sprintf("%d", num)
	}
	if num < 1000000 {
		return fmt.Sprintf("%.1fK", float64(num)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(num)/1000000)
}

// Start 启动UDP打流器
func (u *UDPShooter) Start() error {
	// 启动调度器
	if u.scheduler != nil {
		// 设置调度器回调函数
		u.scheduler.SetCallback(u.onScheduleCallback)
		u.scheduler.Start()
		
		// 如果有调度任务，等待调度器启动打流
		if len(u.config.Schedules) > 0 {
			u.logger.Info("⏰ 等待调度任务启动...")
			return nil
		}
	}
	
	// 没有调度任务，直接启动打流
	return u.startShooting()
}

// onScheduleCallback 调度器回调函数
func (u *UDPShooter) onScheduleCallback(start bool, bandwidth int64) {
	if start {
		u.logger.Infof("🚀 调度器启动打流... (带宽限制: %d Mbps)", bandwidth)
		// 设置当前带宽限制
		u.SetCurrentBandwidth(bandwidth)
		if err := u.startShooting(); err != nil {
			u.logger.Errorf("调度启动打流失败: %v", err)
		}
	} else {
		u.logger.Info("⏹️ 调度器停止打流...")
		u.stopShooting()
	}
}

// startShooting 启动打流
func (u *UDPShooter) startShooting() error {
	// 启动异步统计更新处理协程
	u.wg.Add(1)
	go u.processStatsUpdates()
	
	// 分别解析IPv4和IPv6目标地址
	var ipv4Targets []*net.UDPAddr
	var ipv6Targets []*net.UDPAddr

	for _, target := range u.config.Targets {
		// 处理IPv6地址格式
		host := target.Host
		if strings.Contains(host, ":") && !strings.Contains(host, "[") {
			// IPv6地址需要加方括号
			host = "[" + host + "]"
		}

		addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", host, target.Port))
		if err != nil {
			u.logger.Warnf("解析目标地址失败 %s:%d: %v", target.Host, target.Port, err)
			continue
		}

		// 根据IP类型分类
		if addr.IP.To4() != nil {
			ipv4Targets = append(ipv4Targets, addr)
		} else {
			ipv6Targets = append(ipv6Targets, addr)
		}
	}

	// 分别处理源IP地址
	var ipv4SourceIPs []string
	var ipv6SourceIPs []string

	for _, sourceIP := range u.config.SourceIPs {
		parsedIP := net.ParseIP(sourceIP)
		if parsedIP == nil {
			u.logger.Warnf("无效的源IP地址: %s", sourceIP)
			continue
		}

		if parsedIP.To4() != nil {
			ipv4SourceIPs = append(ipv4SourceIPs, sourceIP)
		} else {
			ipv6SourceIPs = append(ipv6SourceIPs, sourceIP)
		}
	}

	// 创建优化的数据包模板
	packetTemplate := u.networkOptimizer.GetPacketTemplate(u.config.Packet.Size, u.config.Packet.PayloadPattern)

	// 启动统计日志协程
	u.wg.Add(1)
	go u.logStats()
	
	// 启动监控上报器
	if u.reporter != nil {
		u.reporter.Start()
	}

	// 启动IPv4打流
	if len(ipv4Targets) > 0 && len(ipv4SourceIPs) > 0 {
		u.startIPv4Shooter(ipv4SourceIPs, ipv4Targets, packetTemplate)
	}

	// 启动IPv6打流
	if len(ipv6Targets) > 0 && len(ipv6SourceIPs) > 0 {
		u.startIPv6Shooter(ipv6SourceIPs, ipv6Targets, packetTemplate)
	}

	// 检查是否有有效的配置
	totalTargets := len(ipv4Targets) + len(ipv6Targets)
	totalSourceIPs := len(ipv4SourceIPs) + len(ipv6SourceIPs)

	if totalTargets == 0 {
		return fmt.Errorf("没有有效的目标地址")
	}
	if totalSourceIPs == 0 {
		return fmt.Errorf("没有有效的源IP地址")
	}

	u.logger.Infof("🚀 UDP打流器已启动 | IPv4目标: %d个 | IPv6目标: %d个 | IPv4源IP: %d个 | IPv6源IP: %d个",
		len(ipv4Targets), len(ipv6Targets), len(ipv4SourceIPs), len(ipv6SourceIPs))

	return nil
}

// stopShooting 停止打流
func (u *UDPShooter) stopShooting() {
	u.logger.Info("⏹️ 停止UDP打流...")
	u.cancel()
	
	// 停止监控上报器
	if u.reporter != nil {
		u.reporter.Stop()
	}
}

// startIPv4Shooter 启动IPv4打流器
// :param sourceIPs: IPv4源IP列表
// :param targetAddrs: IPv4目标地址列表
// :param packetTemplate: 数据包模板
func (u *UDPShooter) startIPv4Shooter(sourceIPs []string, targetAddrs []*net.UDPAddr, packetTemplate []byte) {
	// 使用当前有效的带宽限制
	currentBandwidth := u.GetCurrentBandwidth()
	bandwidthPerIP := currentBandwidth / int64(len(sourceIPs))
	u.logger.Infof("🌐 IPv4配置 | 目标: %d个 | 源IP: %d个 | 当前带宽: %d Mbps | 每个源IP带宽: %d Mbps",
		len(targetAddrs), len(sourceIPs), currentBandwidth, bandwidthPerIP)

	for _, sourceIP := range sourceIPs {
		// 创建速率限制器，限制该IP的总流量
		var rateLimiter *RateLimiter
		if bandwidthPerIP > 0 {
			rateLimiter = NewRateLimiter(bandwidthPerIP, len(packetTemplate))
		}

		// 启动多个工作协程，共享同一个速率限制器
		for i := 0; i < u.config.Concurrency.WorkersPerIP; i++ {
			u.wg.Add(1)
			go u.sendPackets(sourceIP, targetAddrs, packetTemplate, rateLimiter)
		}
	}
}

// startIPv6Shooter 启动IPv6打流器
// :param sourceIPs: IPv6源IP列表
// :param targetAddrs: IPv6目标地址列表
// :param packetTemplate: 数据包模板
func (u *UDPShooter) startIPv6Shooter(sourceIPs []string, targetAddrs []*net.UDPAddr, packetTemplate []byte) {
	// 使用当前有效的带宽限制
	currentBandwidth := u.GetCurrentBandwidth()
	bandwidthPerIP := currentBandwidth / int64(len(sourceIPs))
	u.logger.Infof("🌐 IPv6配置 | 目标: %d个 | 源IP: %d个 | 当前带宽: %d Mbps | 每个源IP带宽: %d Mbps",
		len(targetAddrs), len(sourceIPs), currentBandwidth, bandwidthPerIP)

	for _, sourceIP := range sourceIPs {
		// 创建速率限制器，限制该IP的总流量
		var rateLimiter *RateLimiter
		if bandwidthPerIP > 0 {
			rateLimiter = NewRateLimiter(bandwidthPerIP, len(packetTemplate))
		}

		// 启动多个工作协程，共享同一个速率限制器
		for i := 0; i < u.config.Concurrency.WorkersPerIP; i++ {
			u.wg.Add(1)
			go u.sendPackets(sourceIP, targetAddrs, packetTemplate, rateLimiter)
		}
	}
}

// Stop 停止UDP打流器
func (u *UDPShooter) Stop() {
	u.logger.Info("正在强制停止UDP打流器...")
	
	// 停止调度器
	if u.scheduler != nil {
		u.scheduler.Stop()
	}
	
	// 停止监控上报器
	if u.reporter != nil {
		u.reporter.Stop()
	}
	
	u.cancel()

	// 输出最终统计信息
	u.stats.mu.Lock()
	elapsed := time.Since(u.stats.startTime).Seconds()
	if elapsed > 0 {
		u.stats.bandwidthMbps = float64(u.stats.bytesSent*8) / (elapsed * 1000000)
	}

	// 格式化最终统计信息
	bytesStr := formatBytes(u.stats.bytesSent)
	packetsStr := formatNumber(u.stats.packetsSent)

	u.logger.Infof("🎯 最终统计 | 总发送: %s (%s包) | 平均带宽: %.2f Mbps | 总运行: %.1fs",
		bytesStr, packetsStr, u.stats.bandwidthMbps, elapsed)
	u.stats.mu.Unlock()
}

func main() {
	// 设置CPU亲和性，最大化性能
	runtime.GOMAXPROCS(runtime.NumCPU())

	// 设置GC参数，减少GC压力
	debug.SetGCPercent(1000)      // 增加GC触发阈值
	debug.SetMemoryLimit(1 << 30) // 设置内存限制为1GB

	// 加载配置
	config, err := loadConfig("config.json")
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 打印启动banner
	PrintBanner()

	// 设置日志
	logger := setupLogger(config)
	logger.Info("UDP打流器启动中...")

	// 创建UDP打流器
	shooter := NewUDPShooter(config, logger)

	// 启动打流器
	if err := shooter.Start(); err != nil {
		logger.Fatalf("启动失败: %v", err)
	}

	// 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	logger.Info("按 Ctrl+C 停止UDP打流器...")
	<-sigChan

	// 强制停止
	logger.Info("收到停止信号，正在强制停止...")
	shooter.Stop()
	logger.Info("UDP打流器已停止")
}
