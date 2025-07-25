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
	SourceIP      string `json:"source_ip"`       // 源IP地址
	TargetIP      string `json:"target_ip"`       // 目标IP地址
	TargetPort    int    `json:"target_port"`     // 目标端口
	Bandwidth     int64  `json:"bandwidth"`       // 带宽限制 (bytes/s)
	TotalBytes    int64  `json:"total_bytes"`     // 总流量限制 (bytes)
	ThreadCount   int    `json:"thread_count"`    // 线程数量
	PacketSize    int    `json:"packet_size"`     // 数据包大小
	ConfigFile    string `json:"config_file"`     // 配置文件路径
	ReloadInterval int   `json:"reload_interval"` // 配置重载间隔(秒)
	LogDir        string `json:"log_dir"`         // 日志目录
}

// UDPShooter UDP打流器
type UDPShooter struct {
	config     *Config
	conn       *net.UDPConn
	stopChan   chan struct{}
	wg         sync.WaitGroup
	configLock sync.RWMutex
	logManager *LogManager
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

	return &config, nil
}

// Start 启动UDP打流
func (s *UDPShooter) Start() error {
	// 创建UDP连接
	raddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", s.config.TargetIP, s.config.TargetPort))
	if err != nil {
		return fmt.Errorf("解析目标地址失败: %v", err)
	}

	var laddr *net.UDPAddr
	if s.config.SourceIP != "" {
		laddr, err = net.ResolveUDPAddr("udp", s.config.SourceIP+":0")
		if err != nil {
			return fmt.Errorf("解析源地址失败: %v", err)
		}
	}

	conn, err := net.DialUDP("udp", laddr, raddr)
	if err != nil {
		return fmt.Errorf("创建UDP连接失败: %v", err)
	}
	s.conn = conn
	defer conn.Close()

	s.logManager.GetLogger().Printf("开始UDP打流: %s:%d -> %s:%d", s.config.SourceIP, 0, s.config.TargetIP, s.config.TargetPort)

	// 启动配置监控
	go s.monitorConfig()

	// 启动多个工作线程
	for i := 0; i < s.config.ThreadCount; i++ {
		s.wg.Add(1)
		go s.worker(i)
	}

	// 等待停止信号
	<-s.stopChan
	s.logManager.GetLogger().Println("正在停止UDP打流...")
	s.wg.Wait()

	return nil
}

// worker 工作线程函数
func (s *UDPShooter) worker(id int) {
	defer s.wg.Done()

	// 创建数据包
	packet := make([]byte, s.config.PacketSize)
	for i := range packet {
		packet[i] = byte(i % 256)
	}

	var bytesSent int64
	var startTime = time.Now()
	var lastReport = startTime

	for {
		select {
		case <-s.stopChan:
			return
		default:
		}

		s.configLock.RLock()
		config := s.config
		s.configLock.RUnlock()

		// 检查总流量限制
		if config.TotalBytes > 0 && bytesSent >= config.TotalBytes {
			s.logManager.GetLogger().Printf("线程 %d 达到总流量限制: %d bytes", id, bytesSent)
			return
		}

		// 发送数据包
		n, err := s.conn.Write(packet)
		if err != nil {
			s.logManager.GetLogger().Printf("线程 %d 发送失败: %v", id, err)
			continue
		}

		bytesSent += int64(n)

		// 带宽限制
		if config.Bandwidth > 0 {
			elapsed := time.Since(startTime).Seconds()
			expectedBytes := int64(elapsed * float64(config.Bandwidth))
			if bytesSent > expectedBytes {
				sleepTime := time.Duration(float64(bytesSent-expectedBytes) / float64(config.Bandwidth) * float64(time.Second))
				time.Sleep(sleepTime)
			}
		}

		// 定期报告
		if time.Since(lastReport) > 5*time.Second {
			s.logManager.GetLogger().Printf("线程 %d 已发送: %d bytes", id, bytesSent)
			lastReport = time.Now()
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
					s.logManager.GetLogger().Printf("重新加载配置失败: %v", err)
					continue
				}

				s.configLock.Lock()
				s.config = newConfig
				s.configLock.Unlock()

				s.logManager.GetLogger().Println("配置已重新加载")
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