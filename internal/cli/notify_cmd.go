package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/FiveMesh/fivemesh-backup/internal/config"
	"github.com/FiveMesh/fivemesh-backup/internal/notify"
	"github.com/FiveMesh/fivemesh-backup/internal/wizard"
)

// notifySend is replaced in tests.
var notifySend wizard.SendFunc = func(ctx context.Context, webhookURL string, settings config.DiscordNotifications, event notify.Event) error {
	ctx, cancel := context.WithTimeout(ctx, notify.Timeout)
	defer cancel()
	return notify.NewDiscord(webhookURL, settings).Send(ctx, event)
}

const notifyUsage = "notify requires a subcommand: discord, test, status, enable, disable, or remove"

func runNotify(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return withCode(exitUsage, errors.New(notifyUsage))
	}
	subcommand := args[0]
	switch subcommand {
	case "discord", "test", "status", "enable", "disable", "remove":
	default:
		return withCode(exitUsage, fmt.Errorf("unknown notify command %q; %s", subcommand, notifyUsage))
	}
	defaults, err := defaultPaths()
	if err != nil {
		return err
	}
	flags := newFlagSet("notify "+subcommand, stderr)
	paths := addPathFlags(flags, defaults)
	webhookStdin := flags.Bool("webhook-url-stdin", false, "discord: read the webhook URL from standard input")
	events := flags.String("events", "", "discord: events to post: all, or a comma list of success,failure,restore,config")
	serverName := flags.String("server-name", "", "discord: name for this server in messages (default: hostname)")
	mention := flags.String("mention", "", "discord: mention on failures, e.g. <@&ROLE_ID>; use \"none\" to clear")
	noTest := flags.Bool("no-test", false, "discord: save without sending a test message")
	if err := parseFlags(flags, args[1:]); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return withCode(exitUsage, errors.New("notify does not accept positional arguments"))
	}
	cfg, err := loadConfig(paths)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("no configuration found; run \"fivemesh-backup setup\" first")
		}
		return err
	}
	credentials, err := config.LoadCredentials(*paths.credentials)
	if err != nil {
		return err
	}
	resolved := pathValues(paths)
	discord := cfg.Notifications.Discord
	webhook := credentials.Notifications.DiscordWebhookURL

	switch subcommand {
	case "status":
		printNotifyStatus(stdout, discord, webhook)
		return nil

	case "test":
		if webhook == "" {
			return notify.ErrNotConfigured
		}
		if err := notifySend(ctx, webhook, discord, notify.TestEvent(discord)); err != nil {
			return fmt.Errorf("test message was not delivered: %w", err)
		}
		fmt.Fprintln(stdout, "Test message sent. Check your Discord channel.")
		if !discord.Enabled {
			fmt.Fprintln(stdout, `Notifications are currently disabled; run "fivemesh-backup notify enable" to turn them on.`)
		}
		return nil

	case "enable", "disable":
		if subcommand == "enable" && webhook == "" {
			return notify.ErrNotConfigured
		}
		cfg.Notifications.Discord.Enabled = subcommand == "enable"
		if err := config.SaveConfig(cfg, resolved.Config); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Discord notifications %sd.\n", subcommand)
		return nil

	case "remove":
		cfg.Notifications.Discord = config.DiscordNotifications{}
		credentials.Notifications.DiscordWebhookURL = ""
		if err := config.SaveReconfigured(cfg, credentials, resolved); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Discord notifications removed and the webhook URL was deleted from the credentials file.")
		return nil
	}

	// discord: set up or change the webhook and preferences.
	interactive := !*webhookStdin && isTerminal(stdin) && isTerminal(stdout)
	if interactive && *events == "" && *serverName == "" {
		prompt := wizard.NewPrompter(stdin, stdout)
		setup, err := wizard.ConfigureDiscord(ctx, prompt, discord, webhook, notifySend)
		if errors.Is(err, wizard.ErrSkipped) {
			fmt.Fprintln(stdout, "No changes were saved.")
			return nil
		}
		if err != nil {
			return err
		}
		discord, webhook = setup.Settings, setup.WebhookURL
	} else {
		if *webhookStdin {
			line, readErr := readLine(bufio.NewReader(stdin))
			if readErr != nil {
				return fmt.Errorf("read webhook URL: %w", readErr)
			}
			webhook = strings.TrimSpace(line)
		}
		if webhook == "" {
			return withCode(exitUsage, errors.New("provide the webhook URL with --webhook-url-stdin, or run this command in a terminal"))
		}
		normalized, _, err := notify.NormalizeWebhookURL(webhook)
		if err != nil {
			return withCode(exitUsage, err)
		}
		webhook = normalized
		discord.Enabled = true
		if *events != "" {
			parsed, parseErr := parseNotifyEvents(*events)
			if parseErr != nil {
				return withCode(exitUsage, parseErr)
			}
			discord.Events = parsed
		}
		if *serverName != "" {
			discord.ServerName = strings.TrimSpace(*serverName)
		}
		if !*noTest {
			if err := notifySend(ctx, webhook, discord, notify.TestEvent(discord)); err != nil {
				return fmt.Errorf("Discord did not accept the test message, nothing was saved: %w", err)
			}
		}
	}
	switch strings.TrimSpace(*mention) {
	case "":
	case "none":
		discord.MentionOnFailure = ""
	default:
		discord.MentionOnFailure = strings.TrimSpace(*mention)
	}
	if err := discord.Validate(); err != nil {
		return withCode(exitUsage, err)
	}
	cfg.Notifications.Discord = discord
	credentials.Notifications.DiscordWebhookURL = webhook
	if err := config.SaveReconfigured(cfg, credentials, resolved); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Discord notifications saved.")
	printNotifyStatus(stdout, discord, webhook)
	return nil
}

func parseNotifyEvents(value string) ([]string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "all" {
		return nil, nil
	}
	var events []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		events = append(events, part)
	}
	if len(events) == 0 {
		return nil, errors.New("--events needs at least one event")
	}
	return events, config.DiscordNotifications{Events: events}.Validate()
}

func printNotifyStatus(stdout io.Writer, discord config.DiscordNotifications, webhook string) {
	if webhook == "" {
		fmt.Fprintln(stdout, `Discord notifications: not configured (run "fivemesh-backup notify discord")`)
		return
	}
	state := "enabled"
	if !discord.Enabled {
		state = "disabled"
	}
	events := strings.Join(discord.Events, ", ")
	if events == "" {
		events = "all (" + strings.Join(config.AllNotifyEvents, ", ") + ")"
	}
	serverName := discord.ServerName
	if serverName == "" {
		serverName = notify.DefaultServerName() + " (hostname)"
	}
	mention := discord.MentionOnFailure
	if mention == "" {
		mention = "none"
	}
	fmt.Fprintf(stdout, "Discord notifications: %s\n", state)
	fmt.Fprintf(stdout, "  %-10s %s\n", "webhook", notify.MaskWebhookURL(webhook))
	fmt.Fprintf(stdout, "  %-10s %s\n", "events", events)
	fmt.Fprintf(stdout, "  %-10s %s\n", "server", serverName)
	fmt.Fprintf(stdout, "  %-10s %s\n", "mention", mention)
}
