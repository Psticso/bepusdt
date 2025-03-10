package main

import (
	"fmt"
	"github.com/Psticso/bepusdt/app"
	"github.com/Psticso/bepusdt/app/monitor"
	"github.com/Psticso/bepusdt/app/web"
	"os"
	"os/signal"
	"runtime"
)

func main() {
	if err := Init(); err != nil {

		panic(err)
	}

	monitor.Start()

	web.Start()

	fmt.Println("Bepusdt 启动成功，当前版本：" + app.Version)

	{
		var signals = make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt, os.Kill)
		<-signals
		runtime.GC()
	}
}
