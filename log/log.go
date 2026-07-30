package log

import (
	"encoding/hex"
	"io"
	"log"
	"os"
	"sync"
	"sync/atomic"
)

var (
	mu     sync.RWMutex
	debug  atomic.Bool
	output io.Writer = os.Stdout
)

func Init() {
	mu.RLock()
	defer mu.RUnlock()
	log.SetOutput(output)
}

func SetOutput(writer io.Writer) {
	if writer == nil {
		writer = io.Discard
	}
	mu.Lock()
	output = writer
	log.SetOutput(output)
	mu.Unlock()
}

func EnableDebug() {
	debug.Store(true)
}

func DisableDebug() {
	debug.Store(false)
}

func DebugEnabled() bool { return debug.Load() }

func Print(v ...any) {
	log.Print(v...)
}

func DebugPrint(v ...any) {
	if debugEnabled() {
		log.Print(v...)
	}
}

func Println(v ...any) {
	log.Println(v...)
}

func DebugPrintln(v ...any) {
	if debugEnabled() {
		log.Println(v...)
	}
}

func Printf(format string, v ...any) {
	log.Printf(format, v...)
}

func DebugPrintf(format string, v ...any) {
	if debugEnabled() {
		log.Printf(format, v...)
	}
}

func Fatal(v ...any) {
	log.Fatal(v...)
}

func Fatalf(format string, v ...any) {
	log.Fatalf(format, v...)
}

func DumpHex(buf []byte) {
	dumpHex(buf)
}

func DebugDumpHex(buf []byte) {
	if debugEnabled() {
		dumpHex(buf)
	}
}

func NewLogger(prefix string) *log.Logger {
	mu.RLock()
	defer mu.RUnlock()
	return log.New(output, prefix, log.LstdFlags)
}

func debugEnabled() bool {
	return debug.Load()
}

func dumpHex(buf []byte) {
	mu.RLock()
	defer mu.RUnlock()
	dumper := hex.Dumper(output)
	defer func() {
		_ = dumper.Close()
	}()
	_, _ = dumper.Write(buf)
}
