/*
 * TgMusicBot - Telegram Music Bot
 *  Copyright (c) 2025 Ashok Shau
 *
 *  Licensed under GNU GPL v3
 *  See https://github.com/AshokShau/TgMusicBot
 */

package handlers

import (
	"ashokshau/tgmusic/src/core/db"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tg "github.com/amarnathcjd/gogram/telegram"
)

var (
	broadcastCancelFlag atomic.Bool
	broadcastInProgress atomic.Bool
)

// /cancelbroadcast
func cancelBroadcastHandler(m *tg.NewMessage) error {
	// If nothing is running, just inform
	if !broadcastInProgress.Load() {
		_, _ = m.Reply("ℹ️ No active broadcast is running right now.")
		return tg.EndGroup
	}

	// Mark as cancelled and free the “in progress” flag
	broadcastCancelFlag.Store(true)
	broadcastInProgress.Store(false)

	_, _ = m.Reply("🚫 Broadcast cancelled. You can start a new broadcast now.")
	return tg.EndGroup
}

// /broadcast
func broadcastHandler(m *tg.NewMessage) error {
	// Prevent parallel broadcasts
	if broadcastInProgress.Load() {
		_, _ = m.Reply("❗ A broadcast is already in progress. Please wait for it to finish or cancel it with /cancelbroadcast")
		return tg.EndGroup
	}

	broadcastInProgress.Store(true)
	defer broadcastInProgress.Store(false)

	ctx, cancel := db.Ctx()
	defer cancel()

	// Try to get replied message (may be nil if not replying)
	reply, replyErr := m.GetReplyMessage()

	args := strings.Fields(m.Args())

	copyMode := false
	noChats := false
	noUsers := false
	limit := 0              // 0 = no limit
	delay := time.Duration(0)
	var textParts []string  // text after flags to broadcast as plain text

	// Parse flags and collect remaining text
	//
	// Supports:
	//   -limit100   and   -limit 100
	//   -delay2s    and   -delay 2s
	for i := 0; i < len(args); i++ {
		a := args[i]

		switch {
		case a == "-copy":
			copyMode = true

		case a == "-nochat" || a == "-nochats":
			noChats = true

		case a == "-nouser" || a == "-nousers":
			noUsers = true

		// /broadcast -limit 100
		case a == "-limit":
			if i+1 >= len(args) {
				_, _ = m.Reply("❗ Invalid limit value. Example: <code>-limit 100</code>")
				return tg.EndGroup
			}
			i++
			val := strings.TrimSpace(args[i])
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				_, _ = m.Reply("❗ Invalid limit value. Example: <code>-limit 100</code>")
				return tg.EndGroup
			}
			limit = n

		// /broadcast -limit100
		case strings.HasPrefix(a, "-limit"):
			val := strings.TrimSpace(strings.TrimPrefix(a, "-limit"))
			if val == "" && i+1 < len(args) {
				i++
				val = strings.TrimSpace(args[i])
			}
			if val != "" {
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					_, _ = m.Reply("❗ Invalid limit value. Example: <code>-limit 100</code>")
					return tg.EndGroup
				}
				limit = n
			}

		// /broadcast -delay 2s
		case a == "-delay":
			if i+1 >= len(args) {
				_, _ = m.Reply("❗ Invalid delay. Example: <code>-delay 2s</code>")
				return tg.EndGroup
			}
			i++
			val := strings.TrimSpace(args[i])
			d, err := time.ParseDuration(val)
			if err != nil {
				_, _ = m.Reply("❗ Invalid delay. Example: <code>-delay 2s</code>")
				return tg.EndGroup
			}
			delay = d

		// /broadcast -delay2s
		case strings.HasPrefix(a, "-delay"):
			val := strings.TrimSpace(strings.TrimPrefix(a, "-delay"))
			if val == "" && i+1 < len(args) {
				i++
				val = strings.TrimSpace(args[i])
			}
			if val != "" {
				d, err := time.ParseDuration(val)
				if err != nil {
					_, _ = m.Reply("❗ Invalid delay. Example: <code>-delay 2s</code>")
					return tg.EndGroup
				}
				delay = d
			}

		default:
			// Anything that is not a known flag is treated as user text
			textParts = append(textParts, a)
		}
	}

	// New behavior: If user provided text after flags, broadcast that text only.
	// Example:
	//   /broadcast -limit 100 -delay 2s Hello Guys
	// → broadcast "Hello Guys"
	broadcastText := strings.Join(textParts, " ")

	// If no text and no replied message → error
	if broadcastText == "" && replyErr != nil {
		_, _ = m.Reply(
			"❗ Reply to a message or provide text to broadcast.\n\n" +
				"Examples:\n" +
				"<code>/broadcast -limit 100 -delay 2s Hello everyone</code>\n" +
				"or reply to a post and use:\n" +
				"<code>/broadcast -copy -limit 50 -delay 1s</code>",
		)
		return tg.EndGroup
	}

	// Fresh broadcast → clear cancellation flag
	broadcastCancelFlag.Store(false)

	chats, _ := db.Instance.GetAllChats(ctx)
	users, _ := db.Instance.GetAllUsers(ctx)

	var targets []int64
	if !noChats {
		targets = append(targets, chats...)
	}
	if !noUsers {
		targets = append(targets, users...)
	}

	if len(targets) == 0 {
		_, _ = m.Reply("❗ No targets found.")
		return tg.EndGroup
	}

	if limit > 0 && limit < len(targets) {
		targets = targets[:limit]
	}

	contentType := "Text"
	if broadcastText == "" {
		contentType = "Message"
	}

	sentMsg, _ := m.Reply(fmt.Sprintf(
		"🚀 <b>Broadcast Started</b>\n"+
			"👥 Targets: %d\n"+
			"📄 Content: %s\n"+
			"⚙ Mode: %s\n"+
			"⏱ Delay: %v\n\n"+
			"Send <code>/cancelbroadcast</code> to stop.",
		len(targets),
		contentType,
		map[bool]string{true: "Copy", false: "Forward"}[copyMode],
		delay,
	))

	var success int32
	var failed int32

	workers := 20
	jobs := make(chan int64, workers)
	wg := sync.WaitGroup{}

	useText := broadcastText != ""

	worker := func() {
		defer wg.Done()

		for id := range jobs {
			if broadcastCancelFlag.Load() {
				atomic.AddInt32(&failed, 1)
				continue
			}

			for {
				var errSend error

				if useText {
					// Broadcast plain text (no forward/copy, just send)
					_, errSend = m.Client.SendMessage(id, broadcastText)
				} else {
					// Broadcast replied message: copy or forward
					if copyMode {
						// True copy: no "Forwarded from", keeps inline buttons & content
						_, errSend = reply.CopyTo(id, nil)
					} else {
						// Normal forward
						_, errSend = reply.ForwardTo(id, nil)
					}
				}

				if errSend == nil {
					atomic.AddInt32(&success, 1)
					break
				}

				if wait := tg.GetFloodWait(errSend); wait > 0 {
					logger.Warn("FloodWait %ds for chatID=%d", wait, id)
					time.Sleep(time.Duration(wait) * time.Second)
					continue
				}

				atomic.AddInt32(&failed, 1)
				logger.Warn("[Broadcast] chatID: %d error: %v", id, errSend)
				break
			}

			if delay > 0 {
				time.Sleep(delay)
			}
		}
	}

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go worker()
	}

	for _, id := range targets {
		jobs <- id
	}
	close(jobs)

	wg.Wait()

	total := len(targets)
	result := fmt.Sprintf(
		"📢 <b>Broadcast Complete</b>\n\n"+
			"👥 Total: %d\n"+
			"✅ Success: %d\n"+
			"❌ Failed: %d\n"+
			"📄 Content: %s\n"+
			"⚙ Mode: %s\n"+
			"⏱ Delay: %v\n"+
			"🛑 Cancelled: %v\n",
		total,
		success,
		failed,
		contentType,
		map[bool]string{true: "Copy", false: "Forward"}[copyMode],
		delay,
		broadcastCancelFlag.Load(),
	)

	_, _ = sentMsg.Edit(result)
	// Extra safety
	broadcastInProgress.Store(false)
	return tg.EndGroup
}
