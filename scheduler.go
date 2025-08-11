package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// ScheduleState 调度状态
type ScheduleState int

const (
	ScheduleWaiting ScheduleState = iota // 等待中
	ScheduleActive                       // 活跃中
	ScheduleCompleted                    // 已完成
	ScheduleSkipped                      // 已跳过
)

// ScheduleItem 调度项
type ScheduleItem struct {
	Schedule  Schedule      `json:"schedule"`
	State     ScheduleState `json:"state"`
	NextRun   time.Time     `json:"next_run"`
	LastRun   time.Time     `json:"last_run"`
	RunCount  int           `json:"run_count"`
}

// Scheduler 时间调度器
type Scheduler struct {
	schedules    []*ScheduleItem
	logger       *logrus.Logger
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.RWMutex
	activeCount  int
	callback     func(bool, int64) // 启动/停止回调函数，增加带宽参数
}

// NewScheduler 创建新的时间调度器
// :param schedules: 调度配置列表
// :param logger: 日志记录器
// :return: 时间调度器实例
func NewScheduler(schedules []Schedule, logger *logrus.Logger) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	
	scheduler := &Scheduler{
		schedules: make([]*ScheduleItem, 0, len(schedules)),
		logger:    logger,
		ctx:       ctx,
		cancel:    cancel,
	}
	
	// 初始化调度项
	for _, schedule := range schedules {
		item := &ScheduleItem{
			Schedule: schedule,
			State:    ScheduleWaiting,
		}
		
		// 计算下次运行时间
		nextRun, err := scheduler.calculateNextRun(schedule, time.Now())
		if err != nil {
			logger.Errorf("调度 [%s] 时间格式错误: %v", schedule.ID, err)
			continue
		}
		
		item.NextRun = nextRun
		scheduler.schedules = append(scheduler.schedules, item)
		
		logger.Infof("⏰ 调度 [%s] 已添加，下次运行: %s", 
			schedule.ID, nextRun.Format("2006-01-02 15:04:05"))
	}
	
	// 按下次运行时间排序
	scheduler.sortSchedules()
	
	return scheduler
}

// SetCallback 设置启动/停止回调函数
// :param callback: 回调函数，第一个参数为true表示启动，false表示停止；第二个参数为带宽限制
func (s *Scheduler) SetCallback(callback func(bool, int64)) {
	s.callback = callback
}

// Start 启动调度器
func (s *Scheduler) Start() {
	if len(s.schedules) == 0 {
		s.logger.Info("⏰ 无调度任务，调度器不启动")
		return
	}
	
	// 立即检查是否有正在进行的调度任务
	now := time.Now()
	s.checkCurrentSchedules(now)
	
	s.wg.Add(1)
	go s.scheduleLoop()
	s.logger.Infof("⏰ 调度器已启动，共 %d 个任务", len(s.schedules))
}

// Stop 停止调度器
func (s *Scheduler) Stop() {
	s.cancel()
	s.wg.Wait()
	s.logger.Info("⏰ 调度器已停止")
}

// scheduleLoop 调度循环
func (s *Scheduler) scheduleLoop() {
	defer s.wg.Done()
	
	ticker := time.NewTicker(1 * time.Second) // 秒级精度
	defer ticker.Stop()
	
	for {
		select {
		case <-s.ctx.Done():
			// 如果有活跃的任务，停止它们
			if s.activeCount > 0 && s.callback != nil {
				s.callback(false, 0)
			}
			return
		case now := <-ticker.C:
			s.checkSchedules(now)
		}
	}
}

// checkCurrentSchedules 检查当前时间是否在调度区间内
func (s *Scheduler) checkCurrentSchedules(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	for _, item := range s.schedules {
		if item.State != ScheduleWaiting {
			continue
		}
		
		// 检查当前时间是否在今天的调度区间内
		if s.isInScheduleWindow(item.Schedule, now) {
			s.logger.Infof("🔍 发现当前时间处于调度区间内: [%s] %s - %s", 
				item.Schedule.ID, item.Schedule.StartTime, item.Schedule.EndTime)
			s.startScheduleItem(item, now)
		}
	}
}

// isInScheduleWindow 检查当前时间是否在调度窗口内
func (s *Scheduler) isInScheduleWindow(schedule Schedule, now time.Time) bool {
	// 解析今天的开始和结束时间
	startTime, err := s.parseTimeOfDay(schedule.StartTime, now)
	if err != nil {
		return false
	}
	
	endTime, err := s.parseTimeOfDay(schedule.EndTime, now)
	if err != nil {
		return false
	}
	
	// 检查重复模式是否匹配今天
	if !s.isScheduleActiveToday(schedule, now) {
		return false
	}
	
	// 检查当前时间是否在区间内
	return (now.After(startTime) || now.Equal(startTime)) && now.Before(endTime)
}

// isScheduleActiveToday 检查调度是否在今天生效
func (s *Scheduler) isScheduleActiveToday(schedule Schedule, now time.Time) bool {
	switch schedule.Repeat {
	case "once":
		// 单次执行，检查是否是设置的那一天（这里简化处理，假设都是今天）
		return true
	case "daily":
		// 每天执行
		return true
	case "weekdays":
		// 工作日执行（周一到周五）
		weekday := now.Weekday()
		return weekday >= time.Monday && weekday <= time.Friday
	default:
		return false
	}
}
func (s *Scheduler) checkSchedules(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	// 检查是否需要启动任务
	for _, item := range s.schedules {
		if item.State == ScheduleWaiting && now.After(item.NextRun) {
			s.startScheduleItem(item, now)
		}
	}
	
	// 检查是否需要停止任务
	for _, item := range s.schedules {
		if item.State == ScheduleActive {
			endTime, err := s.parseTimeOfDay(item.Schedule.EndTime, now)
			if err == nil && now.After(endTime) {
				s.stopScheduleItem(item, now)
			}
		}
	}
}

// startScheduleItem 启动调度项
func (s *Scheduler) startScheduleItem(item *ScheduleItem, now time.Time) {
	item.State = ScheduleActive
	item.LastRun = now
	item.RunCount++
	s.activeCount++
	
	s.logger.Infof("🚀 启动调度任务 [%s]，第 %d 次运行，带宽限制: %d Mbps", 
		item.Schedule.ID, item.RunCount, item.Schedule.BandwidthMbps)
	
	// 如果这是第一个活跃的任务，启动打流器
	if s.activeCount == 1 && s.callback != nil {
		s.callback(true, item.Schedule.BandwidthMbps)
	}
}

// stopScheduleItem 停止调度项
func (s *Scheduler) stopScheduleItem(item *ScheduleItem, now time.Time) {
	item.State = ScheduleCompleted
	s.activeCount--
	
	s.logger.Infof("⏹️ 停止调度任务 [%s]", item.Schedule.ID)
	
	// 计算下次运行时间
	if item.Schedule.Repeat != "once" {
		nextRun, err := s.calculateNextRun(item.Schedule, now)
		if err == nil {
			item.NextRun = nextRun
			item.State = ScheduleWaiting
			s.logger.Infof("📅 调度任务 [%s] 下次运行: %s", 
				item.Schedule.ID, nextRun.Format("2006-01-02 15:04:05"))
		}
	}
	
	// 如果没有活跃的任务了，停止打流器
	if s.activeCount == 0 && s.callback != nil {
		s.callback(false, 0)
	}
	
	// 重新排序
	s.sortSchedules()
}

// calculateNextRun 计算下次运行时间
func (s *Scheduler) calculateNextRun(schedule Schedule, baseTime time.Time) (time.Time, error) {
	startTime, err := s.parseTimeOfDay(schedule.StartTime, baseTime)
	if err != nil {
		return time.Time{}, err
	}
	
	// 如果当前时间在今天的调度区间内，不需要计算下次运行时间
	if s.isInScheduleWindow(schedule, baseTime) {
		return startTime, nil
	}
	
	// 如果今天的时间已经过了，计算明天或下个工作日
	if startTime.Before(baseTime) || startTime.Equal(baseTime) {
		switch schedule.Repeat {
		case "once":
			// 单次执行，如果时间已过，则跳过
			return time.Time{}, fmt.Errorf("单次执行时间已过")
		case "daily":
			// 每日执行，加一天
			startTime = startTime.AddDate(0, 0, 1)
		case "weekdays":
			// 工作日执行，找下一个工作日
			startTime = s.nextWeekday(startTime)
		}
	} else {
		// 今天的时间还没到，检查是否符合重复模式
		if !s.isScheduleActiveToday(schedule, baseTime) {
			// 今天不符合重复模式，找下一个符合的日期
			switch schedule.Repeat {
			case "daily":
				startTime = startTime.AddDate(0, 0, 1)
			case "weekdays":
				startTime = s.nextWeekday(startTime)
			}
		}
	}
	
	return startTime, nil
}

// parseTimeOfDay 解析时间字符串为今天的具体时间
func (s *Scheduler) parseTimeOfDay(timeStr string, baseTime time.Time) (time.Time, error) {
	parts := strings.Split(timeStr, ":")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("时间格式错误，应为 HH:MM:SS")
	}
	
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return time.Time{}, fmt.Errorf("小时格式错误")
	}
	
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return time.Time{}, fmt.Errorf("分钟格式错误")
	}
	
	second, err := strconv.Atoi(parts[2])
	if err != nil || second < 0 || second > 59 {
		return time.Time{}, fmt.Errorf("秒数格式错误")
	}
	
	// 构造今天的具体时间
	year, month, day := baseTime.Date()
	location := baseTime.Location()
	
	return time.Date(year, month, day, hour, minute, second, 0, location), nil
}

// nextWeekday 获取下一个工作日
func (s *Scheduler) nextWeekday(t time.Time) time.Time {
	for {
		t = t.AddDate(0, 0, 1)
		// 周一到周五是工作日
		if t.Weekday() >= time.Monday && t.Weekday() <= time.Friday {
			break
		}
	}
	return t
}

// sortSchedules 按下次运行时间排序调度项
func (s *Scheduler) sortSchedules() {
	sort.Slice(s.schedules, func(i, j int) bool {
		// 将已完成的单次任务排到最后
		if s.schedules[i].Schedule.Repeat == "once" && s.schedules[i].State == ScheduleCompleted {
			return false
		}
		if s.schedules[j].Schedule.Repeat == "once" && s.schedules[j].State == ScheduleCompleted {
			return true
		}
		return s.schedules[i].NextRun.Before(s.schedules[j].NextRun)
	})
}

// GetStatus 获取调度器状态
func (s *Scheduler) GetStatus() []ScheduleItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	
	status := make([]ScheduleItem, len(s.schedules))
	for i, item := range s.schedules {
		status[i] = *item
	}
	
	return status
}

// IsActive 检查是否有活跃的调度任务
func (s *Scheduler) IsActive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeCount > 0
}