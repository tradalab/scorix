package event

import (
	"strings"
	"sync"

	"github.com/tradalab/scorix/internal/logger"
)

type Subscriber func(data any)

var (
	mu    sync.RWMutex
	subs  = make(map[string][]Subscriber)
	wilds = make(map[string][]Subscriber)
)

func Subscribe(topic string, fn Subscriber) func() {
	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(topic, "*") {
		wilds[topic] = append(wilds[topic], fn)
	} else {
		subs[topic] = append(subs[topic], fn)
	}
	logger.Info("event subscribed", logger.Str("topic", topic))
	return func() { Unsubscribe(topic, fn) }
}

func Unsubscribe(topic string, fn Subscriber) {
	mu.Lock()
	defer mu.Unlock()
	slice := subs[topic]
	if strings.Contains(topic, "*") {
		slice = wilds[topic]
	}
	for i, v := range slice {
		if &v == &fn {
			if strings.Contains(topic, "*") {
				wilds[topic] = append(slice[:i], slice[i+1:]...)
			} else {
				subs[topic] = append(slice[:i], slice[i+1:]...)
			}
			break
		}
	}
}

func Publish(topic string, data any) {
	mu.RLock()
	defer mu.RUnlock()
	for _, fn := range subs[topic] {
		go fn(data)
	}
	for pattern, fns := range wilds {
		if match(topic, pattern) {
			for _, fn := range fns {
				go fn(data)
			}
		}
	}
}

func match(topic, pattern string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, ".*") {
		prefix := pattern[:len(pattern)-2]
		return strings.HasPrefix(topic, prefix)
	}
	return topic == pattern
}
