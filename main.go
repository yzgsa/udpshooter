package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
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

// isIPv6 检查IP地址是否为IPv6
func isIPv6(ip string) bool {
	return strings.Contains(ip, ":")
}

// validateIP 验证IP地址格式
func validateIP(ip string) error {
	if ip == "" {
		return nil // 空IP表示使用默认
	}
	
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return fmt.Errorf("无效的IP地址格式: %s", ip)
	}
	
	return nil
}

// checkLocalIP 检查IP是否为本地IP地址
func checkLocalIP(ip string) error {
	if ip == "" {
		return nil
	}
	
	// 获取所有网络接口
	interfaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("获取网络接口失败: %v", err)
	}
	
	// 检查IP是否在本地接口上
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		
		for _, addr := range addrs {
			switch v := addr.(type) {
			case *net.IPNet:
				if v.IP.String() == ip {
					return nil // 找到匹配的本地IP
				}
			case *net.IPAddr:
				if v.IP.String() == ip {
					return nil // 找到匹配的本地IP
				}
			}
		}
	}
	
	// 如果没找到，可能是因为网络接口状态问题，暂时跳过验证
	// 在实际连接时会再次验证
	return nil
}

// getNetworkProtocol 根据IP地址获取网络协议
func getNetworkProtocol(ip string) string {
	if isIPv6(ip) {
		return "udp6"
	}
	return "udp"
}

// ConnectionInfo 连接信息
type ConnectionInfo struct {
	SourceIP   string
	TargetIP   string
	TargetPort int
	Conn       *net.UDPConn
	BytesSent  int64
	StartTime  time.Time
	Protocol   string // 网络协议 (udp/udp6)
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

	// 验证IP地址格式
	for i, sourceIP := range config.SourceIPs {
		if err := validateIP(sourceIP); err != nil {
			return nil, fmt.Errorf("源IP %d (%s): %v", i+1, sourceIP, err)
		}
		// 验证源IP是否为本地IP地址
		if err := checkLocalIP(sourceIP); err != nil {
			return nil, fmt.Errorf("源IP %d (%s): %v", i+1, sourceIP, err)
		}
	}
	
	for i, targetIP := range config.TargetIPs {
		if err := validateIP(targetIP); err != nil {
			return nil, fmt.Errorf("目标IP %d (%s): %v", i+1, targetIP, err)
		}
	}

	// 转换单位：MB -> bytes, Mbps -> bytes/s
	config.TotalBytes = mbToBytes(config.TotalBytes)
	config.Bandwidth = mbpsToBytesPerSec(config.Bandwidth)

	return &config, nil
}

// createConnections 创建所有连接
func (s *UDPShooter) createConnections() error {
	s.connections = make([]*ConnectionInfo, 0)
	
	// 按IP版本分组源IP和目标IP
	var sourceIPv4, sourceIPv6, targetIPv4, targetIPv6 []string
	
	for _, sourceIP := range s.config.SourceIPs {
		if isIPv6(sourceIP) {
			sourceIPv6 = append(sourceIPv6, sourceIP)
		} else {
			sourceIPv4 = append(sourceIPv4, sourceIP)
		}
	}
	
	for _, targetIP := range s.config.TargetIPs {
		if isIPv6(targetIP) {
			targetIPv6 = append(targetIPv6, targetIP)
		} else {
			targetIPv4 = append(targetIPv4, targetIP)
		}
	}
	
	// 创建IPv4连接
	ipv4Connections := len(sourceIPv4) * len(targetIPv4)
	// 创建IPv6连接
	ipv6Connections := len(sourceIPv6) * len(targetIPv6)
	
	totalConnections := ipv4Connections + ipv6Connections
	if totalConnections == 0 {
		return fmt.Errorf("没有找到兼容的IPv4或IPv6连接对")
	}
	
	bytesPerConnection := s.config.TotalBytes / int64(totalConnections)
	bandwidthPerConnection := s.config.Bandwidth / int64(totalConnections)

	s.logManager.Log("创建连接 - IPv4: %d个, IPv6: %d个, 总计: %d个", 
		ipv4Connections, ipv6Connections, totalConnections)
	s.logManager.Log("每个连接流量限制: %.2f MB, 带宽限制: %.2f Mbps", 
		bytesToMB(bytesPerConnection), bytesPerSecToMbps(bandwidthPerConnection))

	// 创建IPv4连接
	for _, sourceIP := range sourceIPv4 {
		for _, targetIP := range targetIPv4 {
			if err := s.createSingleConnection(sourceIP, targetIP, "udp", bytesPerConnection, bandwidthPerConnection); err != nil {
				return err
			}
		}
	}
	
	// 创建IPv6连接
	for _, sourceIP := range sourceIPv6 {
		for _, targetIP := range targetIPv6 {
			if err := s.createSingleConnection(sourceIP, targetIP, "udp6", bytesPerConnection, bandwidthPerConnection); err != nil {
				return err
			}
		}
	}

	return nil
}

// createSingleConnection 创建单个连接
func (s *UDPShooter) createSingleConnection(sourceIP, targetIP, protocol string, bytesPerConnection, bandwidthPerConnection int64) error {
	// 创建UDP连接
	var raddr *net.UDPAddr
	var err error
	
	if protocol == "udp6" {
		// IPv6地址需要用方括号包围
		raddr, err = net.ResolveUDPAddr(protocol, fmt.Sprintf("[%s]:%d", targetIP, s.config.TargetPort))
	} else {
		raddr, err = net.ResolveUDPAddr(protocol, fmt.Sprintf("%s:%d", targetIP, s.config.TargetPort))
	}
	if err != nil {
		// 如果目标地址解析失败，记录警告但继续尝试
		s.logManager.Log("警告: 无法解析目标地址 %s:%d: %v", targetIP, s.config.TargetPort, err)
		return nil
	}

	var laddr *net.UDPAddr
	if sourceIP != "" {
		// 对于源地址绑定，我们需要指定一个端口，让系统自动分配
		if protocol == "udp6" {
			// IPv6地址需要用方括号包围
			laddr, err = net.ResolveUDPAddr(protocol, fmt.Sprintf("[%s]:0", sourceIP))
		} else {
			laddr, err = net.ResolveUDPAddr(protocol, fmt.Sprintf("%s:0", sourceIP))
		}
		if err != nil {
			// 如果源地址绑定失败，尝试不绑定源地址
			s.logManager.Log("警告: 无法绑定源地址 %s，将使用系统默认地址: %v", sourceIP, err)
			laddr = nil
		}
	}

	conn, err := net.DialUDP(protocol, laddr, raddr)
	if err != nil {
		return fmt.Errorf("创建UDP连接失败 %s -> %s:%d: %v", sourceIP, targetIP, s.config.TargetPort, err)
	}
	
	// 验证连接是否成功建立
	if conn == nil {
		return fmt.Errorf("UDP连接创建失败: 连接对象为空")
	}

	connInfo := &ConnectionInfo{
		SourceIP:   sourceIP,
		TargetIP:   targetIP,
		TargetPort: s.config.TargetPort,
		Conn:       conn,
		StartTime:  time.Now(),
		Protocol:   protocol,
	}

	s.connections = append(s.connections, connInfo)
	s.logManager.Log("创建连接: %s -> %s:%d (协议: %s)", sourceIP, targetIP, s.config.TargetPort, protocol)
	
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
	totalConnections := len(s.connections)
	bytesPerConnection := s.config.TotalBytes / int64(totalConnections)
	bandwidthPerConnection := s.config.Bandwidth / int64(totalConnections)

	var bytesSent int64
	var lastReport = time.Now()

	s.logManager.Log("线程 %d-%d 开始工作: %s -> %s:%d (协议: %s)", connIndex, threadID, 
		connInfo.SourceIP, connInfo.TargetIP, connInfo.TargetPort, connInfo.Protocol)

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
			s.logManager.Log("线程 %d-%d (%s -> %s:%d) 达到流量限制: %.2f MB", 
				connIndex, threadID, connInfo.SourceIP, connInfo.TargetIP, connInfo.TargetPort, bytesToMB(bytesSent))
			return
		}

		// 发送数据包
		n, err := connInfo.Conn.Write(packet)
		if err != nil {
			s.logManager.Log("线程 %d-%d (%s -> %s:%d) 发送失败: %v", 
				connIndex, threadID, connInfo.SourceIP, connInfo.TargetIP, connInfo.TargetPort, err)
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
			s.logManager.Log("线程 %d-%d (%s -> %s:%d) 已发送: %.2f MB", 
				connIndex, threadID, connInfo.SourceIP, connInfo.TargetIP, connInfo.TargetPort, bytesToMB(bytesSent))
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