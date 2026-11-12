package discord

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/bwmarrin/discordgo"
)

type ResourceDiscovery struct {
	GuildID  string              `json:"guild_id"`
	Channels []DiscoveredChannel `json:"channels"`
	Roles    []DiscoveredRole    `json:"roles"`
}

type DiscoveredChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type DiscoveredRole struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Managed  bool   `json:"managed"`
	Position int    `json:"position"`
}

func newRESTSession(tokenFile string) (*discordgo.Session, error) {
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, fmt.Errorf("read Discord token: %w", err)
	}
	value := strings.TrimSpace(string(token))
	if value == "" {
		return nil, fmt.Errorf("Discord token file is empty")
	}
	session, err := discordgo.New("Bot " + value)
	value = ""
	if err != nil {
		return nil, err
	}
	return session, nil
}

func Discover(tokenFile, guildID string) (ResourceDiscovery, error) {
	session, err := newRESTSession(tokenFile)
	if err != nil {
		return ResourceDiscovery{}, err
	}
	channels, err := session.GuildChannels(guildID)
	if err != nil {
		return ResourceDiscovery{}, fmt.Errorf("list guild channels: %w", err)
	}
	roles, err := session.GuildRoles(guildID)
	if err != nil {
		return ResourceDiscovery{}, fmt.Errorf("list guild roles: %w", err)
	}
	result := ResourceDiscovery{GuildID: guildID}
	for _, channel := range channels {
		if channel.Type != discordgo.ChannelTypeGuildText {
			continue
		}
		result.Channels = append(result.Channels, DiscoveredChannel{ID: channel.ID, Name: channel.Name, Type: "text"})
	}
	for _, role := range roles {
		result.Roles = append(result.Roles, DiscoveredRole{ID: role.ID, Name: role.Name, Managed: role.Managed, Position: role.Position})
	}
	sort.Slice(result.Channels, func(i, j int) bool { return result.Channels[i].Name < result.Channels[j].Name })
	sort.Slice(result.Roles, func(i, j int) bool { return result.Roles[i].Position > result.Roles[j].Position })
	return result, nil
}

func ResolveDisplayNames(tokenFile, guildID string, userIDs []string) (map[string]string, error) {
	session, err := newRESTSession(tokenFile)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	names := make(map[string]string, len(userIDs))
	for _, userID := range userIDs {
		member, err := session.GuildMember(guildID, userID)
		if err != nil {
			var restErr *discordgo.RESTError
			if errors.As(err, &restErr) && restErr.Response != nil && restErr.Response.StatusCode == http.StatusNotFound {
				names[userID] = "<not in guild>"
				continue
			}
			return names, fmt.Errorf("resolve Discord member %s: %w", userID, err)
		}
		name := member.Nick
		if name == "" && member.User != nil {
			name = member.User.GlobalName
			if name == "" {
				name = member.User.Username
			}
		}
		if name == "" {
			name = "<unnamed>"
		}
		names[userID] = name
	}
	return names, nil
}

func SelfTestChannel(tokenFile, channelID string) error {
	session, err := newRESTSession(tokenFile)
	if err != nil {
		return err
	}
	message, err := session.ChannelMessageSend(channelID, "VibeRange broker self-test: Discord channel delivery succeeded. This message will be removed automatically.")
	if err != nil {
		return fmt.Errorf("send Discord self-test message: %w", err)
	}
	if err := session.ChannelMessageDelete(channelID, message.ID); err != nil {
		return fmt.Errorf("delete Discord self-test message: %w", err)
	}
	return nil
}
