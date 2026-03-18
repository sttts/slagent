package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	slackapi "github.com/slack-go/slack"

	"github.com/sttts/slagent"
	slackclient "github.com/sttts/slagent/client"
	"github.com/sttts/slagent/credential"
)

// ReadCmd reads a Slack thread and processes it with Claude.
type ReadCmd struct {
	URL   string        `arg:"" help:"Slack DM/group DM URL (thread or conversation)."`
	Topic []string      `arg:"" optional:"" help:"Instruction for Claude (default: summarize the thread)."`
	Since time.Duration `help:"How far back to read conversation history (default: 24h). Only applies to non-thread URLs." default:"24h"`
}

func (cmd *ReadCmd) Run() error {
	ch, threadTS, _, _ := parseThreadURL(cmd.URL)
	if ch == "" {
		return fmt.Errorf("invalid URL: %s", cmd.URL)
	}

	// Only DMs and group DMs are supported
	if strings.HasPrefix(ch, "C") {
		return fmt.Errorf("channel threads are not supported — use a DM or group DM URL")
	}

	if err := credential.Ensure(cli.Workspace, interactiveAuth); err != nil {
		return err
	}
	creds, err := credential.Load(cli.Workspace)
	if err != nil {
		return err
	}
	sc := slackclient.New(creds.EffectiveToken(), creds.Cookie)
	sc.SetEnterprise(creds.Enterprise)

	var allMsgs []slackapi.Message

	if threadTS != "" {
		// Thread URL — fetch all replies
		fmt.Fprintf(os.Stderr, "📖 Reading thread...")
		page := 0
		cursor := ""
		for {
			params := &slackapi.GetConversationRepliesParameters{
				ChannelID: ch,
				Timestamp: threadTS,
				Cursor:    cursor,
			}
			msgs, hasMore, nextCursor, err := sc.GetConversationReplies(params)
			if err != nil {
				fmt.Fprintln(os.Stderr)
				return fmt.Errorf("fetch thread: %w", err)
			}
			allMsgs = append(allMsgs, msgs...)
			page++
			if page > 1 {
				fmt.Fprintf(os.Stderr, "\r📖 Reading thread... %d messages", len(allMsgs))
			}
			if !hasMore || nextCursor == "" {
				break
			}
			cursor = nextCursor
		}
		fmt.Fprintf(os.Stderr, "\r📖 Read %d messages\033[K\n", len(allMsgs))
	} else {
		// Conversation URL — fetch history with --since window
		fmt.Fprintf(os.Stderr, "📖 Reading conversation (last %s)...", cmd.Since)
		oldest := strconv.FormatInt(time.Now().Add(-cmd.Since).Unix(), 10)
		page := 0
		cursor := ""
		for {
			params := &slackapi.GetConversationHistoryParameters{
				ChannelID: ch,
				Oldest:    oldest,
				Cursor:    cursor,
			}
			resp, err := sc.GetConversationHistory(params)
			if err != nil {
				fmt.Fprintln(os.Stderr)
				return fmt.Errorf("fetch history: %w", err)
			}
			allMsgs = append(allMsgs, resp.Messages...)
			page++
			if page > 1 {
				fmt.Fprintf(os.Stderr, "\r📖 Reading conversation (last %s)... %d messages", cmd.Since, len(allMsgs))
			}
			if !resp.HasMore || resp.ResponseMetaData.NextCursor == "" {
				break
			}
			cursor = resp.ResponseMetaData.NextCursor
		}
		fmt.Fprintf(os.Stderr, "\r📖 Read %d messages (last %s)\033[K\n", len(allMsgs), cmd.Since)

		// GetConversationHistory returns newest-first; reverse for chronological order
		for i, j := 0, len(allMsgs)-1; i < j; i, j = i+1, j-1 {
			allMsgs[i], allMsgs[j] = allMsgs[j], allMsgs[i]
		}
	}

	if len(allMsgs) == 0 {
		return fmt.Errorf("no messages found")
	}

	// Resolve user names
	fmt.Fprintf(os.Stderr, "👤 Resolving users...")
	userCache := make(map[string]string)
	var sb strings.Builder
	for _, msg := range allMsgs {
		// Identify slagent bot messages by block_id
		kind, instanceID := slagent.ClassifyBlocks(msg.Blocks)
		if kind != slagent.BlockNone {
			emoji := slagent.InstanceEmoji(instanceID)
			fmt.Fprintf(&sb, "Claude Bot %s: %s\n", emoji, msg.Text)
			continue
		}

		// Human message
		user := resolveUser(sc, msg.User, userCache)
		sb.WriteString("@")
		sb.WriteString(user)
		sb.WriteString(": ")
		sb.WriteString(msg.Text)
		sb.WriteByte('\n')
	}
	fmt.Fprintf(os.Stderr, "\r👤 Resolved %d users\033[K\n", len(userCache))

	thread := sb.String()
	if strings.TrimSpace(thread) == "" {
		return fmt.Errorf("no readable messages in thread")
	}

	// Build instruction
	instruction := "summarize the thread"
	if topic := strings.Join(cmd.Topic, " "); topic != "" {
		instruction = topic
	}

	prompt := fmt.Sprintf("Here is a Slack thread:\n\n%s\n\n%s", thread, instruction)

	// Run claude -p with the prompt
	fmt.Fprintf(os.Stderr, "🤖 Processing with Claude...\n")
	claude := exec.Command("claude", "-p", prompt)
	claude.Stdout = os.Stdout
	claude.Stderr = os.Stderr
	return claude.Run()
}

// resolveUser resolves a Slack user ID to a display name, with caching.
func resolveUser(sc *slackclient.Client, userID string, cache map[string]string) string {
	if name, ok := cache[userID]; ok {
		return name
	}
	info, err := sc.GetUserInfo(userID)
	if err != nil {
		return userID
	}
	name := info.Profile.DisplayName
	if name == "" {
		name = info.RealName
	}
	if name == "" {
		name = info.Name
	}
	cache[userID] = name
	return name
}
