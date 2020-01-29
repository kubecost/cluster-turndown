package loggin

import (
	"fmt"

	"k8s.io/klog"
)

// Named Status Logger that leverages klog levels
type NamedLogger string

// Logs a debug status at level 5.
func (l NamedLogger) Debug(format string, args ...interface{}) {
	v := klog.V(1)
	if v {
		v.Infof("[Debug](%s) %s\n", l, fmt.Sprintf(format, args...))
	}
}

// Logs an info status at level 3.
func (l NamedLogger) Log(format string, args ...interface{}) {
	v := klog.V(3)
	if v {
		v.Infof("[%s] %s\n", l, fmt.Sprintf(format, args...))
	}
}

// Logs an indented info status at level 3.
func (l NamedLogger) SLog(format string, args ...interface{}) {
	v := klog.V(3)
	if v {
		v.Infof("  [%s] %s\n", l, fmt.Sprintf(format, args...))
	}
}

// Logs a warning status at level 2.
func (l NamedLogger) Warn(format string, args ...interface{}) {
	v := klog.V(2)
	if v {
		v.Infof("[Warning](%s) %s\n", l, fmt.Sprintf(format, args...))
	}
}

// Logs an error at level 1.
func (l NamedLogger) Err(format string, args ...interface{}) {
	v := klog.V(1)
	if v {
		v.Infof("[Error] %s\n", fmt.Sprintf(format, args...))
	}
}
