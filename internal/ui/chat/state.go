package chat

import (
	"log/slog"
	"slices"

	"github.com/ayn2op/discordo/internal/notifications"
	"github.com/ayn2op/tview"
	"github.com/ayn2op/tview/tree"
	"github.com/diamondburned/arikawa/v3/discord"
	"github.com/diamondburned/arikawa/v3/gateway"
	"github.com/diamondburned/arikawa/v3/utils/httputil/httpdriver"
	"github.com/diamondburned/arikawa/v3/utils/ws"
	"github.com/diamondburned/ningen/v3/states/read"
)

func (m *Model) onRequest(r httpdriver.Request) error {
	if req, ok := r.(*httpdriver.DefaultRequest); ok {
		slog.Debug("new HTTP request", "method", req.Method, "url", req.URL)
	}
	return nil
}

func (m *Model) onRaw(event *ws.RawEvent) {
	slog.Debug(
		"new raw event",
		"code", event.OriginalCode,
		"type", event.OriginalType,
		// "data", event.Raw,
	)
}

func (m *Model) onReady(event *gateway.ReadyEvent) tview.Cmd {
	if event.UserSettings != nil {
		m.populateGuildsTree(*event.UserSettings)
	}
	m.guildsTree.SetCurrentNode(m.guildsTree.GetRoot())
	return tview.SetFocus(m.guildsTree)
}

// populateGuildsTree rebuilds the guilds sidebar from user settings and cabinet state.
func (m *Model) populateGuildsTree(settings gateway.UserSettings) {
	m.guildsTree.resetNodeIndex()

	dmNode := tree.NewNode("Direct Messages").SetReference(dmNode{}).SetExpandable(true).SetExpanded(false)
	m.guildsTree.dmRootNode = dmNode

	root := m.guildsTree.
		GetRoot().
		ClearChildren().
		AddChild(dmNode)

	guilds, err := m.state.Cabinet.Guilds()
	if err != nil {
		slog.Error("failed to get guilds from state", "err", err)
		return
	}

	guildsByID := make(map[discord.GuildID]discord.Guild, len(guilds))
	for _, guild := range guilds {
		guildsByID[guild.ID] = guild
	}

	// Track guilds already in folders to find orphans.
	// Newly joined guilds may not be synced to GuildFolders yet but always appear in guild positions.
	guildsInFolders := make(map[discord.GuildID]bool)
	for _, folder := range settings.GuildFolders {
		for _, guildID := range folder.GuildIDs {
			guildsInFolders[guildID] = true
		}
	}

	// Use GuildPositions for ordering (it's the canonical order).
	// Guilds not in any folder are "orphans" - add them directly to root.
	positions := settings.GuildPositions
	// GuildPositions is normally set; fall back to event order if not.
	if len(positions) == 0 {
		positions = make([]discord.GuildID, 0, len(guilds))
		for _, guild := range guilds {
			positions = append(positions, guild.ID)
		}
	}

	for _, guildID := range positions {
		// Already handled in folder processing below.
		if guildsInFolders[guildID] {
			continue
		}
		if guild, ok := guildsByID[guildID]; ok {
			m.guildsTree.createGuildNode(root, guild)
		}
	}

	for _, folder := range settings.GuildFolders {
		if folder.ID == 0 && len(folder.GuildIDs) == 1 {
			if guild, ok := guildsByID[folder.GuildIDs[0]]; ok {
				m.guildsTree.createGuildNode(root, guild)
			}
		} else {
			m.guildsTree.createFolderNode(folder, guildsByID)
		}
	}
}

func (m *Model) onMessageCreate(message *gateway.MessageCreateEvent) tview.Cmd {
	var cmds []tview.Cmd

	me, err := m.state.Cabinet.Me()
	if err == nil && message.Author.ID != me.ID {
		cmds = append(cmds, m.refreshUnreadStylesCmd(message.ChannelID))
	}

	selectedChannel, ok := m.SelectedChannel()
	if ok && selectedChannel.ID == message.ChannelID {
		m.removeTyper(message.Author.ID)
		m.messagesList.addMessage(message.Message)
	}

	cmds = append(cmds, m.notify(*message))
	return tview.Batch(cmds...)
}

func (m *Model) refreshUnreadStylesCmd(channelID discord.ChannelID) tview.Cmd {
	return func() tview.Msg {
		m.refreshUnreadStyles(channelID)
		return nil
	}
}

func (m *Model) notify(message gateway.MessageCreateEvent) tview.Cmd {
	return func() tview.Msg {
		if _, err := notifications.Notify(m.state, message, m.cfg); err != nil {
			slog.Error("failed to notify", "err", err, "channel_id", message.ChannelID, "message_id", message.ID)
		}
		return nil
	}
}

func (m *Model) onMessageUpdate(message *gateway.MessageUpdateEvent) {
	selectedChannel, ok := m.SelectedChannel()
	if !ok || selectedChannel.ID != message.ChannelID {
		return
	}

	index := slices.IndexFunc(m.messagesList.messages, func(m discord.Message) bool {
		return m.ID == message.ID
	})
	if index < 0 {
		return
	}

	m.messagesList.setMessage(index, message.Message)
}

func (m *Model) onMessageDelete(message *gateway.MessageDeleteEvent) {
	selectedChannel, ok := m.SelectedChannel()
	if !ok || selectedChannel.ID != message.ChannelID {
		return
	}

	prevCursor := m.messagesList.Cursor()
	deletedIndex := slices.IndexFunc(m.messagesList.messages, func(m discord.Message) bool {
		return m.ID == message.ID
	})
	if deletedIndex < 0 {
		return
	}

	m.messagesList.deleteMessage(deletedIndex)

	newCursor := cursorAfterDelete(prevCursor, deletedIndex, len(m.messagesList.messages))
	if newCursor != prevCursor {
		m.messagesList.SetCursor(newCursor)
	}
}

func cursorAfterDelete(prevCursor, deletedIndex, remaining int) int {
	switch {
	case prevCursor > deletedIndex:
		return prevCursor - 1
	case prevCursor == deletedIndex:
		if prev := deletedIndex - 1; prev >= 0 {
			return prev
		}
		return min(deletedIndex, remaining-1)
	default:
		return prevCursor
	}
}

func (m *Model) onGuildMembersChunk(event *gateway.GuildMembersChunkEvent) {
	m.messagesList.setFetchingChunk(false, uint(len(event.Members)))
}

func (m *Model) onGuildMemberRemove(event *gateway.GuildMemberRemoveEvent) {
	m.composer.cache.Invalidate(event.GuildID.String()+" "+event.User.Username, m.state.MemberState.SearchLimit)
}

func (m *Model) onTypingStart(event *gateway.TypingStartEvent) {
	selectedChannel, ok := m.SelectedChannel()
	if !ok || selectedChannel.ID != event.ChannelID {
		return
	}

	if m.isMe(event.UserID) {
		return
	}

	m.addTyper(event.UserID)
}

func (m *Model) onReadUpdate(event *read.UpdateEvent) {
	m.refreshUnreadStyles(event.ChannelID)
}
