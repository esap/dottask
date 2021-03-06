package task

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type (
	//CronTask cron task info define
	CronTask struct {
		TaskInfo
		RawExpress   string `json:"express"` //运行周期表达式，当TaskType==TaskType_Cron时有效
		time_WeekDay *ExpressSet
		time_Month   *ExpressSet
		time_Day     *ExpressSet
		time_Hour    *ExpressSet
		time_Minute  *ExpressSet
		time_Second  *ExpressSet
	}
)

// GetConfig get task config info
func (task *CronTask) GetConfig() *TaskConfig {
	return &TaskConfig{
		TaskID:   task.taskID,
		TaskType: task.TaskType,
		IsRun:    task.IsRun,
		Handler:  task.handler,
		DueTime:  task.DueTime,
		Interval: 0,
		Express:  task.RawExpress,
		TaskData: task.TaskData,
	}
}

//Reset first check conf, then reload conf & restart task
//special, TaskID can not be reset
//special, if TaskData is nil, it can not be reset
//special, if Handler is nil, it can not be reset
func (task *CronTask) Reset(conf *TaskConfig) error {
	expresslist := strings.Split(conf.Express, " ")

	//basic check
	if conf.Express == "" {
		errmsg := "express is empty"
		task.taskService.Logger().Debug(fmt.Sprint("TaskInfo:Reset ", task, conf, "error", errmsg))
		return errors.New(errmsg)
	}
	if len(expresslist) != 6 {
		errmsg := "express is wrong format => not 6 parts"
		task.taskService.Logger().Debug(fmt.Sprint("TaskInfo:Reset ", task, conf, "error", errmsg))
		return errors.New("express is wrong format => not 6 parts")
	}

	//restart task
	task.Stop()
	task.IsRun = conf.IsRun
	if conf.TaskData != nil {
		task.TaskData = conf.TaskData
	}
	if conf.Handler != nil {
		task.handler = conf.Handler
	}
	task.DueTime = conf.DueTime
	task.RawExpress = conf.Express
	if task.TaskType == TaskType_Cron {
		task.time_WeekDay = parseExpress(expresslist[5], ExpressType_WeekDay)
		task.taskService.debugExpress(task.time_WeekDay)
		task.time_Month = parseExpress(expresslist[4], ExpressType_Month)
		task.taskService.debugExpress(task.time_Month)
		task.time_Day = parseExpress(expresslist[3], ExpressType_Day)
		task.taskService.debugExpress(task.time_Day)
		task.time_Hour = parseExpress(expresslist[2], ExpressType_Hour)
		task.taskService.debugExpress(task.time_Hour)
		task.time_Minute = parseExpress(expresslist[1], ExpressType_Minute)
		task.taskService.debugExpress(task.time_Minute)
		task.time_Second = parseExpress(expresslist[0], ExpressType_Second)
		task.taskService.debugExpress(task.time_Second)
	}
	task.Start()
	task.taskService.Logger().Debug(fmt.Sprint("TaskInfo:Reset ", task, conf, "success"))
	return nil
}

//Start start task
func (task *CronTask) Start() {
	if !task.IsRun {
		return
	}

	task.mutex.Lock()
	defer task.mutex.Unlock()

	if task.State == TaskState_Init || task.State == TaskState_Stop {
		task.State = TaskState_Run
		startCronTask(task)
	}
}

// RunOnce do task only once
// no match Express or Interval
// no recover panic
// support for #6 新增RunOnce方法建议
func (task *CronTask) RunOnce() error {
	err := task.handler(task.getTaskContext())
	return err
}

// NewCronTask create new cron task
func NewCronTask(taskID string, isRun bool, express string, handler TaskHandle, taskData interface{}) (Task, error) {
	task := new(CronTask)
	task.initCounters()
	task.taskID = taskID
	task.TaskType = TaskType_Cron
	task.IsRun = isRun
	task.handler = handler
	task.RawExpress = express
	task.TaskData = taskData
	expressList := strings.Split(express, " ")
	if len(expressList) != 6 {
		return nil, errors.New("express is wrong format => not 6 parts")
	}
	task.time_WeekDay = parseExpress(expressList[5], ExpressType_WeekDay)
	task.time_Month = parseExpress(expressList[4], ExpressType_Month)
	task.time_Day = parseExpress(expressList[3], ExpressType_Day)
	task.time_Hour = parseExpress(expressList[2], ExpressType_Hour)
	task.time_Minute = parseExpress(expressList[1], ExpressType_Minute)
	task.time_Second = parseExpress(expressList[0], ExpressType_Second)

	task.State = TaskState_Init
	return task, nil
}

//start cron task
func startCronTask(task *CronTask) {
	now := time.Now()
	nowSecond := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second(), 0, time.Local)
	afterTime := nowSecond.Add(time.Second).Sub(time.Now().Local())
	task.TimeTicker = time.NewTicker(DefaultPeriod)
	go func() {
		time.Sleep(afterTime)
		for {
			select {
			case <-task.TimeTicker.C:
				doCronTask(task)
			}
		}
	}()
}

func doCronTask(task *CronTask) {
	taskCtx := task.getTaskContext()
	defer func() {
		if taskCtx.TimeoutCancel != nil {
			taskCtx.TimeoutCancel()
		}
		task.putTaskContext(taskCtx)
		if err := recover(); err != nil {
			task.CounterInfo().ErrorCounter.Inc(1)
			task.taskService.Logger().Debug(fmt.Sprint(task.TaskID(), " cron handler recover error => ", err))
			if task.taskService.ExceptionHandler != nil {
				task.taskService.ExceptionHandler(taskCtx, fmt.Errorf("%v", err))
			}
			//goroutine panic, restart cron task
			startCronTask(task)
			task.taskService.Logger().Debug(fmt.Sprint(task.TaskID(), " goroutine panic, restart CronTask"))
		}
	}()

	handler := func() {
		defer func() {
			if task.Timeout > 0 {
				taskCtx.doneChan <- struct{}{}
			}
		}()
		now := time.Now()
		if task.time_WeekDay.IsMatch(now) &&
			task.time_Month.IsMatch(now) &&
			task.time_Day.IsMatch(now) &&
			task.time_Hour.IsMatch(now) &&
			task.time_Minute.IsMatch(now) &&
			task.time_Second.IsMatch(now) {

			//inc run counter
			task.CounterInfo().RunCounter.Inc(1)
			//do log
			if task.taskService != nil && task.taskService.OnBeforeHandler != nil {
				task.taskService.OnBeforeHandler(taskCtx)
			}
			var err error
			if !taskCtx.IsEnd {
				err = task.handler(taskCtx)
			}
			if err != nil {
				taskCtx.Error = err
				task.CounterInfo().ErrorCounter.Inc(1)
				if task.taskService != nil && task.taskService.ExceptionHandler != nil {
					task.taskService.ExceptionHandler(taskCtx, err)
				}
			}
			if task.taskService != nil && task.taskService.OnEndHandler != nil {
				task.taskService.OnEndHandler(taskCtx)
			}
		}
	}

	if task.Timeout > 0 {
		go handler()
		select {
		case <-taskCtx.TimeoutContext.Done():
			task.taskService.Logger().Debug(fmt.Sprint(task.TaskID(), "do handler timeout."))
		case <-taskCtx.doneChan:
			return
		}
	} else {
		handler()
	}

}
