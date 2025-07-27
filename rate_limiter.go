package main

import (
	"sync"
	"time"
)

// RateLimiter 速率限制器
type RateLimiter struct {
	mu           sync.Mutex
	rate         int64 // Mbps
	tokens       int64
	lastRefill   time.Time
	refillRate   int64 // tokens per second
	maxTokens    int64
}

// NewRateLimiter 创建新的速率限制器
// :param rateMbps: 速率限制（Mbps）
// :return: 速率限制器实例
func NewRateLimiter(rateMbps int64) *RateLimiter {
	// 将Mbps转换为tokens per second
	// 1 Mbps = 125000 bytes per second
	refillRate := (rateMbps * 125000) / 1000 // tokens per second
	maxTokens := refillRate * 2 // 允许2秒的突发

	return &RateLimiter{
		rate:       rateMbps,
		refillRate: refillRate,
		maxTokens:  maxTokens,
		tokens:     maxTokens, // 初始时满令牌
		lastRefill: time.Now(),
	}
}

// Wait 等待令牌可用
func (r *RateLimiter) Wait() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(r.lastRefill).Seconds()
	
	// 计算需要补充的令牌
	tokensToAdd := int64(elapsed * float64(r.refillRate))
	if tokensToAdd > 0 {
		r.tokens = min(r.tokens+tokensToAdd, r.maxTokens)
		r.lastRefill = now
	}

	// 如果令牌不足，等待
	if r.tokens < 1 {
		// 计算需要等待的时间
		waitTime := time.Duration(float64(1-r.tokens) / float64(r.refillRate) * float64(time.Second))
		r.mu.Unlock()
		time.Sleep(waitTime)
		r.mu.Lock()
		r.tokens = 0
	} else {
		r.tokens--
	}
}

// min 返回两个整数中的较小值
func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
} 