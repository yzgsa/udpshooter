# UDP打流器 (UDP Shooter)

一个高性能的UDP打流器，支持20-50G带宽的并发能力，能够最大限度利用机器物理带宽。

## 功能特性

- 🚀 支持20-50G带宽的并发能力
- 📊 支持定义最大带宽或最大流量限制
- 🌐 支持多源IP和多目标IP地址（IPv4/IPv6分别处理）
- 📝 支持日志输出，保留7天内的日志
- ⚙️ 配置文件驱动，易于调整参数
- 🎯 优先跑量，最大化带宽利用率
- 🔧 高性能优化：对象池、批量写入、系统优化

## 系统要求

- Go 1.21+
- Linux系统（推荐）
- 足够的网络带宽和CPU资源

## 安装和运行

1. 克隆项目：
```bash
git clone <repository-url>
cd udpshooter
```

2. 安装依赖：
```bash
go mod tidy
```

3. 配置参数（编辑 `config.json`）：
```json
{
  "targets": [
    {
      "host": "192.168.1.100",
      "port": 8080
    },
    {
      "host": "192.168.1.101",
      "port": 8080
    },
    {
      "host": "192.168.1.102",
      "port": 8080
    }
  ],
  "bandwidth": {
    "max_bandwidth_mbps": 50000,
    "max_bytes": 0
  },
  "source_ips": [
    "192.168.1.10",
    "192.168.1.11",
    "192.168.1.12"
  ],
  "packet": {
    "size": 1400,
    "payload_pattern": "UDP_SHOOTER"
  },
  "concurrency": {
    "workers_per_ip": 4,
    "buffer_size": 65536
  },
  "logging": {
    "level": "info",
    "file": "logs/udpshooter.log",
    "max_size_mb": 100,
    "max_backups": 7,
    "max_age_days": 7,
    "compress": true
  }
}
```

4. 运行程序：
```bash
go run .
```

## 配置说明

### Targets（目标配置）
- 支持多个目标IP地址和端口（IPv4/IPv6分别处理）
- IPv4源IP只会打流到IPv4目标，IPv6源IP只会打流到IPv6目标
- 每个工作协程会轮询发送到所有目标
- 格式：`[{"host": "IP地址", "port": 端口号}]`
- 示例：
  ```json
  [
    {"host": "192.168.1.100", "port": 8080},
    {"host": "2408:864e:8000:105:1::20", "port": 5889}
  ]
  ```

### Bandwidth（带宽配置）
- `max_bandwidth_mbps`: 最大带宽限制（Mbps），0表示无限制
- `max_bytes`: 最大发送字节数，0表示无限制

### Source IPs（源IP配置）
- 支持多个源IP地址，每个IP会平摊总带宽

### Packet（数据包配置）
- `size`: UDP数据包大小（字节）
- `payload_pattern`: 数据包负载模式

### Concurrency（并发配置）
- `workers_per_ip`: 每个IP的工作协程数量
- `buffer_size`: UDP发送缓冲区大小

### Logging（日志配置）
- `level`: 日志级别（debug, info, warn, error）
- `file`: 日志文件路径
- `max_size_mb`: 单个日志文件最大大小（MB）
- `max_backups`: 最大备份文件数
- `max_age_days`: 日志文件最大保留天数
- `compress`: 是否压缩旧日志文件

## 性能优化建议

1. **CPU优化**：
   - 设置 `workers_per_ip` 为CPU核心数的1-2倍
   - 使用 `runtime.GOMAXPROCS(runtime.NumCPU())` 最大化CPU使用

2. **网络优化**：
   - 增加 `buffer_size` 到131072或更大
   - 使用多个源IP和目标IP分散负载
   - 调整数据包大小到1400字节（避免IP分片）
   - 多目标轮询发送，提高网络利用率
   - IPv4/IPv6分别处理，避免协议不匹配

3. **系统优化**：
   - 运行优化脚本：`./optimize_system.sh`
   - 增加系统文件描述符限制：`ulimit -n 65536`
   - 优化网络参数：
     ```bash
     echo 'net.core.rmem_max = 134217728' >> /etc/sysctl.conf
     echo 'net.core.wmem_max = 134217728' >> /etc/sysctl.conf
     echo 'net.ipv4.tcp_rmem = 4096 87380 134217728' >> /etc/sysctl.conf
     echo 'net.ipv4.tcp_wmem = 4096 65536 134217728' >> /etc/sysctl.conf
     sysctl -p
     ```

## 监控和日志

程序会每10秒输出一次统计信息，包括：
- 发送字节数
- 发送数据包数
- 当前带宽使用率
- 运行时间

日志文件保存在 `logs/udpshooter.log`，支持自动轮转和压缩。

## 注意事项

1. **网络安全**：确保在合法授权的网络环境中使用
2. **资源监控**：监控CPU、内存和网络使用情况
3. **目标确认**：确保目标地址正确，避免误攻击
4. **带宽限制**：根据实际网络环境调整带宽限制

## 故障排除

1. **权限问题**：确保有足够的权限绑定源IP地址
2. **网络问题**：检查网络连接和目标可达性
3. **性能问题**：调整并发参数和缓冲区大小
4. **日志问题**：检查日志文件权限和磁盘空间

## 许可证

本项目仅供学习和测试使用，请遵守相关法律法规。 