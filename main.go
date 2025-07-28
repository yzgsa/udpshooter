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

// Config 配置结构体
type Config struct {
	Targets []Target `json:"targets"`
	Bandwidth struct {
		MaxBandwidthMbps int64 `json:"max_bandwidth_mbps"`
		MaxBytes         int64 `json:"max_bytes"`
	} `json:"bandwidth"`
	SourceIPs []string `json:"source_ips"`
	Packet    struct {
		Size           int    `json:"size"`
		PayloadPattern string `json:"payload_pattern"`
	} `json:"packet"`
	Concurrency struct {
		WorkersPerIP int `json:"workers_per_ip"`
		BufferSize   int `json:"buffer_size"`
	} `json:"concurrency"`
	Logging struct {
		Level      string `json:"level"`
		File       string `json:"file"`
		MaxSizeMB  int    `json:"max_size_mb"`
		MaxBackups int    `json:"max_backups"`
		MaxAgeDays int    `json:"max_age_days"`
		Compress   bool   `json:"compress"`
	} `json:"logging"`
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
}

// Stats 统计信息结构体
type Stats struct {
	mu           sync.RWMutex
	bytesSent    int64
	packetsSent  int64
	startTime    time.Time
	lastLogTime  time.Time
	bandwidthMbps float64
}

// NewUDPShooter 创建新的UDP打流器实例
// :param config: 配置信息
// :param logger: 日志记录器
// :return: UDP打流器实例
func NewUDPShooter(config *Config, logger *logrus.Logger) *UDPShooter {
	ctx, cancel := context.WithCancel(context.Background())
	return &UDPShooter{
		config:           config,
		logger:           logger,
		stats:            &Stats{startTime: time.Now(), lastLogTime: time.Now()},
		ctx:              ctx,
		cancel:           cancel,
		startTime:        time.Now(),
		packetPool:       NewOptimizedPacketPool(),
		networkOptimizer: NewNetworkOptimizer(),
	}
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
			continue
		}
		
		// 设置发送缓冲区大小
		if err := conn.SetWriteBuffer(u.config.Concurrency.BufferSize); err != nil {
			u.logger.Warnf("设置发送缓冲区失败 [%s] -> %s: %v", sourceIP, targetAddr.String(), err)
		}
		
		connections[i] = conn
		defer conn.Close()
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
			// 速率限制
			if rateLimiter != nil {
				rateLimiter.Wait()
			}

			// 快速复制数据包模板
			copy(packet, packetTemplate)
			
			// 批量发送到所有目标
			batchWriter.WriteSingle(packet)

			// 更新统计信息（原子操作）
			u.stats.mu.Lock()
			u.stats.bytesSent += int64(packetSize)
			u.stats.packetsSent++
			u.stats.mu.Unlock()
		}
	}
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

// startIPv4Shooter 启动IPv4打流器
// :param sourceIPs: IPv4源IP列表
// :param targetAddrs: IPv4目标地址列表
// :param packetTemplate: 数据包模板
func (u *UDPShooter) startIPv4Shooter(sourceIPs []string, targetAddrs []*net.UDPAddr, packetTemplate []byte) {
	bandwidthPerIP := u.config.Bandwidth.MaxBandwidthMbps / int64(len(sourceIPs))
	u.logger.Infof("🌐 IPv4配置 | 目标: %d个 | 源IP: %d个 | 每个IP带宽: %d Mbps", 
		len(targetAddrs), len(sourceIPs), bandwidthPerIP)

	for _, sourceIP := range sourceIPs {
		// 创建速率限制器
		var rateLimiter *RateLimiter
		if bandwidthPerIP > 0 {
			rateLimiter = NewRateLimiter(bandwidthPerIP)
		}

		// 启动多个工作协程
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
	bandwidthPerIP := u.config.Bandwidth.MaxBandwidthMbps / int64(len(sourceIPs))
	u.logger.Infof("🌐 IPv6配置 | 目标: %d个 | 源IP: %d个 | 每个IP带宽: %d Mbps", 
		len(targetAddrs), len(sourceIPs), bandwidthPerIP)

	for _, sourceIP := range sourceIPs {
		// 创建速率限制器
		var rateLimiter *RateLimiter
		if bandwidthPerIP > 0 {
			rateLimiter = NewRateLimiter(bandwidthPerIP)
		}

		// 启动多个工作协程
		for i := 0; i < u.config.Concurrency.WorkersPerIP; i++ {
			u.wg.Add(1)
			go u.sendPackets(sourceIP, targetAddrs, packetTemplate, rateLimiter)
		}
	}
}

// Stop 停止UDP打流器
func (u *UDPShooter) Stop() {
	u.logger.Info("正在强制停止UDP打流器...")
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
	debug.SetGCPercent(1000) // 增加GC触发阈值
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