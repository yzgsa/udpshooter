package main

import (
	"testing"
	"time"
)

// TestCreatePacket 测试数据包创建
func TestCreatePacket(t *testing.T) {
	size := 100
	pattern := "TEST"
	packet := createPacket(size, pattern)
	
	if len(packet) != size {
		t.Errorf("数据包大小错误，期望: %d, 实际: %d", size, len(packet))
	}
	
	// 检查模式是否正确填充
	for i := 0; i < size; i++ {
		expected := pattern[i%len(pattern)]
		if packet[i] != expected {
			t.Errorf("位置 %d 的字节错误，期望: %c, 实际: %c", i, expected, packet[i])
		}
	}
}

// TestRateLimiter 测试速率限制器
func TestRateLimiter(t *testing.T) {
	rateMbps := int64(1000) // 1 Gbps
	limiter := NewRateLimiter(rateMbps)
	
	if limiter.rate != rateMbps {
		t.Errorf("速率设置错误，期望: %d, 实际: %d", rateMbps, limiter.rate)
	}
	
	// 测试令牌桶初始状态
	if limiter.tokens <= 0 {
		t.Error("令牌桶应该初始化为满状态")
	}
}

// TestMinFunction 测试min函数
func TestMinFunction(t *testing.T) {
	tests := []struct {
		a, b, expected int64
	}{
		{1, 2, 1},
		{2, 1, 1},
		{1, 1, 1},
		{-1, 1, -1},
		{1, -1, -1},
	}
	
	for _, test := range tests {
		result := min(test.a, test.b)
		if result != test.expected {
			t.Errorf("min(%d, %d) = %d, 期望: %d", test.a, test.b, result, test.expected)
		}
	}
}

// BenchmarkCreatePacket 数据包创建性能测试
func BenchmarkCreatePacket(b *testing.B) {
	size := 1400
	pattern := "UDP_SHOOTER"
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		createPacket(size, pattern)
	}
}

// BenchmarkRateLimiter 速率限制器性能测试
func BenchmarkRateLimiter(b *testing.B) {
	limiter := NewRateLimiter(1000)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		limiter.Wait()
	}
} 