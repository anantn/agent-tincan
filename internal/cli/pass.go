package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/history"
	"github.com/mvanhorn/agent-tincan/internal/pass"
)

// passReader is the live reader for a chat site; tests replace it.
var passReader = func(src history.Source) (history.Reader, bool) {
	return history.NewLiveReader(src, history.NewClient(), historyNow)
}

// DefaultPassWait is how long tincan pass waits for the destination to
// open the new chat.
const DefaultPassWait = 10 * time.Minute

func passCmd() *cobra.Command {
	var to, id string
	var wait time.Duration
	cmd := &cobra.Command{
		Use:   "pass <chat-url | source> --to <web agent>",
		Short: "Move one of the owner's chats into a new chat on another web chat teammate",
		Long: "Reads one of the owner's chats through the Tincan Chrome extension (a chat URL on " + history.LiveSourcesLabel() + ",\n" +
			"or a source name for its newest chat, or a source with --id), packs it into one message (its title, the\n" +
			"first line of each earlier prompt and the last 6 turns verbatim, cut to fit the destination's message cap),\n" +
			"and asks the destination web chat teammate, as this agent, to open a new chat with it. It prints the\n" +
			"conversation the destination opened; continue it in the browser.\n\n" +
			"The destination must be one of the six web chat teammates, signed in and not out of messages; anything\n" +
			"else is refused before the chat is read. The chat goes to the destination's vendor. See docs/adapters/pass.md.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" {
				return errors.New("--to names the web chat teammate to pass the chat to")
			}
			src, convID, err := passSource(args[0], id)
			if err != nil {
				return err
			}
			r, _, err := connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			roster, err := r.Agents(ctx)
			if err != nil {
				return err
			}
			dst, err := pass.Destination(roster, to, time.Now())
			if err != nil {
				return err
			}
			rd, ok := passReader(src)
			if !ok {
				return fmt.Errorf("%s is not a chat site tincan reads (want %s)", src, history.LiveSourcesLabel())
			}
			conv, err := pass.Read(ctx, rd, convID, history.Window{})
			if err != nil {
				return err
			}
			seed, err := pass.Pack(conv, pass.Budget(dst))
			if err != nil {
				return err
			}
			sent, err := r.SendGrouped(ctx, to, seed, envelope.KindAsk, "", pass.NewGroup())
			if err != nil {
				return err
			}
			cmd.PrintErrf("Passing the %s conversation %s to %s (request %s)\n", history.SourceLabel(src), conv.ID, to, sent.ID)
			res, err := waitFinal(cmd, r, sent.ID, wait)
			if err != nil {
				return err
			}
			if !res.Done() {
				return fmt.Errorf("%s has not opened the chat after %s; check it later with tincan get %s", to, wait, sent.ID)
			}
			body := ""
			if res.Reply != nil {
				body = res.Reply.Body
			}
			if res.Status != envelope.StatusAnswered {
				return fmt.Errorf("%s did not open the chat (%s): %s", to, res.Status, body)
			}
			newID, ok := pass.NewConversation(body, dst)
			if !ok {
				return fmt.Errorf("%s answered without naming its conversation; look for the new chat in %s", to, history.SourceLabel(dst))
			}
			cmd.Printf("Passed to %s. %s: %s\n", to, history.ConversationName(dst), newID)
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "the web chat teammate that opens the new chat (chatgpt-web, claude-web, ...)")
	cmd.Flags().StringVar(&id, "id", "", "with a source name, the conversation to pass (default: the newest)")
	cmd.Flags().DurationVar(&wait, "wait", DefaultPassWait, "how long to wait for the destination to open the chat")
	return cmd
}

// passSource reads the chat a pass names: a chat URL, or a source name
// with an optional id ("" means its newest conversation).
func passSource(ref, id string) (history.Source, string, error) {
	if strings.Contains(ref, "://") {
		if id != "" {
			return "", "", errors.New("--id goes with a source name, not a chat URL")
		}
		return history.ResolveChatURL(ref)
	}
	src := history.Source(ref)
	if !history.IsLiveSource(src) {
		return "", "", fmt.Errorf("%q is not a chat URL or a chat site (want a chat URL, or one of %s)", ref, liveSourceNames())
	}
	return src, id, nil
}

// liveSourceNames lists the chat sites a pass reads, by source name.
func liveSourceNames() string {
	var names []string
	for _, s := range history.Sources {
		if history.IsLiveSource(s) {
			names = append(names, string(s))
		}
	}
	return strings.Join(names, ", ")
}

// waitFinal waits up to wait for request id to finish, a long poll at a
// time; it returns its last state either way.
func waitFinal(cmd *cobra.Command, r *client.Relay, id string, wait time.Duration) (client.Result, error) {
	deadline := time.Now().Add(wait)
	for {
		left := time.Until(deadline)
		res, err := r.Get(cmd.Context(), id, client.ClampWait(left))
		if err != nil || res.Done() || left <= 0 {
			return res, err
		}
	}
}
