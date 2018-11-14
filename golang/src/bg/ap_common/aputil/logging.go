package aputil

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	levelFlag  = zapcore.InfoLevel
	daemonName string
)

func zapTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Format("2006/01/02 15:04:05"))
}

// Annotate each log message with the daemon and file that generated it.  If the
// file comes from a different package than the daemon, include the file's
// directory as well.
func zapCallerEncoder(caller zapcore.EntryCaller, enc zapcore.PrimitiveArrayEncoder) {
	dir, fileName := filepath.Split(caller.File)
	dir = filepath.Base(dir)
	if dir != daemonName {
		// The structure of our source tree is such that every daemon's
		// files are in a directory with the same name as the daemon.
		// If the directory name doesn't match the daemon, include the
		// directory in the log message.
		fileName = filepath.Join(dir, fileName)
	}

	enc.AppendString(fmt.Sprintf("%s:%s:%d", daemonName, fileName,
		caller.Line))
}

// newChildLogger returns a 'sugared' zap logger, intended to be used to log the
// output from child daemons.  This logger differs from that returned by
// ChildLogger by omitting the caller name, allowing us to tag the output using
// the name of the child instead.  e.g.:
//	2018/11/02 12:51:46     INFO    hostapd: wlan1: AP-ENABLED
func newChildLogger() (*zap.SugaredLogger, error) {
	var slogger *zap.SugaredLogger

	zapConfig := zap.NewDevelopmentConfig()
	zapConfig.Level = zap.NewAtomicLevelAt(levelFlag)
	zapConfig.DisableStacktrace = true
	zapConfig.DisableCaller = true
	zapConfig.EncoderConfig.EncodeTime = zapTimeEncoder

	logger, err := zapConfig.Build()
	if err == nil {
		slogger = logger.Sugar()
	}

	return slogger, err
}

// NewLogger returns a 'sugared' zap logger.  Each logged line will include a
// timestamp, the log level, and enough context to track down the source of the
// message.
// e.g.:
//     2018/11/15 14:35:44     INFO    ap.dns4d:dns4d.go:833   Adding PTR record
//     2018/11/15 14:35:44     INFO    ap.dns4d:data/dns.go:99 Ingested 22 hostnames
func NewLogger(name string) *zap.SugaredLogger {
	daemonName = name

	zapConfig := zap.NewDevelopmentConfig()
	zapConfig.Level = zap.NewAtomicLevelAt(levelFlag)
	zapConfig.DisableStacktrace = true
	zapConfig.EncoderConfig.EncodeTime = zapTimeEncoder
	zapConfig.EncoderConfig.EncodeCaller = zapCallerEncoder

	logger, err := zapConfig.Build()
	if err != nil {
		log.Panicf("can't zap: %s", err)
	}
	_ = zap.RedirectStdLog(logger)

	return logger.Sugar()
}

func init() {
	flag.Var(&levelFlag, "log-level",
		"Log level [debug,info,warn,error,panic,fatal]")
}
