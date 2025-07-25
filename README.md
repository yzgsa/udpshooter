# UDP打流器

一个高性能的UDP流量生成工具，支持多源IP到多目标IP的流量测试。

## 功能特性

- 支持多源IP到多目标IP的UDP流量生成
- 可配置的带宽限制（Mbps）
- 可配置的总流量限制（MB）
- 多线程并发发送
- 实时统计报告
- 配置文件热重载
- 自动日志轮转和清理

## 配置文件

配置文件使用JSON格式，主要参数说明：

```json
{
    "source_ips": ["192.168.1.75", "192.168.1.76"],     // 源IP地址列表
    "target_ips": ["192.168.1.31", "192.168.1.32"],     // 目标IP地址列表
    "target_port": 8080,                                 // 目标端口
    "bandwidth": 1000,                                   // 总带宽限制 (Mbps)
    "total_bytes": 10240,                                // 总流量限制 (MB)
    "thread_count": 4,                                   // 每个连接的线程数量
    "packet_size": 1024,                                 // 数据包大小 (bytes)
    "config_file": "config.json",                        // 配置文件路径
    "reload_interval": 30,                               // 配置重载间隔(秒)
    "log_dir": "logs"                                    // 日志目录
}
```

### 单位说明

- **bandwidth**: 总带宽限制，单位为Mbps（兆比特每秒）
- **total_bytes**: 总流量限制，单位为MB（兆字节）
- **packet_size**: 数据包大小，单位为bytes（字节）

## 使用方法

1. 准备配置文件 `config.json`
2. 运行程序：
   ```bash
   go run main.go config.json
   ```
   或者编译后运行：
   ```bash
   go build -o udpshooter main.go logger.go
   ./udpshooter config.json
   ```

## 日志输出

程序会输出以下格式的日志：
- 连接创建信息
- 线程工作状态
- 流量统计（MB单位）
- 带宽使用情况（Mbps单位）
- 配置重载信息

## 信号处理

程序支持以下信号：
- `SIGINT` (Ctrl+C): 优雅停止
- `SIGTERM`: 优雅停止

## 注意事项

1. 确保有足够的网络权限
2. 带宽限制是总限制，会平均分配给所有连接
3. 流量限制是总限制，会平均分配给所有连接
4. 日志文件会自动轮转和清理（保留7天） 