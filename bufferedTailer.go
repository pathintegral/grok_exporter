package main

import (
	"container/list"
	"github.com/fstab/grok_exporter/tailer"
	"github.com/prometheus/client_golang/prometheus"
	"log"
	"sync"
	"time"
)

type bufferedTailerWithMetrics struct {
	out        chan string
	origTailer tailer.Tailer
}

func (b *bufferedTailerWithMetrics) LineChan() chan string {
	return b.out
}

func (b *bufferedTailerWithMetrics) ErrorChan() chan error {
	return b.origTailer.ErrorChan()
}

func (b *bufferedTailerWithMetrics) Close() {
	b.origTailer.Close()
}

func BufferedTailerWithMetrics(origTailer tailer.Tailer) tailer.Tailer {
	buffer := list.New()
	bufferSync := sync.NewCond(&sync.Mutex{}) // coordinate producer and consumer
	out := make(chan string)

	// producer
	go func() {
		bufferLoad := prometheus.NewSummary(prometheus.SummaryOpts{
			Name: "grok_exporter_line_buffer_peak_load",
			Help: "Number of lines that are read from the logfile and waiting to be processed. Peak value per second.",
		})
		prometheus.MustRegister(bufferLoad)
		bufferLoadLastSecond := 0
		tick := time.NewTicker(1 * time.Second)
		for {
			select {
			case line, ok := <-origTailer.LineChan():
				if ok {
					bufferSync.L.Lock()
					if buffer.Len() > bufferLoadLastSecond {
						bufferLoadLastSecond = buffer.Len()
					}
					buffer.PushBack(line)
					bufferSync.Signal()
					bufferSync.L.Unlock()
				} else {
					bufferSync.L.Lock()
					buffer = nil // make the consumer quit
					bufferSync.Signal()
					bufferSync.L.Unlock()
					prometheus.Unregister(bufferLoad)
					close(out)
					tick.Stop()
					return
				}
			case <-tick.C:
				bufferLoad.Observe(float64(bufferLoadLastSecond))
				bufferLoadLastSecond = 0
			}
		}
	}()

	// consumer
	go func() {
		for {
			bufferSync.L.Lock()
			for buffer != nil && buffer.Len() == 0 {
				bufferSync.Wait()
			}
			if buffer == nil {
				bufferSync.L.Unlock()
				return
			}
			first := buffer.Front()
			buffer.Remove(first)
			bufferSync.L.Unlock()
			switch line := first.Value.(type) {
			case string:
				out <- line
			default:
				// this cannot happen
				log.Fatal("unexpected type in tailer buffer")
			}
		}
	}()
	return &bufferedTailerWithMetrics{
		out:        out,
		origTailer: origTailer,
	}
}