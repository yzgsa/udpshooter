package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"sync"
)

// LogManager 日志管理器
type LogManager struct {
	logDir        string
	currentLog    *os.File
	logger        *log.Logger
	rotateChan    chan struct{}
	stopChan      chan struct{}
	wg            sync.WaitGroup
}

// NewLogManager 创建新的日志管理器
func NewLogManager(logDir string) (*LogManager, error) {
	// 创建日志目录
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("创建日志目录失败: %v", err)
	}

	lm := &LogManager{
		logDir:     logDir,
		rotateChan: make(chan struct{}),
		stopChan:   make(chan struct{}),
	}

	// 初始化当前日志文件
	if err := lm.rotateLog(); err != nil {
		return nil, err
	}

	// 启动日志轮转和清理
	go lm.startLogRotation()
	go lm.startLogCleanup()

	return lm, nil
}

// rotateLog 轮转日志文件
func (lm *LogManager) rotateLog() error {
	// 关闭当前日志文件
	if lm.currentLog != nil {
		lm.currentLog.Close()
	}

	// 生成新的日志文件名
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	logFileName := fmt.Sprintf("udpshooter_%s.log", timestamp)
	logPath := filepath.Join(lm.logDir, logFileName)

	// 创建新的日志文件
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("创建日志文件失败: %v", err)
	}

	// 设置多输出（同时输出到文件和控制台）
	multiWriter := io.MultiWriter(os.Stdout, file)
	lm.currentLog = file
	// 使用自定义前缀，只显示时间戳和程序名称
	lm.logger = log.New(multiWriter, "", log.LstdFlags)

	// 使用标准log输出，因为这是logger内部的消息
	log.Printf("[UDPshooter] 日志文件已轮转: %s", logPath)
	return nil
}

// startLogRotation 启动日志轮转
func (lm *LogManager) startLogRotation() {
	lm.wg.Add(1)
	defer lm.wg.Done()

	ticker := time.NewTicker(24 * time.Hour) // 每24小时轮转一次
	defer ticker.Stop()

	for {
		select {
		case <-lm.stopChan:
			return
		case <-ticker.C:
			if err := lm.rotateLog(); err != nil {
				log.Printf("[UDPshooter] 日志轮转失败: %v", err)
			}
		case <-lm.rotateChan:
			if err := lm.rotateLog(); err != nil {
				log.Printf("[UDPshooter] 手动日志轮转失败: %v", err)
			}
		}
	}
}

// startLogCleanup 启动日志清理
func (lm *LogManager) startLogCleanup() {
	lm.wg.Add(1)
	defer lm.wg.Done()

	ticker := time.NewTicker(12 * time.Hour) // 每12小时清理一次
	defer ticker.Stop()

	for {
		select {
		case <-lm.stopChan:
			return
		case <-ticker.C:
			lm.cleanupOldLogs()
		}
	}
}

// cleanupOldLogs 清理旧日志文件
func (lm *LogManager) cleanupOldLogs() {
	// 计算7天前的时间
	cutoffTime := time.Now().AddDate(0, 0, -7)

	// 遍历日志目录
	entries, err := os.ReadDir(lm.logDir)
	if err != nil {
		log.Printf("[UDPshooter] 读取日志目录失败: %v", err)
		return
	}

	var deletedCount int
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// 检查是否是日志文件
		if !strings.HasPrefix(entry.Name(), "udpshooter_") || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}

		// 解析文件名中的时间戳
		fileName := entry.Name()
		timeStr := strings.TrimSuffix(strings.TrimPrefix(fileName, "udpshooter_"), ".log")
		
		fileTime, err := time.Parse("2006-01-02_15-04-05", timeStr)
		if err != nil {
			log.Printf("[UDPshooter] 解析日志文件名时间失败: %s", fileName)
			continue
		}

		// 如果文件时间早于7天前，则删除
		if fileTime.Before(cutoffTime) {
			filePath := filepath.Join(lm.logDir, fileName)
			if err := os.Remove(filePath); err != nil {
				log.Printf("[UDPshooter] 删除旧日志文件失败: %s, %v", fileName, err)
			} else {
				deletedCount++
				log.Printf("[UDPshooter] 已删除旧日志文件: %s", fileName)
			}
		}
	}

	if deletedCount > 0 {
		log.Printf("[UDPshooter] 日志清理完成，删除了 %d 个旧日志文件", deletedCount)
	}
}

// GetLogger 获取日志记录器
func (lm *LogManager) GetLogger() *log.Logger {
	return lm.logger
}

// Log 自定义日志方法，添加UDPshooter标识
func (lm *LogManager) Log(format string, v ...interface{}) {
	message := fmt.Sprintf(format, v...)
	lm.logger.Printf("[UDPshooter] %s", message)
}

// RotateLog 手动触发日志轮转
func (lm *LogManager) RotateLog() {
	select {
	case lm.rotateChan <- struct{}{}:
	default:
	}
}

// Stop 停止日志管理器
func (lm *LogManager) Stop() {
	close(lm.stopChan)
	lm.wg.Wait()
	
	if lm.currentLog != nil {
		lm.currentLog.Close()
	}
	
	log.Println("[UDPshooter] 日志管理器已停止")
} 