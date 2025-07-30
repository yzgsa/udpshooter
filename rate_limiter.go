package main

import (
	"sync"
	"time"
)

// RateLimiter 改进的速率限制器
type RateLimiter struct {
	mu          sync.Mutex
	rateMbps    int64     // 速率限制（Mbps）
	bytesPerSec float64   // 每秒字节数
	tokens      float64   // 当前令牌数（浮点数提高精度）
	maxTokens   float64   // 最大令牌数
	lastRefill  time.Time // 上次补充令牌时间
	packetSize  int       // 数据包大小
}

// NewRateLimiter 创建新的速率限制器
// :param rateMbps: 速率限制（Mbps）
// :param packetSize: 数据包大小（字节）
// :return: 速率限制器实例
func NewRateLimiter(rateMbps int64, packetSize int) *RateLimiter {
	// 将Mbps转换为bytes per second，使用浮点数计算提高精度
	bytesPerSec := float64(rateMbps) * 125000.0 // 1 Mbps = 125000 bytes/sec

	// 允许2秒的突发流量
	maxTokens := bytesPerSec * 2.0

	return &RateLimiter{
		rateMbps:    rateMbps,
		bytesPerSec: bytesPerSec,
		maxTokens:   maxTokens,
		tokens:      maxTokens, // 初始时满令牌
		lastRefill:  time.Now(),
		packetSize:  packetSize,
	}
}

// Wait 等待令牌可用（消费一个数据包的令牌）
func (r *RateLimiter) Wait() {
	r.WaitBytes(r.packetSize)
}

// WaitBytes 等待指定字节数的令牌
func (r *RateLimiter) WaitBytes(bytes int) {
	if r.bytesPerSec <= 0 {
		return // 无限制
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	tokensNeeded := float64(bytes)

	for {
		now := time.Now()
		elapsed := now.Sub(r.lastRefill).Seconds()

		// 计算需要补充的令牌
		if elapsed > 0 {
			tokensToAdd := elapsed * r.bytesPerSec
			r.tokens = min(r.tokens+tokensToAdd, r.maxTokens)
			r.lastRefill = now
		}

		// 如果令牌足够，直接消费
		if r.tokens >= tokensNeeded {
			r.tokens -= tokensNeeded
			return
		}

		// 令牌不足，计算需要等待的时间
		deficit := tokensNeeded - r.tokens
		waitTime := time.Duration(deficit / r.bytesPerSec * float64(time.Second))

		// 释放锁，等待一段时间后重试
		r.mu.Unlock()
		time.Sleep(waitTime)
		r.mu.Lock()
	}
}

// GetCurrentRate 获取当前速率（仅用于调试）
func (r *RateLimiter) GetCurrentRate() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bytesPerSec / 125000.0 // 转换回Mbps
}

// min 返回两个浮点数中的较小值
func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
