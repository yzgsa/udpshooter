package main

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

// BannerInfo banner信息结构体
type BannerInfo struct {
	Version    string
	BuildDate  string
	Author     string
	GoVersion  string
	OS         string
	Arch       string
	CPUCount   int
	MemoryInfo string
}

// NewBannerInfo 创建banner信息
func NewBannerInfo() *BannerInfo {
	return &BannerInfo{
		Version:    "v0.9.0",
		BuildDate:  "2025-08-12",
		Author:     "yuanzi",
		GoVersion:  runtime.Version(),
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		CPUCount:   runtime.NumCPU(),
		MemoryInfo: getMemoryInfo(),
	}
}

// getMemoryInfo 获取内存信息
func getMemoryInfo() string {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// 转换为MB
	totalMB := m.TotalAlloc / 1024 / 1024
	sysMB := m.Sys / 1024 / 1024

	return fmt.Sprintf("Total: %d MB, Sys: %d MB", totalMB, sysMB)
}

// PrintBanner 打印banner和系统信息
func PrintBanner() {
	banner := `
 ██     ██ ███████   ███████          ██                          ██                 
░██    ░██░██░░░░██ ░██░░░░██        ░██                         ░██                 
░██    ░██░██    ░██░██   ░██  ██████░██       ██████   ██████  ██████  █████  ██████
░██    ░██░██    ░██░███████  ██░░░░ ░██████  ██░░░░██ ██░░░░██░░░██░  ██░░░██░░██░░█
░██    ░██░██    ░██░██░░░░  ░░█████ ░██░░░██░██   ░██░██   ░██  ░██  ░███████ ░██ ░ 
░██    ░██░██    ██ ░██       ░░░░░██░██  ░██░██   ░██░██   ░██  ░██  ░██░░░░  ░██   
░░███████ ░███████  ░██       ██████ ░██  ░██░░██████ ░░██████   ░░██ ░░██████░███   
 ░░░░░░░  ░░░░░░░   ░░       ░░░░░░  ░░   ░░  ░░░░░░   ░░░░░░     ░░   ░░░░░░ ░░░    `

	info := NewBannerInfo()

	// 打印banner
	fmt.Println(banner)
	fmt.Println()

	// 打印版本信息
	fmt.Printf("Version: %s\n", info.Version)
	fmt.Printf("Build Date: %s\n", info.BuildDate)
	fmt.Printf("Author: %s\n", info.Author)
	fmt.Printf("Go Version: %s\n", info.GoVersion)

	fmt.Println()
	// 打印启动时间
	fmt.Printf("Started at: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Println(strings.Repeat("=", 80))
}
