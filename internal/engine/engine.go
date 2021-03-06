// Package engine runs graw bots.
package engine

import (
	"container/list"
	"fmt"
	"sync"
	"time"

	"github.com/turnage/graw/botfaces"
	"github.com/turnage/graw/internal/api"
	"github.com/turnage/graw/internal/client"
	"github.com/turnage/graw/internal/monitor"
	"github.com/turnage/redditproto"
)

const (
	// blockTime is the amount of time to block between letting the next
	// monitor update.
	defaultBlockTime = time.Minute / 30
)

type Engine struct {
	// cli is the http client this engine uses to communicate with Reddit.
	cli client.Client
	// bot is the bot this engine runs.
	bot interface{}
	// stopSig is a channel over which bots can send a signal to the engine
	// to stop.
	stopSig chan bool
	// stop is a flag for the engine to conclude its main loop.
	stop bool
	// Mutex protects all fields below.
	sync.Mutex
	// monitors is a list of the monitors this engine uses to get events
	// from Reddit.
	monitors *list.List
	// userMonitors is a map of username to the monitors dedicated to that
	// username.
	userMonitors map[string]*list.Element
}

// Reply submits a reply.
func (e *Engine) Reply(parentName, text string) error {
	return api.Reply(e.cli.Do, parentName, text)
}

// SendMessage sends a private message.
func (e *Engine) SendMessage(user, subject, text string) error {
	return api.Compose(e.cli.Do, user, subject, text)
}

// SelfPost makes a self (text) post to a subreddit.
func (e *Engine) SelfPost(subreddit, title, text string) error {
	return api.Submit(e.cli.Do, subreddit, "self", title, text)
}

// LinkPost makes a link post to a subreddit.
func (e *Engine) LinkPost(subreddit, title, url string) error {
	return api.Submit(e.cli.Do, subreddit, "link", title, url)
}

// WatchUser starts monitoring a user.
func (e *Engine) WatchUser(user string) error {
	han, ok := e.bot.(botfaces.UserHandler)
	if !ok {
		return fmt.Errorf("bot cannot handle user posts or comments")
	}

	mon, err := monitor.UserMonitor(
		func(path, tip string, limit int) (
			[]*redditproto.Link,
			[]*redditproto.Comment,
			[]*redditproto.Message,
			error,
		) {
			return api.Scrape(e.cli.Do, path, tip, limit)
		},
		han.UserPost,
		han.UserComment,
		user,
	)
	if err != nil {
		return err
	}

	e.Lock()
	defer e.Unlock()
	e.userMonitors[user] = e.monitors.PushBack(mon)
	return nil
}

// Unwatch users stops monitoring a user.
func (e *Engine) UnwatchUser(user string) error {
	e.Lock()
	defer e.Unlock()

	if elem, ok := e.userMonitors[user]; ok {
		e.monitors.Remove(elem)
		delete(e.userMonitors, user)
	}

	return nil
}

// DigestThread returns a Link with a parsed comment tree.
func (e *Engine) DigestThread(permalink string) (*redditproto.Link, error) {
	return api.Thread(e.cli.Do, permalink)
}

// Stop stops the engine.
func (e *Engine) Stop() {
	e.stopSig <- true
}

func (e *Engine) Run() error {
	if loader, ok := e.bot.(botfaces.Loader); ok {
		if err := loader.SetUp(); err != nil {
			return err
		}
	}

	if tearer, ok := e.bot.(botfaces.Tearer); ok {
		defer tearer.TearDown()
	}

	for !e.stop {
		select {
		case <-e.stopSig:
			e.stop = true
		case <-time.After(e.blockTime()):
			if err := e.updateMonitors(); err != nil {
				if failer, ok := e.bot.(botfaces.Failer); !(ok && !failer.Fail(err)) {
					return err
				}
			}
		}
	}

	return nil
}

func (e *Engine) updateMonitors() error {
	e.Lock()
	monitors := []monitor.Monitor{}
	for i := e.monitors.Front(); i != nil; i = i.Next() {
		monitors = append(monitors, i.Value.(monitor.Monitor))
	}
	e.Unlock()

	for _, mon := range monitors {
		if err := mon.Update(
			func(path, tip string, limit int) (
				[]*redditproto.Link,
				[]*redditproto.Comment,
				[]*redditproto.Message,
				error,
			) {
				return api.Scrape(e.cli.Do, path, tip, limit)
			},
			func(id string) (bool, error) {
				return api.IsThereThing(e.cli.Do, id)
			},
		); err != nil {
			return err
		}
	}

	return nil
}

// If the bot specifies a blockTime by implementing BlockTimer, return its
// value. Otherwise, fall back to defaultBlockTime.
func (e *Engine) blockTime() time.Duration {
	if blockTimer, ok := e.bot.(botfaces.BlockTimer); ok {
		return blockTimer.BlockTime()
	} else {
		return defaultBlockTime
	}
}
