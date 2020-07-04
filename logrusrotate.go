package logrusrotate

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	defaultMaxSize = 100
	timeFormat     = "2006-01-02_15.04.05"
	MB             = 1024 * 1024
)

var _ io.WriteCloser = (*Logger)(nil)

type Logger struct {
	MaxSizeMb       int
	MaxAge          int
	MaxBackups      int
	size            int64
	file            *os.File
	lock            sync.Mutex
	rotateTaskChan  chan bool
	taskStartOnce   sync.Once
	logDir          string
	logfileBaseName string
	logfileFullName string
}

func NewLogger() *Logger {
	exeFile, _ := exec.LookPath(os.Args[0])
	exePath, _ := filepath.Abs(exeFile)
	logDir := filepath.Join(filepath.Dir(exePath), "log")
	logfileBaseName := filepath.Base(exePath)
	return &Logger{
		logfileFullName: "",
		MaxSizeMb:       0,
		MaxBackups:      0,
		MaxAge:          0,
		logDir:          logDir,
		logfileBaseName: logfileBaseName,
	}
}

type LogFileOpts map[logrus.Level]*Logger

type Hook struct {
	defaultLogger *Logger
	formatter     logrus.Formatter
	minLevel      logrus.Level
	loggerByLevel map[logrus.Level]*Logger
}

func NewHook(defaultLogger *Logger, minLevel logrus.Level, formatter logrus.Formatter, opts *LogFileOpts) (*Hook, error) {
	hook := Hook{
		defaultLogger: defaultLogger,
		minLevel:      minLevel,
		formatter:     formatter,
		loggerByLevel: make(map[logrus.Level]*Logger),
	}
	if hook.defaultLogger == nil {
		hook.defaultLogger = NewLogger()
	}

	if opts != nil {
		maxLevel := len(hook.Levels())
		for level, config := range *opts {
			if maxLevel <= int(level) {
				continue
			}
			hook.loggerByLevel[level] = &Logger{
				logfileFullName: config.logfileFullName,
				MaxSizeMb:       config.MaxSizeMb,
				MaxBackups:      config.MaxBackups,
				MaxAge:          config.MaxAge,
			}
		}
	}

	return &hook, nil
}

func (hook *Hook) Fire(entry *logrus.Entry) error {
	msg, err := hook.formatter.Format(entry)
	if err != nil {
		return err
	}

	if logger, ok := hook.loggerByLevel[entry.Level]; ok {
		_, err = logger.Write([]byte(msg))
	} else {
		_, err = hook.defaultLogger.Write([]byte(msg))
	}

	return err
}

func (hook *Hook) Levels() []logrus.Level {
	return logrus.AllLevels[:hook.minLevel+1]
}

func (l *Logger) Write(p []byte) (n int, err error) {
	l.lock.Lock()
	defer l.lock.Unlock()

	writeLen := int64(len(p))
	if writeLen > l.max() {
		return 0, fmt.Errorf(
			"write length: %d, max file size: %d", writeLen, l.max(),
		)
	}

	if l.file == nil {
		if err = l.openOrNew(len(p)); err != nil {
			return 0, err
		}
	}

	if l.size+writeLen > l.max() {
		if err := l.rotateImmediately(); err != nil {
			return 0, err
		}
	}

	n, err = l.file.Write(p)
	l.size += int64(n)

	return n, err
}

func (l *Logger) Close() error {
	l.lock.Lock()
	defer l.lock.Unlock()
	return l.close()
}

func (l *Logger) close() error {
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func (l *Logger) Rotate() error {
	l.lock.Lock()
	defer l.lock.Unlock()
	return l.rotateImmediately()
}

func (l *Logger) rotateImmediately() error {
	if err := l.close(); err != nil {
		return err
	}
	if err := l.openNew(); err != nil {
		return err
	}
	l.rotateTaskStart()
	return nil
}

func newFileName(logDir, logfileBaseName string) string {
	pid := strconv.Itoa(os.Getpid())
	return filepath.Join(logDir, logfileBaseName+".pid"+pid+"."+time.Now().Format(timeFormat)+".log")
}

func (l *Logger) openNew() error {
	l.logfileFullName = newFileName(l.logDir, l.logfileBaseName)
	err := os.MkdirAll(l.logDir, 0744)
	if err != nil {
		return fmt.Errorf("can't mkdir :%s. error: %s", l.logDir, err)
	}
	mode := os.FileMode(0644)
	f, err := os.OpenFile(l.logfileFullName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("can't open new logfile: %s", err)
	}
	l.file = f
	l.size = 0
	return nil
}

func (l *Logger) openOrNew(writeLen int) error {
	l.rotateTaskStart()
	info, err := os.Stat(l.logfileFullName)
	if os.IsNotExist(err) {
		return l.openNew()
	}
	if err != nil {
		return fmt.Errorf("access file error: %s", err)
	}

	if info.Size()+int64(writeLen) >= l.max() {
		return l.rotateImmediately()
	}

	file, err := os.OpenFile(l.logfileFullName, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return l.openNew()
	}
	l.file = file
	l.size = info.Size()
	return nil
}

func (l *Logger) rotateRunOnce() error {
	if l.MaxBackups == 0 && l.MaxAge == 0 {
		return nil
	}

	files, err := l.allLogFiles()
	if err != nil {
		return err
	}

	if l.MaxBackups > 0 && l.MaxBackups < len(files) {
		rmoveCount := len(files) - l.MaxBackups
		for i := 1; i <= rmoveCount; i++ {
			deleteFile := files[len(files)-i]
			os.Remove(filepath.Join(l.logDir, deleteFile.FileInfo.Name()))
		}
	}
	if l.MaxAge > 0 && len(files) > 1 {
		newestFile := files[0]
		deadLine := newestFile.timestamp.AddDate(0, 0, -1*l.MaxAge)
		for i := len(files) - 1; i > 0; i-- {
			oldestFile := files[i]
			if oldestFile.timestamp.Before(deadLine) {
				os.Remove(filepath.Join(l.logDir, oldestFile.FileInfo.Name()))
			} else {
				break
			}
		}
	}

	return err
}

func (l *Logger) rotateRun() {
	for {
		<-l.rotateTaskChan
		l.rotateRunOnce()
	}
}

func (l *Logger) rotateTaskStart() {
	l.taskStartOnce.Do(func() {
		l.rotateTaskChan = make(chan bool, 1)
		go l.rotateRun()
	})
	select {
	case l.rotateTaskChan <- true:
	default:
	}
}

func (l *Logger) max() int64 {
	if l.MaxSizeMb == 0 {
		return int64(defaultMaxSize * MB)
	}
	return int64(l.MaxSizeMb) * int64(MB)
}

type LogInfo struct {
	timestamp time.Time
	os.FileInfo
}

type LogInfoSorter []LogInfo

func (s LogInfoSorter) Less(i, j int) bool {
	return s[i].timestamp.After(s[j].timestamp)
}

func (s LogInfoSorter) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s LogInfoSorter) Len() int {
	return len(s)
}

func (l *Logger) allLogFiles() ([]LogInfo, error) {
	files, err := ioutil.ReadDir(l.logDir)
	if err != nil {
		return nil, fmt.Errorf("can NOT access directory: %s", err)
	}
	logFiles := []LogInfo{}

	for _, f := range files {
		if f.IsDir() {
			continue
		}
		logFiles = append(logFiles, LogInfo{f.ModTime(), f})
	}

	sort.Sort(LogInfoSorter(logFiles))

	return logFiles, nil
}
