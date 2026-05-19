package chat

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ayn2op/discordo/internal/config"
	"github.com/ayn2op/discordo/internal/http"
	"github.com/ayn2op/discordo/internal/ui"
	"github.com/ayn2op/tview"
	"github.com/ayn2op/tview/flex"
	"github.com/ayn2op/tview/keybind"
	"github.com/ayn2op/tview/layers"
	"github.com/diamondburned/arikawa/v3/discord"
	"github.com/diamondburned/arikawa/v3/gateway"
	"github.com/diamondburned/arikawa/v3/session"
	"github.com/diamondburned/arikawa/v3/state"
	"github.com/diamondburned/arikawa/v3/state/store/defaultstore"
	"github.com/diamondburned/arikawa/v3/utils/handler"
	"github.com/diamondburned/arikawa/v3/utils/httputil"
	"github.com/diamondburned/arikawa/v3/utils/ws"
	"github.com/diamondburned/ningen/v3"
	"github.com/diamondburned/ningen/v3/states/read"
	"github.com/gdamore/tcell/v3"
)

const typingDuration = 10 * time.Second

const (
	flexLayerName         = "flex"
	mentionsListLayerName = "mentionsList"
	confirmModalLayerName = "confirmModal"

	channelsPickerLayerName    = "channelsPicker"
	attachmentsPickerLayerName = "attachmentsPicker"
)

type Model struct {
	*layers.Layers

	// guildsTree (sidebar) + rightFlex
	mainFlex *flex.Model
	// messagesList + messageInput
	rightFlex *flex.Model

	guildsTree     *guildsTree
	messagesList   *messagesList
	messageInput   *messageInput
	channelsPicker *channelsPicker

	selectedChannel   *discord.Channel
	selectedChannelMu sync.RWMutex

	confirmModalDone          func(label string)
	confirmModalPreviousFocus tview.Model

	state  *ningen.State
	events chan gateway.Event

	typersMu sync.RWMutex
	typers   map[discord.UserID]*time.Timer

	app *tview.Application
	cfg *config.Config
}

func NewModel(app *tview.Application, cfg *config.Config, token string) *Model {
	m := &Model{
		Layers: layers.New(),

		mainFlex:  flex.NewModel(),
		rightFlex: flex.NewModel(),

		typers: make(map[discord.UserID]*time.Timer),

		app: app,
		cfg: cfg,
	}

	m.guildsTree = newGuildsTree(cfg, m)
	m.messagesList = newMessagesList(cfg, m)
	m.messageInput = newMessageInput(cfg, m)
	m.channelsPicker = newChannelsPicker(cfg, m)

	identifyProps := http.IdentifyProperties()
	gateway.DefaultIdentity = identifyProps
	gateway.DefaultPresence = &gateway.UpdatePresenceCommand{
		Status: m.cfg.Status,
	}

	id := gateway.DefaultIdentifier(token)
	id.Compress = false

	session := session.NewCustom(id, http.NewClient(token), handler.New())
	state := state.NewFromSession(session, defaultstore.New())
	m.state = ningen.FromState(state)
	m.state.DontWaitForReady = true

	m.events = make(chan gateway.Event)
	m.state.AddHandler(m.events)
	m.state.StateLog = func(err error) {
		slog.Error("state log", "err", err)
	}
	m.state.OnRequest = append(m.state.OnRequest, httputil.WithHeaders(http.Headers()), m.onRequest)

	m.SetBackgroundLayerStyle(m.cfg.Theme.Dialog.BackgroundStyle.Style)
	m.buildLayout()
	return m
}

func (m *Model) SelectedChannel() *discord.Channel {
	m.selectedChannelMu.RLock()
	defer m.selectedChannelMu.RUnlock()
	return m.selectedChannel
}

func (m *Model) SetSelectedChannel(channel *discord.Channel) {
	m.selectedChannelMu.Lock()
	m.selectedChannel = channel
	m.selectedChannelMu.Unlock()
}

func (m *Model) buildLayout() {
	m.Clear()
	m.rightFlex.Clear()
	m.mainFlex.Clear()

	m.rightFlex.
		SetDirection(flex.DirectionRow).
		AddItem(m.messagesList, 0, 1, false).
		AddItem(m.messageInput, 3, 1, false)
	m.mainFlex.
		AddItem(m.guildsTree, 0, 1, true).
		AddItem(m.rightFlex, 0, 4, false)

	m.AddLayer(m.mainFlex, layers.WithName(flexLayerName), layers.WithResize(true), layers.WithVisible(true))
	m.AddLayer(
		m.messageInput.mentionsList,
		layers.WithName(mentionsListLayerName),
		layers.WithResize(false),
		layers.WithVisible(false),
		layers.WithEnabled(false),
	)
}

func (m *Model) togglePicker() {
	if m.HasLayer(channelsPickerLayerName) {
		m.closePicker()
	} else {
		m.openPicker()
	}
}

func (m *Model) openPicker() {
	m.AddLayer(
		ui.Centered(m.channelsPicker, m.cfg.Picker.Width, m.cfg.Picker.Height),
		layers.WithName(channelsPickerLayerName),
		layers.WithResize(true),
		layers.WithVisible(true),
		layers.WithOverlay(),
	).SendToFront(channelsPickerLayerName)
	m.channelsPicker.update()
}

func (m *Model) closePicker() {
	m.RemoveLayer(channelsPickerLayerName)
	m.channelsPicker.Refresh()
}

func (m *Model) toggleGuildsTree() tview.Cmd {
	// The guilds tree is visible if the number of items is two.
	if m.mainFlex.GetItemCount() == 2 {
		m.mainFlex.RemoveItem(m.guildsTree)
		if m.guildsTree.HasFocus() {
			return tview.SetFocus(m.mainFlex)
		}
	} else {
		m.buildLayout()
		return tview.SetFocus(m.guildsTree)
	}
	return nil
}

func (m *Model) focusGuildsTree() tview.Cmd {
	// The guilds tree is not hidden if the number of items is two.
	if m.mainFlex.GetItemCount() == 2 {
		return tview.SetFocus(m.guildsTree)
	}
	return nil
}

func (m *Model) focusMessageInput() tview.Cmd {
	return tview.SetFocus(m.messageInput)
}

// matchesFocusMessageInput: many terminals report Ctrl+I as plain Tab. Old UI fixed this in
// Pages SetInputCapture (ebd226f). We only accept that Tab from guilds/messages list so Tab
// in the input still runs tab-complete.
//
// After focus changes (e.g. opening a URL in an external browser), tcell may instead deliver
// Ctrl+I as KeyTab with ModCtrl or KeyRune('\t') with ModCtrl — see tcell EventKey docs — so
// we treat those like Ctrl+I when the configured shortcut is ctrl+i.
func (m *Model) matchesFocusMessageInput(msg *tcell.EventKey) bool {
	if keybind.Matches(msg, m.cfg.Keybinds.FocusMessageInput.Keybind) {
		return true
	}
	wantTabAsShortcut := false
	for _, k := range m.cfg.Keybinds.FocusMessageInput.Keys() {
		if k == "ctrl+i" {
			wantTabAsShortcut = true
			break
		}
	}
	if !wantTabAsShortcut {
		return false
	}
	switch m.app.Focused() {
	case m.guildsTree, m.messagesList:
	default:
		return false
	}
	if msg.Key() == tcell.KeyTab && msg.Modifiers() == 0 {
		return true
	}
	if msg.Modifiers()&tcell.ModCtrl != 0 {
		if msg.Key() == tcell.KeyTab {
			return true
		}
		if msg.Key() == tcell.KeyRune && msg.Str() == "\t" {
			return true
		}
	}
	return false
}

// globalKeyCmd handles root-level shortcuts on chat.Model before Layers.Update runs.
func (m *Model) globalKeyCmd(msg tview.Msg) tview.Cmd {
	keyMsg, ok := msg.(tview.KeyMsg)
	if !ok {
		return nil
	}
	switch {
	case keybind.Matches(keyMsg, m.cfg.Keybinds.FocusGuildsTree.Keybind):
		m.messageInput.removeMentionsList()
		return m.focusGuildsTree()
	case keybind.Matches(keyMsg, m.cfg.Keybinds.FocusMessagesList.Keybind):
		m.messageInput.removeMentionsList()
		return m.focusMessagesList()
	case m.matchesFocusMessageInput(keyMsg):
		return m.focusMessageInput()
	case keybind.Matches(keyMsg, m.cfg.Keybinds.FocusPrevious.Keybind):
		return m.focusPrevious()
	case keybind.Matches(keyMsg, m.cfg.Keybinds.FocusNext.Keybind):
		return m.focusNext()
	case keybind.Matches(keyMsg, m.cfg.Keybinds.ToggleGuildsTree.Keybind):
		return m.toggleGuildsTree()
	case keybind.Matches(keyMsg, m.cfg.Keybinds.ToggleChannelsPicker.Keybind):
		m.togglePicker()
		return nil
	case keybind.Matches(keyMsg, m.cfg.Keybinds.Logout.Keybind):
		return tview.Sequence(m.closeState(), m.logout())
	}
	return nil
}

func (m *Model) focusMessagesList() tview.Cmd {
	return tview.SetFocus(m.messagesList)
}

func (m *Model) focusPrevious() tview.Cmd {
	switch m.app.Focused() {
	case m.guildsTree:
		if cmd := m.focusMessageInput(); cmd != nil {
			return cmd
		}
		return m.focusMessagesList()
	case m.messagesList:
		// Fallback when guilds/input are unavailable.
		if cmd := m.focusGuildsTree(); cmd != nil {
			return cmd
		}
		if cmd := m.focusMessageInput(); cmd != nil {
			return cmd
		}
		return m.focusMessagesList()
	case m.messageInput:
		return m.focusMessagesList()
	}
	return nil
}

func (m *Model) focusNext() tview.Cmd {
	switch m.app.Focused() {
	case m.guildsTree:
		return m.focusMessagesList()
	case m.messagesList:
		// Fallback when input/guilds are unavailable.
		if cmd := m.focusMessageInput(); cmd != nil {
			return cmd
		}
		if cmd := m.focusGuildsTree(); cmd != nil {
			return cmd
		}
	case m.messageInput:
		if cmd := m.focusGuildsTree(); cmd != nil {
			return cmd
		}
		return m.focusMessagesList()
	}
	return nil
}

func (m *Model) Update(msg tview.Msg) tview.Cmd {
	switch msg := msg.(type) {
	case tview.InitMsg:
		return tview.Batch(m.openState(), m.listen())
	case gateway.Event:
		switch eventMsg := msg.(type) {
		case *ws.RawEvent:
			m.onRaw(eventMsg)

		case *gateway.ReadyEvent:
			return tview.Batch(m.onReady(eventMsg), m.listen())

		case *gateway.MessageCreateEvent:
			return tview.Batch(m.onMessageCreate(eventMsg), m.listen())
		case *gateway.MessageUpdateEvent:
			m.onMessageUpdate(eventMsg)
		case *gateway.MessageDeleteEvent:
			m.onMessageDelete(eventMsg)

		case *gateway.GuildMembersChunkEvent:
			m.onGuildMembersChunk(eventMsg)
		case *gateway.GuildMemberRemoveEvent:
			m.onGuildMemberRemove(eventMsg)

		case *gateway.TypingStartEvent:
			if m.cfg.TypingIndicator.Receive {
				m.onTypingStart(eventMsg)
			}

		case *read.UpdateEvent:
			m.onReadUpdate(eventMsg)
		}
		return m.listen()
	case channelLoadedMsg:
		node := m.guildsTree.GetCurrentNode()
		if node == nil {
			return nil
		}
		channelID, ok := node.GetReference().(discord.ChannelID)
		if !ok || channelID != msg.Channel.ID {
			return nil
		}

		m.SetSelectedChannel(&msg.Channel)
		m.clearTypers()
		m.messageInput.stopTypingTimer()

		m.messagesList.reset()
		m.messagesList.setTitle(msg.Channel)
		m.messagesList.setMessages(msg.Messages)
		m.messagesList.ScrollBottom()

		isDM := msg.Channel.Type == discord.DirectMessage || msg.Channel.Type == discord.GroupDM
		hasNoPerm := !isDM && !m.state.HasPermissions(msg.Channel.ID, discord.PermissionSendMessages)
		m.messageInput.SetDisabled(hasNoPerm)

		text := "Message..."
		focusCmd := tview.Cmd(nil)

		if hasNoPerm {
			text = "You do not have permission to send messages in this channel."
		} else if m.cfg.AutoFocus {
			focusCmd = m.focusMessageInput()
		}
		m.messageInput.SetPlaceholder(tview.NewLine(tview.NewSegment(text, tcell.StyleDefault.Dim(true))))
		return focusCmd
	case QuitMsg:
		return m.closeState()
	case tview.ModalDoneMsg:
		if m.HasLayer(confirmModalLayerName) {
			m.RemoveLayer(confirmModalLayerName)
			var focusCmd tview.Cmd
			if m.confirmModalPreviousFocus != nil {
				focusCmd = tview.SetFocus(m.confirmModalPreviousFocus)
			}
			onDone := m.confirmModalDone
			m.confirmModalDone = nil
			m.confirmModalPreviousFocus = nil
			if onDone != nil {
				onDone(msg.ButtonLabel)
			}
			return focusCmd
		}
	case tview.KeyMsg:
		if cmd := m.globalKeyCmd(msg); cmd != nil {
			return cmd
		}
	case tabSuggestMsg:
		// Member search completes in a command goroutine; resume suggestion
		// generation on the update loop to keep UI mutations serialized.
		return m.messageInput.Update(msg)
	}
	return m.Layers.Update(msg)
}

func (m *Model) showConfirmModal(prompt string, buttons []string, onDone func(label string)) {
	m.confirmModalPreviousFocus = m.app.Focused()
	m.confirmModalDone = onDone

	modal := tview.NewModal().
		SetText(prompt).
		AddButtons(buttons)
	m.
		AddLayer(
			ui.Centered(modal, 0, 0),
			layers.WithName(confirmModalLayerName),
			layers.WithResize(true),
			layers.WithVisible(true),
			layers.WithOverlay(),
		).
		SendToFront(confirmModalLayerName)
}

func (m *Model) clearTypers() {
	m.typersMu.Lock()
	for _, timer := range m.typers {
		timer.Stop()
	}
	clear(m.typers)
	m.typersMu.Unlock()
	m.updateFooter()
}

func (m *Model) addTyper(userID discord.UserID) {
	m.typersMu.Lock()
	typer, ok := m.typers[userID]
	if ok {
		typer.Reset(typingDuration)
	} else {
		m.typers[userID] = time.AfterFunc(typingDuration, func() {
			m.removeTyper(userID)
		})
	}
	m.typersMu.Unlock()
	m.updateFooter()
}

func (m *Model) removeTyper(userID discord.UserID) {
	m.typersMu.Lock()
	if typer, ok := m.typers[userID]; ok {
		typer.Stop()
		delete(m.typers, userID)
	}
	m.typersMu.Unlock()
	m.updateFooter()
}

func (m *Model) updateFooter() {
	selectedChannel := m.SelectedChannel()
	if selectedChannel == nil {
		return
	}
	guildID := selectedChannel.GuildID

	m.typersMu.RLock()
	defer m.typersMu.RUnlock()

	var footer string
	if len(m.typers) > 0 {
		var names []string
		for userID := range m.typers {
			var name string
			if guildID.IsValid() {
				member, err := m.state.Cabinet.Member(guildID, userID)
				if err != nil {
					slog.Error("failed to get member from state", "err", err, "guild_id", guildID, "user_id", userID)
					continue
				}

				if member.Nick != "" {
					name = member.Nick
				} else {
					name = member.User.DisplayOrUsername()
				}
			} else {
				for _, recipient := range selectedChannel.DMRecipients {
					if recipient.ID == userID {
						name = recipient.DisplayOrUsername()
						break
					}
				}
			}

			if name != "" {
				names = append(names, name)
			}
		}

		switch len(names) {
		case 1:
			footer = names[0] + " is typing..."
		case 2:
			footer = fmt.Sprintf("%s and %s are typing...", names[0], names[1])
		case 3:
			footer = fmt.Sprintf("%s, %s, and %s are typing...", names[0], names[1], names[2])
		default:
			footer = "Several people are typing..."
		}
	}

	m.messagesList.SetFooter(footer)
}

// refreshUnreadStyles updates sidebar unread styling for a channel.
// Guild rows are always restyled; DMs are synced into the tree when missing.
func (m *Model) refreshUnreadStyles(channelID discord.ChannelID) {
	channel, ok := m.channelForSidebar(channelID)
	if !ok {
		return
	}

	if guildID := channel.GuildID; guildID.IsValid() {
		if guildNode := m.guildsTree.findNodeByReference(guildID); guildNode != nil {
			m.guildsTree.setNodeLineStyle(guildNode, m.guildsTree.guildNodeStyle(guildID))
		}
	} else if m.guildsTree.findNodeByReference(channelID) == nil {
		m.guildsTree.syncDMChannelNodes()
	}

	if channelNode := m.guildsTree.findNodeByReference(channelID); channelNode != nil {
		m.guildsTree.setNodeLineStyle(channelNode, m.guildsTree.channelNodeStyle(channel))
	}
}

func (m *Model) channelForSidebar(channelID discord.ChannelID) (discord.Channel, bool) {
	channel, err := m.state.Cabinet.Channel(channelID)
	if err == nil {
		return *channel, true
	}

	channels, err := m.state.PrivateChannels()
	if err != nil {
		return discord.Channel{}, false
	}
	for _, ch := range channels {
		if ch.ID == channelID {
			return ch, true
		}
	}
	return discord.Channel{}, false
}
