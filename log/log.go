package log

import (
	"fmt"
	"os"

	"github.com/Dreamacro/clash/common/observable"

	log "github.com/sirupsen/logrus"
)

var (
	logCh  = make(chan interface{})
	source = observable.NewObservable(logCh)
	level  = INFO
)

func init() {
	log.SetOutput(os.Stdout)
	log.SetLevel(log.DebugLevel)
}

type Event struct {
	LogLevel LogLevel
	Format   string
	Payload  []interface{}
	str      string
}

func (ev *Event) String() string {
	if ev.str == "" {
		ev.str = fmt.Sprintf(ev.Format, ev.Payload...)
	}
	return ev.str
}

func (e *Event) Type() string {
	return e.LogLevel.String()
}

func Infoln(format string, v ...interface{}) {
	event := newLog(INFO, format, v...)
	logCh <- event
	print(event)
}

func Warnln(format string, v ...interface{}) {
	event := newLog(WARNING, format, v...)
	logCh <- event
	print(event)
}

func Errorln(format string, v ...interface{}) {
	event := newLog(ERROR, format, v...)
	logCh <- event
	print(event)
}

func Debugln(format string, v ...interface{}) {
	event := newLog(DEBUG, format, v...)
	logCh <- event
	print(event)
}

func Fatalln(format string, v ...interface{}) {
	log.Fatalf(format, v...)
}

func Subscribe() observable.Subscription {
	sub, _ := source.Subscribe()
	return sub
}

func UnSubscribe(sub observable.Subscription) {
	source.UnSubscribe(sub)
}

func Level() LogLevel {
	return level
}

func SetLevel(newLevel LogLevel) {
	level = newLevel
}

func print(data *Event) {
	if data.LogLevel < level {
		return
	}

	switch data.LogLevel {
	case INFO:
		log.Infoln(data.String())
	case WARNING:
		log.Warnln(data.String())
	case ERROR:
		log.Errorln(data.String())
	case DEBUG:
		log.Debugln(data.String())
	}
}

func newLog(logLevel LogLevel, format string, v ...interface{}) *Event {
	ev := &Event{
		LogLevel: logLevel,
		Format:   format,
		Payload:  v,
	}
	// ev.String()
	return ev
}
