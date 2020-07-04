```golang

func main() {
	logrus.SetReportCaller(true)
	customFormatter := &logrus.TextFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		FullTimestamp:   true,
		ForceColors:     true,
		// DisableColors:             false,
		// ForceQuote:                false,
		// DisableQuote:              true,
		// EnvironmentOverrideColors: true,
		// DisableTimestamp:          false,
		// DisableSorting:            true,
		// DisableLevelTruncation:    false,
		// PadLevelText:              false,
		// QuoteEmptyFields:          true,
		// FieldMap: log.FieldMap{
		// 	log.FieldKeyTime:  "@timestamp",
		// 	log.FieldKeyLevel: "@level",
		// 	log.FieldKeyMsg:   "@message",
		// },
	}
	logrus.SetFormatter(customFormatter)
	logrus.SetLevel(logrus.DebugLevel)

	logger := NewLogger()
	logger.MaxSizeMb = 2
	logger.MaxAge = 2
	logger.MaxBackups = 5
	hook, err := NewHook(
		logger,
		logrus.InfoLevel,
		customFormatter,
		&LogFileOpts{},
	)
	if err != nil {
		panic(err)
	}

	logrus.AddHook(hook)
	for {
		logrus.Debug("Debug message")
		logrus.Info("Info message")
		logrus.Warn("Warn message")
		logrus.Error("Error message")
		time.Sleep(time.Duration(1) * time.Millisecond)
	}
}

```