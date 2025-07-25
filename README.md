# UDP打流工具

一个高性能的UDP流量生成工具，使用Go语言开发，支持多线程并发、带宽控制和配置热重载。

## 功能特性

- ✅ 可指定源IP地址
- ✅ 可指定带宽大小或流量总数
- ✅ 多线程并发发送
- ✅ 只管打流，不关心目标端状态
- ✅ 配置文件热重载
- ✅ 自动日志存档（保留7天）
- ✅ 优雅停止（支持Ctrl+C）

## 配置文件说明

配置文件 `config.json` 包含以下参数：

| 参数 | 类型 | 说明 | 默认值 |
|------|------|------|--------|
| `source_ip` | string | 源IP地址 | "" |
| `target_ip` | string | 目标IP地址 | 必需 |
| `target_port` | int | 目标端口 | 必需 |
| `bandwidth` | int64 | 带宽限制(bytes/s) | 0(无限制) |
| `total_bytes` | int64 | 总流量限制(bytes) | 0(无限制) |
| `thread_count` | int | 线程数量 | 4 |
| `packet_size` | int | 数据包大小(bytes) | 1024 |
| `config_file` | string | 配置文件路径 | "config.json" |
| `reload_interval` | int | 配置重载间隔(秒) | 30 |
| `log_dir` | string | 日志目录 | "logs" |

## 使用方法

### 1. 编译程序

```bash
go build -o udpshooter main.go
```

### 2. 修改配置文件

编辑 `config.json` 文件，设置目标地址和参数：

```json
{
    "source_ip": "192.168.1.100",
    "target_ip": "192.168.1.200", 
    "target_port": 8080,
    "bandwidth": 1048576,
    "thread_count": 8,
    "packet_size": 1024
}
```

### 3. 运行程序

```bash
# 使用默认配置文件
./udpshooter

# 指定配置文件
./udpshooter custom_config.json
```

### 4. 停止程序

按 `Ctrl+C` 或发送 `SIGTERM` 信号。

## 配置示例

### 基础配置
```json
{
    "target_ip": "10.0.0.1",
    "target_port": 1234,
    "thread_count": 4
}
```

### 带宽限制配置
```json
{
    "target_ip": "10.0.0.1", 
    "target_port": 1234,
    "bandwidth": 1048576,
    "thread_count": 8
}
```

### 总流量限制配置
```json
{
    "target_ip": "10.0.0.1",
    "target_port": 1234, 
    "total_bytes": 1073741824,
    "thread_count": 4
}
```

## 日志功能

程序支持自动日志存档功能：

- 日志文件格式：`udpshooter_YYYY-MM-DD_HH-MM-SS.log`
- 每24小时自动轮转日志文件
- 自动清理7天前的旧日志文件
- 日志同时输出到控制台和文件
- 支持手动触发日志轮转

## 注意事项

1. 程序会自动监控配置文件变化，每30秒检查一次
2. 修改配置文件后，程序会自动重新加载配置
3. 带宽限制和总流量限制可以同时使用
4. 源IP地址为可选，留空则使用系统默认
5. 程序会忽略UDP发送错误，持续发送数据包
6. 日志文件保存在 `logs` 目录下（可配置）

## 性能优化

- 使用多线程并发发送
- 预分配数据包缓冲区
- 读写锁保护配置更新
- 非阻塞的配置监控

## 许可证

MIT License 