package chat

import (
	"fmt"
	"sort"

	"github.com/ayn2op/discordo/internal/config"
	"github.com/ayn2op/discordo/internal/ui"
	"github.com/ayn2op/tview"
	"github.com/diamondburned/arikawa/v3/discord"
	"github.com/diamondburned/ningen/v3"
	"github.com/gdamore/tcell/v3"
	"github.com/sahilm/fuzzy"
)

type quickSwitcher struct {
	*tview.Flex
	view *View
	cfg  *config.Config

	inputField *tview.InputField
	list       *tview.List

	candidates []channelCandidate
}

func newQuickSwitcher(view *View, cfg *config.Config) *quickSwitcher {
	qs := &quickSwitcher{
		Flex: tview.NewFlex(),
		view: view,
		cfg:  cfg,
	}

	qs.Flex.Box = ui.ConfigureBox(tview.NewBox(), &cfg.Theme)

	// Create input field
	qs.inputField = tview.NewInputField()
	qs.inputField.SetFieldBackgroundColor(tview.Styles.PrimitiveBackgroundColor)
	qs.inputField.SetFieldTextColor(tview.Styles.PrimaryTextColor)
	qs.inputField.SetLabel("Jump to: ")
	qs.inputField.SetChangedFunc(qs.onInputChanged)
	qs.inputField.SetDoneFunc(qs.onDone)
	qs.inputField.SetInputCapture(qs.onInputFieldCapture)

	// Create list for autocomplete suggestions
	qs.list = tview.NewList()
	qs.list.ShowSecondaryText(false)
	qs.list.SetSelectedFunc(qs.onListSelected)
	qs.list.SetInputCapture(qs.onListInputCapture)

	// Build layout: input field on top, list below
	qs.Flex.
		SetDirection(tview.FlexRow).
		AddItem(qs.inputField, 1, 0, true).
		AddItem(qs.list, 0, 1, false)

	return qs
}

type channelCandidate struct {
	name      string
	guildName string
	id        discord.ChannelID
	unread    bool
	mentioned bool
}

func (c channelCandidate) String() string {
	if c.guildName != "" {
		return fmt.Sprintf("%s (%s)", c.name, c.guildName)
	}
	return c.name
}

// displayText returns the text to display in the list with unread indicator
func (c channelCandidate) displayText() string {
	text := c.String()
	if c.unread {
		text = "• " + text
	}
	return text
}

// isUnread checks if a channel is unread using ChannelIsUnread which automatically excludes muted channels
func (qs *quickSwitcher) isUnread(channelID discord.ChannelID) (bool, bool) {
	// Use ChannelIsUnread with default options (excludes muted channels)
	indication := qs.view.state.ChannelIsUnread(channelID, ningen.UnreadOpts{})
	if indication == ningen.ChannelMentioned {
		return true, true // mentioned (which is also unread)
	}
	if indication == ningen.ChannelUnread {
		return true, false // unread but not mentioned
	}
	return false, false // read or muted
}

// isMuted checks if a channel is muted by comparing unread status with and without IncludeMutedCategories
func (qs *quickSwitcher) isMuted(channelID discord.ChannelID) bool {
	// If channel is unread with muted categories but not with default options, it's muted
	withMuted := qs.view.state.ChannelIsUnread(channelID, ningen.UnreadOpts{IncludeMutedCategories: true})
	withoutMuted := qs.view.state.ChannelIsUnread(channelID, ningen.UnreadOpts{})
	
	// If it's unread with muted categories but read without, it means it's muted
	return (withMuted == ningen.ChannelUnread || withMuted == ningen.ChannelMentioned) &&
		(withoutMuted == ningen.ChannelRead)
}

// isGuildMuted checks if a guild is muted by comparing unread status with and without IncludeMutedCategories
func (qs *quickSwitcher) isGuildMuted(guildID discord.GuildID) bool {
	// If guild is unread with muted categories but not with default options, it's muted
	withMuted := qs.view.state.GuildIsUnread(guildID, ningen.GuildUnreadOpts{UnreadOpts: ningen.UnreadOpts{IncludeMutedCategories: true}})
	withoutMuted := qs.view.state.GuildIsUnread(guildID, ningen.GuildUnreadOpts{UnreadOpts: ningen.UnreadOpts{}})
	
	// If it's unread with muted categories but read without, it means it's muted
	// GuildIsUnread returns UnreadIndication (same as ChannelIsUnread)
	return (withMuted == ningen.ChannelUnread || withMuted == ningen.ChannelMentioned) &&
		(withoutMuted == ningen.ChannelRead)
}

func (qs *quickSwitcher) onInputChanged(text string) {
	qs.updateAutocompleteList(text)
}

func (qs *quickSwitcher) updateAutocompleteList(currentText string) {
	if qs.view.state == nil || qs.view.state.Cabinet == nil {
		qs.list.Clear()
		return
	}

	var candidates []channelCandidate

	// Build list of all non-muted channels
	// Guild Channels
	guilds, _ := qs.view.state.Cabinet.Guilds()
	for _, guild := range guilds {
		// Skip muted guilds
		if qs.isGuildMuted(guild.ID) {
			continue
		}
		channels, _ := qs.view.state.Cabinet.Channels(guild.ID)
		for _, ch := range channels {
			if ch.Type == discord.GuildText || ch.Type == discord.GuildNews || ch.Type == discord.GuildPublicThread || ch.Type == discord.GuildPrivateThread || ch.Type == discord.GuildAnnouncementThread {
				// Skip muted channels
				if qs.isMuted(ch.ID) {
					continue
				}
				unread, mentioned := qs.isUnread(ch.ID)
				candidates = append(candidates, channelCandidate{
					name:      "#" + ch.Name,
					guildName: guild.Name,
					id:        ch.ID,
					unread:    unread,
					mentioned: mentioned,
				})
			}
		}
	}

	// DM Channels
	privateChannels, _ := qs.view.state.PrivateChannels()
	for _, ch := range privateChannels {
		// Skip muted channels
		if qs.isMuted(ch.ID) {
			continue
		}
		name := "Direct Message"
		if len(ch.DMRecipients) > 0 {
			name = ch.DMRecipients[0].Tag()
		}
		if ch.Name != "" {
			name = ch.Name
		}

		unread, mentioned := qs.isUnread(ch.ID)
		candidates = append(candidates, channelCandidate{
			name:      name,
			guildName: "Direct Messages",
			id:        ch.ID,
			unread:    unread,
			mentioned: mentioned,
		})
	}

	// If input is empty, filter to only unread channels
	if currentText == "" {
		var unreadCandidates []channelCandidate
		for _, c := range candidates {
			if c.unread {
				unreadCandidates = append(unreadCandidates, c)
			}
		}
		candidates = unreadCandidates

		// Sort unread channels: mentioned first, then unread
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].mentioned && !candidates[j].mentioned {
				return true
			}
			if !candidates[i].mentioned && candidates[j].mentioned {
				return false
			}
			// Both have same mention status, maintain original order
			return false
		})
	} else {
		// When there's text, use fuzzy search on all non-muted channels
		var candidateStrings []string
		for _, c := range candidates {
			candidateStrings = append(candidateStrings, c.String())
		}

		matches := fuzzy.Find(currentText, candidateStrings)
		sort.SliceStable(matches, func(i, j int) bool {
			return matches[i].Score > matches[j].Score
		})

		var matchedCandidates []channelCandidate
		for _, match := range matches {
			candidate := candidates[match.Index]
			matchedCandidates = append(matchedCandidates, candidate)
		}
		candidates = matchedCandidates
	}

	qs.candidates = candidates

	if len(qs.candidates) > 10 {
		qs.candidates = qs.candidates[:10]
	}

	// Update the list
	qs.list.Clear()
	for _, candidate := range qs.candidates {
		text := candidate.displayText()
		qs.list.AddItem(text, "", 0, nil)
	}

	// If there are suggestions, switch focus to list when arrow key is pressed
	if len(qs.candidates) > 0 {
		qs.list.SetCurrentItem(0)
	}
}

func (qs *quickSwitcher) onListSelected(index int, mainText, secondaryText string, shortcut rune) {
	if index >= 0 && index < len(qs.candidates) {
		candidate := qs.candidates[index]
		qs.view.guildsTree.SelectChannelID(candidate.id)
		qs.view.toggleQuickSwitcher()
	}
}

func (qs *quickSwitcher) onListInputCapture(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyEnter:
		index := qs.list.GetCurrentItem()
		if index >= 0 && index < len(qs.candidates) {
			candidate := qs.candidates[index]
			qs.view.guildsTree.SelectChannelID(candidate.id)
			qs.view.toggleQuickSwitcher()
		}
		return nil
	case tcell.KeyEscape:
		qs.view.toggleQuickSwitcher()
		return nil
	case tcell.KeyUp:
		if qs.list.GetCurrentItem() == 0 {
			// Move focus back to input field
			qs.view.app.SetFocus(qs.inputField)
			return nil
		}
	case tcell.KeyTab:
		// Tab moves focus between input and list
		if qs.view.app.GetFocus() == qs.inputField {
			qs.view.app.SetFocus(qs.list)
			return nil
		} else if qs.view.app.GetFocus() == qs.list {
			qs.view.app.SetFocus(qs.inputField)
			return nil
		}
	}
	return event
}

func (qs *quickSwitcher) onInputFieldCapture(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyDown:
		// Move focus to list if there are suggestions
		if len(qs.candidates) > 0 {
			qs.view.app.SetFocus(qs.list)
			return nil
		}
	case tcell.KeyTab:
		// Move focus to list if there are suggestions
		if len(qs.candidates) > 0 {
			qs.view.app.SetFocus(qs.list)
			return nil
		}
	}
	return event
}

func (qs *quickSwitcher) onDone(key tcell.Key) {
	switch key {
	case tcell.KeyEnter:
		// If there are candidates, select the first one
		if len(qs.candidates) > 0 {
			candidate := qs.candidates[0]
			qs.view.guildsTree.SelectChannelID(candidate.id)
			qs.view.toggleQuickSwitcher()
		} else {
			// Try to match exact text
			text := qs.inputField.GetText()
			for _, c := range qs.candidates {
				if c.String() == text {
					qs.view.guildsTree.SelectChannelID(c.id)
					qs.view.toggleQuickSwitcher()
					return
				}
			}
		}
	case tcell.KeyEscape:
		qs.view.toggleQuickSwitcher()
	}
}
