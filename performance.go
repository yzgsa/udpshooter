package main

import (
	"bytes"
	"net"
	"sync"
)

// PacketPool 数据包对象池
type PacketPool struct {
	pool sync.Pool
	size int
}

// NewPacketPool 创建数据包对象池
// :param size: 数据包大小
// :return: 数据包对象池
func NewPacketPool(size int) *PacketPool {
	return &PacketPool{
		size: size,
		pool: sync.Pool{
			New: func() interface{} {
				return make([]byte, size)
			},
		},
	}
}

// Get 获取数据包
// :return: 数据包字节数组
func (p *PacketPool) Get() []byte {
	return p.pool.Get().([]byte)
}

// Put 归还数据包
// :param packet: 数据包字节数组
func (p *PacketPool) Put(packet []byte) {
	if len(packet) == p.size {
		p.pool.Put(packet)
	}
}

// BatchWriter 批量写入器
type BatchWriter struct {
	conns    []*net.UDPConn
	buffer   *bytes.Buffer
	batchSize int
	mu       sync.Mutex
}

// NewBatchWriter 创建批量写入器
// :param conns: UDP连接列表
// :param batchSize: 批量大小
// :return: 批量写入器
func NewBatchWriter(conns []*net.UDPConn, batchSize int) *BatchWriter {
	return &BatchWriter{
		conns:     conns,
		buffer:    bytes.NewBuffer(make([]byte, 0, batchSize*1400)),
		batchSize: batchSize,
	}
}

// WriteBatch 批量写入数据包
// :param packets: 数据包列表
func (bw *BatchWriter) WriteBatch(packets [][]byte) {
	bw.mu.Lock()
	defer bw.mu.Unlock()

	for _, conn := range bw.conns {
		if conn == nil {
			continue
		}

		// 批量写入到单个连接
		for _, packet := range packets {
			conn.Write(packet)
		}
	}
}

// WriteSingle 写入单个数据包
// :param packet: 数据包
func (bw *BatchWriter) WriteSingle(packet []byte) {
	bw.mu.Lock()
	defer bw.mu.Unlock()

	for _, conn := range bw.conns {
		if conn == nil {
			continue
		}
		conn.Write(packet)
	}
}

// NetworkOptimizer 网络优化器
type NetworkOptimizer struct {
	// 预分配的数据包模板
	packetTemplates map[int][]byte
	mu              sync.RWMutex
}

// NewNetworkOptimizer 创建网络优化器
// :return: 网络优化器
func NewNetworkOptimizer() *NetworkOptimizer {
	return &NetworkOptimizer{
		packetTemplates: make(map[int][]byte),
	}
}

// GetPacketTemplate 获取数据包模板
// :param size: 数据包大小
// :param pattern: 负载模式
// :return: 数据包模板
func (no *NetworkOptimizer) GetPacketTemplate(size int, pattern string) []byte {
	no.mu.RLock()
	if template, exists := no.packetTemplates[size]; exists {
		no.mu.RUnlock()
		return template
	}
	no.mu.RUnlock()

	// 创建新模板
	template := createPacket(size, pattern)
	
	no.mu.Lock()
	no.packetTemplates[size] = template
	no.mu.Unlock()
	
	return template
}

// FastCopy 快速复制字节数组
// :param src: 源数组
// :return: 复制的数组
func FastCopy(src []byte) []byte {
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

// ZeroCopyPacket 零拷贝数据包创建
// :param template: 模板数据包
// :return: 新数据包
func ZeroCopyPacket(template []byte) []byte {
	// 使用append进行快速复制
	return append([]byte(nil), template...)
}

// OptimizedPacketPool 优化的数据包池
type OptimizedPacketPool struct {
	pools map[int]*PacketPool
	mu    sync.RWMutex
}

// NewOptimizedPacketPool 创建优化的数据包池
// :return: 优化的数据包池
func NewOptimizedPacketPool() *OptimizedPacketPool {
	return &OptimizedPacketPool{
		pools: make(map[int]*PacketPool),
	}
}

// GetPacket 获取指定大小的数据包
// :param size: 数据包大小
// :return: 数据包字节数组
func (opp *OptimizedPacketPool) GetPacket(size int) []byte {
	opp.mu.RLock()
	pool, exists := opp.pools[size]
	opp.mu.RUnlock()
	
	if !exists {
		opp.mu.Lock()
		pool = NewPacketPool(size)
		opp.pools[size] = pool
		opp.mu.Unlock()
	}
	
	return pool.Get()
}

// PutPacket 归还数据包
// :param packet: 数据包字节数组
func (opp *OptimizedPacketPool) PutPacket(packet []byte) {
	size := len(packet)
	opp.mu.RLock()
	pool, exists := opp.pools[size]
	opp.mu.RUnlock()
	
	if exists {
		pool.Put(packet)
	}
} 