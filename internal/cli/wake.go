package cli

import (
	"errors"
	"io"
	"net/url"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agent-tincan/internal/identity"
)

func wakeCmd() *cobra.Command {
	var socket, relayURL string
	cmd := &cobra.Command{Use: "wake", Short: "Configure relay webhook wakes (admin; applies immediately)"}
	cmd.PersistentFlags().StringVar(&socket, "socket", "", "relay admin socket (default on relay host: its own)")
	cmd.PersistentFlags().StringVar(&relayURL, "relay", "", "relay URL (default: saved config)")
	list := &cobra.Command{Use: "list", Short: "List configured wake methods, without addresses or secrets", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := adminRelay(socket, relayURL)
		if err != nil {
			return err
		}
		var methods map[string]string
		if err := r.Raw(cmd.Context(), "GET", "/v1/admin/wake-config", nil, &methods); err != nil {
			return err
		}
		names := make([]string, 0, len(methods))
		for name := range methods {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			cmd.Printf("%s\t%s\n", name, methods[name])
		}
		if len(names) == 0 {
			cmd.Println("No relay wake methods configured.")
		}
		return nil
	}}
	var endpoint string
	var stdin bool
	set := &cobra.Command{Use: "set <agent> --webhook <url>", Short: "Save a webhook wake and apply it without restarting the relay", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !identity.ValidName(args[0]) {
			return errors.New("invalid agent name")
		}
		if endpoint == "" {
			return errors.New("--webhook is required")
		}
		in := struct {
			Webhook     string  `json:"webhook"`
			BearerToken *string `json:"bearer_token,omitempty"`
		}{Webhook: endpoint}
		if stdin {
			b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 8194))
			if err != nil {
				return errors.New("could not read bearer token from stdin")
			}
			token := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
			if len(token) > 8192 || strings.ContainsAny(token, "\r\n\x00") {
				return errors.New("bearer token must be one line, at most 8192 bytes")
			}
			in.BearerToken = &token
		}
		r, err := adminRelay(socket, relayURL)
		if err != nil {
			return err
		}
		if err := r.Raw(cmd.Context(), "PUT", "/v1/admin/wake-config/"+url.PathEscape(args[0]), in, nil); err != nil {
			return err
		}
		cmd.Printf("%s: webhook wake saved and applied.\n", args[0])
		return nil
	}}
	set.Flags().StringVar(&endpoint, "webhook", "", "webhook URL (kept only in the relay's private wake.json)")
	set.Flags().BoolVar(&stdin, "bearer-token-stdin", false, "read bearer token from stdin; omitted preserves the existing token, empty input clears it")
	cmd.AddCommand(list, set)
	return cmd
}
