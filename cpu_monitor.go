package main

import (
	"runtime"
	"sync"
	"time"
)

// CPUMonitor CPU和内存监控器
type CPUMonitor struct {
	mu           sync.RWMutex
	cpuUsage     float64
	memoryUsage  float64
	lastCPUTime  time.Duration
	lastTime     time.Time
	running      bool
}

// NewCPUMonitor 创建新的CPU和内存监控器
func NewCPUMonitor() *CPUMonitor {
	return &CPUMonitor{
		lastTime: time.Now(),
	}
}

// Start 启动监控
func (c *CPUMonitor) Start() {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.mu.Unlock()
	
	go c.monitorLoop()
}

// Stop 停止监控
func (c *CPUMonitor) Stop() {
	c.mu.Lock()
	c.running = false
	c.mu.Unlock()
}

// GetCPUUsage 获取CPU使用率
func (c *CPUMonitor) GetCPUUsage() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cpuUsage
}

// GetMemoryUsage 获取内存使用率
func (c *CPUMonitor) GetMemoryUsage() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.memoryUsage
}

// monitorLoop 监控循环
func (c *CPUMonitor) monitorLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	
	for {
		c.mu.RLock()
		running := c.running
		c.mu.RUnlock()
		
		if !running {
			break
		}
		
		select {
		case <-ticker.C:
			c.updateStats()
		}
	}
}

// updateStats 更新CPU和内存统计
func (c *CPUMonitor) updateStats() {
	// 获取内存统计
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	
	currentTime := time.Now()
	
	c.mu.Lock()
	defer c.mu.Unlock()
	
	// 计算时间差
	timeDelta := currentTime.Sub(c.lastTime)
	if timeDelta.Seconds() < 0.1 {
		return // 时间间隔太短，跳过
	}
	
	// 简化的CPU使用率计算
	// 基于GC活动和goroutine数量来估算CPU使用率
	goroutines := runtime.NumGoroutine()
	cpuCount := runtime.NumCPU()
	
	// 基础使用率（基于goroutine数量）
	baseUsage := float64(goroutines) / float64(cpuCount*10) * 100
	if baseUsage > 100 {
		baseUsage = 100
	}
	
	// 平滑处理CPU使用率
	c.cpuUsage = c.cpuUsage*0.8 + baseUsage*0.2
	
	// 计算内存使用率
	// 使用 HeapInuse / HeapSys 来计算堆内存使用率
	if memStats.HeapSys > 0 {
		heapUsage := float64(memStats.HeapInuse) / float64(memStats.HeapSys) * 100
		c.memoryUsage = c.memoryUsage*0.8 + heapUsage*0.2
	}
	
	c.lastTime = currentTime
}