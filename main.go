package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// Config 配置结构体
type Config struct {
	SourceIPs     []string `json:"source_ips"`      // 源IP地址列表
	TargetIPs     []string `json:"target_ips"`      // 目标IP地址列表
	TargetPort    int      `json:"target_port"`     // 目标端口
	Bandwidth     int64    `json:"bandwidth"`       // 总带宽限制 (Mbps)
	TotalBytes    int64    `json:"total_bytes"`     // 总流量限制 (MB)
	ThreadCount   int      `json:"thread_count"`    // 每个连接的线程数量
	PacketSize    int      `json:"packet_size"`     // 数据包大小
	ConfigFile    string   `json:"config_file"`     // 配置文件路径
	ReloadInterval int     `json:"reload_interval"` // 配置重载间隔(秒)
	LogDir        string   `json:"log_dir"`         // 日志目录
}

// mbToBytes 将MB转换为字节
func mbToBytes(mb int64) int64 {
	return mb * 1024 * 1024
}

// mbpsToBytesPerSec 将Mbps转换为字节/秒
func mbpsToBytesPerSec(mbps int64) int64 {
	return mbps * 1024 * 1024 / 8
}

// bytesToMB 将字节转换为MB
func bytesToMB(bytes int64) float64 {
	return float64(bytes) / 1024 / 1024
}

// bytesPerSecToMbps 将字节/秒转换为Mbps
func bytesPerSecToMbps(bytesPerSec int64) float64 {
	return float64(bytesPerSec) * 8 / 1024 / 1024
}

// ConnectionInfo 连接信息
type ConnectionInfo struct {
	SourceIP   string
	TargetIP   string
	TargetPort int
	Conn       *net.UDPConn
	BytesSent  int64
	StartTime  time.Time
}

// UDPShooter UDP打流器
type UDPShooter struct {
	config       *Config
	connections  []*ConnectionInfo
	stopChan     chan struct{}
	wg           sync.WaitGroup
	configLock   sync.RWMutex
	logManager   *LogManager
	statsLock    sync.RWMutex
	totalBytes   int64
}

// NewUDPShooter 创建新的UDP打流器
func NewUDPShooter(configPath string) (*UDPShooter, error) {
	config, err := loadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("加载配置失败: %v", err)
	}

	// 创建日志管理器
	logManager, err := NewLogManager(config.LogDir)
	if err != nil {
		return nil, fmt.Errorf("创建日志管理器失败: %v", err)
	}

	shooter := &UDPShooter{
		config:     config,
		stopChan:   make(chan struct{}),
		logManager: logManager,
	}

	return shooter, nil
}

// loadConfig 加载配置文件
func loadConfig(configPath string) (*Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	// 设置默认值
	if config.ThreadCount <= 0 {
		config.ThreadCount = 4
	}
	if config.PacketSize <= 0 {
		config.PacketSize = 1024
	}
	if config.ReloadInterval <= 0 {
		config.ReloadInterval = 30
	}
	if config.LogDir == "" {
		config.LogDir = "logs"
	}

	// 验证配置
	if len(config.SourceIPs) == 0 {
		return nil, fmt.Errorf("至少需要指定一个源IP")
	}
	if len(config.TargetIPs) == 0 {
		return nil, fmt.Errorf("至少需要指定一个目标IP")
	}
	if config.TargetPort <= 0 {
		return nil, fmt.Errorf("目标端口必须大于0")
	}

	// 转换单位：MB -> bytes, Mbps -> bytes/s
	config.TotalBytes = mbToBytes(config.TotalBytes)
	config.Bandwidth = mbpsToBytesPerSec(config.Bandwidth)

	return &config, nil
}

// createConnections 创建所有连接
func (s *UDPShooter) createConnections() error {
	s.connections = make([]*ConnectionInfo, 0)
	
	// 计算每个连接的流量限制
	totalConnections := len(s.config.SourceIPs) * len(s.config.TargetIPs)
	bytesPerConnection := s.config.TotalBytes / int64(totalConnections)
	bandwidthPerConnection := s.config.Bandwidth / int64(totalConnections)

	s.logManager.Log("创建 %d 个连接，每个连接流量限制: %.2f MB, 带宽限制: %.2f Mbps", 
		totalConnections, bytesToMB(bytesPerConnection), bytesPerSecToMbps(bandwidthPerConnection))

	for _, sourceIP := range s.config.SourceIPs {
		for _, targetIP := range s.config.TargetIPs {
			// 创建UDP连接
			raddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", targetIP, s.config.TargetPort))
			if err != nil {
				return fmt.Errorf("解析目标地址失败 %s:%d: %v", targetIP, s.config.TargetPort, err)
			}

			var laddr *net.UDPAddr
			if sourceIP != "" {
				laddr, err = net.ResolveUDPAddr("udp", sourceIP+":0")
				if err != nil {
					return fmt.Errorf("解析源地址失败 %s: %v", sourceIP, err)
				}
			}

			conn, err := net.DialUDP("udp", laddr, raddr)
			if err != nil {
				return fmt.Errorf("创建UDP连接失败 %s -> %s:%d: %v", sourceIP, targetIP, s.config.TargetPort, err)
			}

			connInfo := &ConnectionInfo{
				SourceIP:   sourceIP,
				TargetIP:   targetIP,
				TargetPort: s.config.TargetPort,
				Conn:       conn,
				StartTime:  time.Now(),
			}

			s.connections = append(s.connections, connInfo)
			s.logManager.Log("创建连接: %s -> %s:%d", sourceIP, targetIP, s.config.TargetPort)
		}
	}

	return nil
}

// Start 启动UDP打流
func (s *UDPShooter) Start() error {
	// 创建所有连接
	if err := s.createConnections(); err != nil {
		return err
	}

	// 启动配置监控
	go s.monitorConfig()

	// 为每个连接启动工作线程
	for i, connInfo := range s.connections {
		for j := 0; j < s.config.ThreadCount; j++ {
			s.wg.Add(1)
			go s.worker(i, j, connInfo)
		}
	}

	// 启动统计报告
	go s.reportStats()

	// 等待停止信号
	<-s.stopChan
	s.logManager.Log("正在停止UDP打流...")
	s.wg.Wait()

	// 关闭所有连接
	for _, connInfo := range s.connections {
		connInfo.Conn.Close()
	}

	return nil
}

// worker 工作线程函数
func (s *UDPShooter) worker(connIndex, threadID int, connInfo *ConnectionInfo) {
	defer s.wg.Done()

	// 创建数据包
	packet := make([]byte, s.config.PacketSize)
	for i := range packet {
		packet[i] = byte(i % 256)
	}

	// 计算该连接的流量限制
	totalConnections := len(s.config.SourceIPs) * len(s.config.TargetIPs)
	bytesPerConnection := s.config.TotalBytes / int64(totalConnections)
	bandwidthPerConnection := s.config.Bandwidth / int64(totalConnections)

	var bytesSent int64
	var lastReport = time.Now()

	s.logManager.Log("线程 %d-%d 开始工作: %s -> %s:%d", connIndex, threadID, 
		connInfo.SourceIP, connInfo.TargetIP, connInfo.TargetPort)

	for {
		select {
		case <-s.stopChan:
			return
		default:
		}

		s.configLock.RLock()
		_ = s.config
		s.configLock.RUnlock()

		// 检查总流量限制
		if bytesPerConnection > 0 && bytesSent >= bytesPerConnection {
			s.logManager.Log("线程 %d-%d 达到流量限制: %.2f MB", connIndex, threadID, bytesToMB(bytesSent))
			return
		}

		// 发送数据包
		n, err := connInfo.Conn.Write(packet)
		if err != nil {
			s.logManager.Log("线程 %d-%d 发送失败: %v", connIndex, threadID, err)
			continue
		}

		bytesSent += int64(n)
		
		// 更新连接统计
		s.statsLock.Lock()
		connInfo.BytesSent += int64(n)
		s.totalBytes += int64(n)
		s.statsLock.Unlock()

		// 带宽限制
		if bandwidthPerConnection > 0 {
			elapsed := time.Since(connInfo.StartTime).Seconds()
			expectedBytes := int64(elapsed * float64(bandwidthPerConnection))
			if bytesSent > expectedBytes {
				sleepTime := time.Duration(float64(bytesSent-expectedBytes) / float64(bandwidthPerConnection) * float64(time.Second))
				time.Sleep(sleepTime)
			}
		}

		// 定期报告
		if time.Since(lastReport) > 10*time.Second {
			s.logManager.Log("线程 %d-%d 已发送: %.2f MB", connIndex, threadID, bytesToMB(bytesSent))
			lastReport = time.Now()
		}
	}
}

// reportStats 定期报告统计信息
func (s *UDPShooter) reportStats() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.statsLock.RLock()
			totalBytes := s.totalBytes
			s.statsLock.RUnlock()

			s.logManager.Log("总发送流量: %.2f MB", bytesToMB(totalBytes))
			
			// 报告每个连接的统计
			for i, connInfo := range s.connections {
				s.statsLock.RLock()
				bytesSent := connInfo.BytesSent
				s.statsLock.RUnlock()
				
				s.logManager.Log("连接 %d (%s -> %s:%d): %.2f MB", 
					i, connInfo.SourceIP, connInfo.TargetIP, connInfo.TargetPort, bytesToMB(bytesSent))
			}
		}
	}
}

// monitorConfig 监控配置文件变化
func (s *UDPShooter) monitorConfig() {
	ticker := time.NewTicker(time.Duration(s.config.ReloadInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			newConfig, err := loadConfig(s.config.ConfigFile)
			if err != nil {
				s.logManager.Log("重新加载配置失败: %v", err)
				continue
			}

			s.configLock.Lock()
			s.config = newConfig
			s.configLock.Unlock()

			s.logManager.Log("配置已重新加载")
		}
	}
}

// Stop 停止UDP打流
func (s *UDPShooter) Stop() {
	close(s.stopChan)
	if s.logManager != nil {
		s.logManager.Stop()
	}
}

func main() {
	// 打印启动banner和系统信息
	PrintBanner()
	
	configPath := "config.json"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	shooter, err := NewUDPShooter(configPath)
	if err != nil {
		log.Fatalf("创建UDP打流器失败: %v", err)
	}

	// 处理信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("收到停止信号")
		shooter.Stop()
	}()

	if err := shooter.Start(); err != nil {
		log.Fatalf("启动UDP打流失败: %v", err)
	}
} 