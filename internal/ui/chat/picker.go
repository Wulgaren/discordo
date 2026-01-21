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

type picker struct {
	*tview.Flex
	view *View
	cfg  *config.Config

	inputField *tview.InputField
	list       *tview.List

	candidates []channelCandidate
}

func newPicker(view *View, cfg *config.Config) *picker {
	p := &picker{
		Flex: tview.NewFlex(),
		view: view,
		cfg:  cfg,
	}

	p.Box = ui.ConfigureBox(tview.NewBox(), &cfg.Theme)

	// Create input field
	p.inputField = tview.NewInputField()
	p.inputField.SetLabel("> ")
	p.inputField.SetChangedFunc(p.onInputChanged)
	p.inputField.SetDoneFunc(p.onDone)
	p.inputField.SetInputCapture(p.onInputFieldCapture)

	// Create list for autocomplete suggestions
	p.list = tview.NewList()
	p.list.ShowSecondaryText(false)
	p.list.SetSelectedFunc(p.onListSelected)

	// Build layout: input field on top, list below
	p.Flex.
		SetDirection(tview.FlexRow).
		AddItem(p.inputField, 1, 0, true).
		AddItem(p.list, 0, 1, false)

	return p
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
func (p *picker) isUnread(channelID discord.ChannelID) (bool, bool) {
	// Use ChannelIsUnread with default options (excludes muted channels)
	indication := p.view.state.ChannelIsUnread(channelID, ningen.UnreadOpts{})
	if indication == ningen.ChannelMentioned {
		return true, true // mentioned (which is also unread)
	}
	if indication == ningen.ChannelUnread {
		return true, false // unread but not mentioned
	}
	return false, false // read or muted
}

// isMuted checks if a channel is muted by comparing unread status with and without IncludeMutedCategories
func (p *picker) isMuted(channelID discord.ChannelID) bool {
	// If channel is unread with muted categories but not with default options, it's muted
	withMuted := p.view.state.ChannelIsUnread(channelID, ningen.UnreadOpts{IncludeMutedCategories: true})
	withoutMuted := p.view.state.ChannelIsUnread(channelID, ningen.UnreadOpts{})
	
	// If it's unread with muted categories but read without, it means it's muted
	return (withMuted == ningen.ChannelUnread || withMuted == ningen.ChannelMentioned) &&
		(withoutMuted == ningen.ChannelRead)
}

// isGuildMuted checks if a guild is muted by comparing unread status with and without IncludeMutedCategories
func (p *picker) isGuildMuted(guildID discord.GuildID) bool {
	// If guild is unread with muted categories but not with default options, it's muted
	withMuted := p.view.state.GuildIsUnread(guildID, ningen.GuildUnreadOpts{UnreadOpts: ningen.UnreadOpts{IncludeMutedCategories: true}})
	withoutMuted := p.view.state.GuildIsUnread(guildID, ningen.GuildUnreadOpts{UnreadOpts: ningen.UnreadOpts{}})
	
	// If it's unread with muted categories but read without, it means it's muted
	// GuildIsUnread returns UnreadIndication (same as ChannelIsUnread)
	return (withMuted == ningen.ChannelUnread || withMuted == ningen.ChannelMentioned) &&
		(withoutMuted == ningen.ChannelRead)
}

func (p *picker) onInputChanged(text string) {
	p.updateAutocompleteList(text)
}

func (p *picker) updateAutocompleteList(currentText string) {
	if p.view.state == nil || p.view.state.Cabinet == nil {
		p.list.Clear()
		return
	}

	var candidates []channelCandidate

	// Build list of all non-muted channels
	// Guild Channels
	guilds, err := p.view.state.Cabinet.Guilds()
	if err != nil {
		return
	}
	for _, guild := range guilds {
		// Skip muted guilds
		if p.isGuildMuted(guild.ID) {
			continue
		}
		channels, err := p.view.state.Cabinet.Channels(guild.ID)
		if err != nil {
			continue
		}
		for _, ch := range channels {
			if ch.Type == discord.GuildText || ch.Type == discord.GuildNews || ch.Type == discord.GuildPublicThread || ch.Type == discord.GuildPrivateThread || ch.Type == discord.GuildAnnouncementThread {
				// Skip muted channels
				if p.isMuted(ch.ID) {
					continue
				}
				unread, mentioned := p.isUnread(ch.ID)
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
	privateChannels, err := p.view.state.PrivateChannels()
	if err != nil {
		return
	}
	for _, ch := range privateChannels {
		// Skip muted channels
		if p.isMuted(ch.ID) {
			continue
		}
		name := "Direct Message"
		if len(ch.DMRecipients) > 0 {
			name = ch.DMRecipients[0].Tag()
		}
		if ch.Name != "" {
			name = ch.Name
		}

		unread, mentioned := p.isUnread(ch.ID)
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
		matches := fuzzy.FindFrom(currentText, candidateList(candidates))
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

	p.candidates = candidates

	if len(p.candidates) > 10 {
		p.candidates = p.candidates[:10]
	}

	// Update the list
	p.list.Clear()
	for _, candidate := range p.candidates {
		text := candidate.displayText()
		p.list.AddItem(text, "", 0, nil)
	}

	// Reset list selection to first item when candidates are updated
	if len(p.candidates) > 0 {
		p.list.SetCurrentItem(0)
	}
}

type candidateList []channelCandidate

func (cl candidateList) String(i int) string {
	return cl[i].String()
}

func (cl candidateList) Len() int {
	return len(cl)
}

func (p *picker) onListSelected(index int, mainText, secondaryText string, shortcut rune) {
	if index >= 0 && index < len(p.candidates) {
		candidate := p.candidates[index]
		p.view.guildsTree.SelectChannelID(candidate.id)
		p.view.togglePicker()
	}
}

func (p *picker) onInputFieldCapture(event *tcell.EventKey) *tcell.EventKey {
	if len(p.candidates) > 0 {
		switch event.Name() {
		case p.cfg.Keys.Picker.Up:
			p.list.InputHandler()(tcell.NewEventKey(tcell.KeyUp, "", tcell.ModNone), nil)
			return nil
		case p.cfg.Keys.Picker.Down:
			p.list.InputHandler()(tcell.NewEventKey(tcell.KeyDown, "", tcell.ModNone), nil)
			return nil
		case p.cfg.Keys.Picker.Confirm:
			index := p.list.GetCurrentItem()
			if index >= 0 && index < len(p.candidates) {
				candidate := p.candidates[index]
				p.view.guildsTree.SelectChannelID(candidate.id)
				p.view.togglePicker()
			}
			return nil
		}
	}

	switch event.Name() {
	case p.cfg.Keys.Picker.Cancel:
		p.view.togglePicker()
		return nil
	}
	return event
}

func (p *picker) onDone(key tcell.Key) {
	switch key {
	case tcell.KeyEnter:
		// If there are candidates, select the first one
		if len(p.candidates) > 0 {
			candidate := p.candidates[0]
			p.view.guildsTree.SelectChannelID(candidate.id)
			p.view.togglePicker()
		}
	case tcell.KeyEscape:
		p.view.togglePicker()
	}
}
