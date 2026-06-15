// Command dctl is a token-frugal CLI for an AI agent to drive a Discord bot:
// send, read, reply, watch. Output is deliberately minimal (ids and one-line
// messages, no JSON) so an LLM reading stdout spends as few tokens as possible.
//
// Config (env): DISCORD_BOT_TOKEN (required), DISCORD_CHANNEL_ID (default
// channel; overridable per-call with -c/--channel).
//
//	dctl send "hello"                 -> prints the new message id
//	dctl reply <message_id> "ok"      -> prints the reply id
//	dctl read [-n 20] [--after <id>]  -> "<id>\t<author>\t<content>" per line
//	dctl watch [-i 10] [--after <id>] -> same, streaming new messages forever
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Akayashuu/dctl"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	token := os.Getenv("DISCORD_BOT_TOKEN")
	client := dctl.New(token, os.Getenv("DISCORD_CHANNEL_ID"))
	ctx := context.Background()

	var err error
	switch cmd {
	case "send":
		err = runSend(ctx, client, args)
	case "reply":
		err = runReply(ctx, client, args)
	case "read":
		err = runRead(ctx, client, args)
	case "watch":
		err = runWatch(ctx, client, args)
	case "bridge":
		err = runBridge(ctx, client, args)
	case "react":
		err = runReact(ctx, client, args)
	case "thread":
		err = runThread(ctx, client, args)
	case "channel":
		err = runChannel(ctx, client, args)
	case "serve":
		err = runServe(ctx, client, token, args)
	case "service":
		err = runService(ctx, args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "dctl: unknown command %q\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dctl: "+err.Error())
		os.Exit(1)
	}
}

func runSend(ctx context.Context, c *dctl.Client, args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	ch := channelFlag(fs)
	fs.Parse(args)
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		return fmt.Errorf("usage: dctl send [-c CHANNEL] <text>")
	}
	msg, err := c.Send(ctx, *ch, text)
	if err != nil {
		return err
	}
	fmt.Println(msg.ID)
	return nil
}

func runReply(ctx context.Context, c *dctl.Client, args []string) error {
	fs := flag.NewFlagSet("reply", flag.ExitOnError)
	ch := channelFlag(fs)
	fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 2 {
		return fmt.Errorf("usage: dctl reply [-c CHANNEL] <message_id> <text>")
	}
	text := strings.TrimSpace(strings.Join(rest[1:], " "))
	msg, err := c.Reply(ctx, *ch, rest[0], text)
	if err != nil {
		return err
	}
	fmt.Println(msg.ID)
	return nil
}

func runRead(ctx context.Context, c *dctl.Client, args []string) error {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	ch := channelFlag(fs)
	n := fs.Int("n", 20, "number of recent messages (1-100)")
	after := fs.String("after", "", "only messages newer than this id")
	fs.Parse(args)
	msgs, err := c.Read(ctx, *ch, *n, *after)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		fmt.Println(line(m))
	}
	return nil
}

func runWatch(ctx context.Context, c *dctl.Client, args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	ch := channelFlag(fs)
	interval := fs.Int("i", 10, "poll interval in seconds")
	after := fs.String("after", "", "start watching after this id")
	fs.Parse(args)
	last := *after
	for {
		msgs, err := c.Read(ctx, *ch, 100, last)
		if err != nil {
			return err
		}
		for _, m := range msgs {
			fmt.Println(line(m))
			last = m.ID
		}
		time.Sleep(time.Duration(*interval) * time.Second)
	}
}

func runReact(ctx context.Context, c *dctl.Client, args []string) error {
	fs := flag.NewFlagSet("react", flag.ExitOnError)
	ch := channelFlag(fs)
	fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 2 {
		return fmt.Errorf("usage: dctl react [-c CHANNEL] <message_id> <emoji>")
	}
	return c.React(ctx, *ch, rest[0], rest[1])
}

func runThread(ctx context.Context, c *dctl.Client, args []string) error {
	fs := flag.NewFlagSet("thread", flag.ExitOnError)
	ch := channelFlag(fs)
	fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 2 {
		return fmt.Errorf("usage: dctl thread [-c CHANNEL] <message_id> <name>")
	}
	name := strings.TrimSpace(strings.Join(rest[1:], " "))
	t, err := c.StartThread(ctx, *ch, rest[0], name)
	if err != nil {
		return err
	}
	fmt.Println(t.ID)
	return nil
}

// channelFlag registers -c/--channel on fs and returns the bound pointer.
func channelFlag(fs *flag.FlagSet) *string {
	ch := fs.String("channel", "", "channel id (default: DISCORD_CHANNEL_ID)")
	fs.StringVar(ch, "c", "", "channel id (shorthand)")
	return ch
}

// line renders one message as a single tab-separated row, content flattened to
// one line — the most compact form an agent can still parse (id, author, text).
func line(m dctl.Message) string {
	content := strings.ReplaceAll(m.Content, "\n", " ")
	return m.ID + "\t" + m.Author.Username + "\t" + content
}

func usage() {
	fmt.Fprint(os.Stderr, `dctl — minimal Discord bot CLI

  dctl send  [-c CHANNEL] <text>              post a message, prints its id
  dctl reply [-c CHANNEL] <message_id> <text> reply in thread, prints reply id
  dctl read  [-c CHANNEL] [-n 20] [--after ID]  recent messages, one per line
  dctl watch [-c CHANNEL] [-i 10] [--after ID]  stream new messages forever
  dctl bridge --cmd '<command>' [-i 5] [--state FILE]
                                              link the channel to a command:
                                              run it per human message, post its
                                              stdout back (e.g. a Claude session)
  dctl react  [-c CHANNEL] <message_id> <emoji>  add a reaction (e.g. 👀)
  dctl thread [-c CHANNEL] <message_id> <name>  open a real thread off a message
  dctl channel <list|create|post|delete|ensure> [args] [--guild ID]
                                              manage channels: create [--forum] a
                                              channel, post <forum_id> <title>
                                              <content> a forum thread, delete on
                                              request
  dctl serve [--health-addr :8787] [--status-channel ID] [--state FILE] [--env-file PATH]
                                              always-on Gateway daemon: bot online
                                              24/7, slash commands (/set home,
                                              /session, /allow), supervises one
                                            bridge per session; --env-file loads
                                            secrets from a file (used by service)
  dctl service <install|uninstall|status|restart|update> [--health-addr ADDR]
               [--env-file PATH] [--source DIR] [--no-pull]
                                              manage the serve daemon: install it
                                              as a boot-started native service
                                              (systemd/launchd/Task Scheduler),
                                              restart it, or update = (git pull +)
                                              rebuild from --source (default cwd)
                                              then restart — run after a merge

env: DISCORD_BOT_TOKEN (required), DISCORD_CHANNEL_ID (default channel)
     DCTL_OWNER_ID (seed allowlist), DCTL_STATE_DIR (state dir)
`)
}
